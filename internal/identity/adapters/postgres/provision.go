package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// ProvisionStore runs control-plane identity mutations on the schema owner pool.
type ProvisionStore struct {
	Pool      *pgxpool.Pool
	Emails    app.EmailSealer
	Blobs     app.AvatarBlobs
	PublicURL string
	Now       func() time.Time
}

func (s ProvisionStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// ProvisionMemberInput is the payload for creating a provisioned member.
type ProvisionMemberInput struct {
	ExternalID string
	Email      string
	Name       string
	Role       string
	Active     *bool
	Teams      []string
	KeyID      string
}

// ProvisionMemberUpdate is the payload for updating a provisioned member.
type ProvisionMemberUpdate struct {
	Email  string
	Name   string
	Role   string
	Active *bool
	Teams  []string
	KeyID  string
}

// ProvisionMember is the provisioned member representation returned by the API.
type ProvisionMember struct {
	ID         string   `json:"id"`
	ExternalID string   `json:"external_id"`
	Email      string   `json:"email"`
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Active     bool     `json:"active"`
	Teams      []string `json:"teams"`
}

// ProvisionTeamInput is the payload for creating a provisioned team.
type ProvisionTeamInput struct {
	ExternalID string
	Name       string
	Members    []string
	KeyID      string
}

// ProvisionTeamUpdate is the payload for updating a provisioned team.
type ProvisionTeamUpdate struct {
	Name    string
	Members []string
	KeyID   string
}

// ProvisionTeam is the provisioned team representation returned by the API.
type ProvisionTeam struct {
	ID         string   `json:"id"`
	ExternalID string   `json:"external_id"`
	Name       string   `json:"name"`
	Members    []string `json:"members"`
}

// ProvisionLink is one minted SSO or SSO step-up link.
type ProvisionLink struct {
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	Purpose   string    `json:"purpose"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ProvisionQuota is the workspace spend ceiling state.
type ProvisionQuota struct {
	SpendCeilingMicroUSD *int64    `json:"spend_ceiling_micro_usd"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func cleanMemberEmail(raw string) (string, error) {
	email := strings.TrimSpace(raw)
	if email == "" || !strings.Contains(email, "@") || utf8.RuneCountInString(email) > 320 || strings.ContainsFunc(email, unicode.IsControl) || !utf8.ValidString(email) {
		return "", fmt.Errorf("%w: invalid email", domain.ErrInvalidProvisionInput)
	}
	return email, nil
}

func cleanMemberName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > 255 || strings.ContainsFunc(name, unicode.IsControl) || !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: invalid name", domain.ErrInvalidProvisionInput)
	}
	return name, nil
}

// CreateMember inserts a provisioned user keyed by external_id.
func (s ProvisionStore) CreateMember(ctx context.Context, in ProvisionMemberInput) (ProvisionMember, error) {
	extID, err := domain.CleanExternalID(in.ExternalID)
	if err != nil {
		return ProvisionMember{}, err
	}
	email, err := cleanMemberEmail(in.Email)
	if err != nil {
		return ProvisionMember{}, err
	}
	name, err := cleanMemberName(in.Name)
	if err != nil {
		return ProvisionMember{}, err
	}
	role, err := domain.MapProvisionRole(in.Role)
	if err != nil {
		return ProvisionMember{}, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}

	stored, hashes, keyID, err := sealEmail(s.Emails, email)
	if err != nil {
		return ProvisionMember{}, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ProvisionMember{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users WHERE external_id = $1)`, extID).Scan(&exists); err != nil {
		return ProvisionMember{}, err
	}
	if exists {
		return ProvisionMember{}, domain.ErrExternalIDConflict
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users WHERE email_hash = ANY($1))`, hashes).Scan(&exists); err != nil {
		return ProvisionMember{}, err
	}
	if exists {
		return ProvisionMember{}, domain.ErrEmailConflict
	}

	var roleID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = $1`, string(role)).Scan(&roleID); err != nil {
		return ProvisionMember{}, err
	}

	teamIDs, teamExtIDs, err := resolveTeamsByExternalID(ctx, tx, in.Teams)
	if err != nil {
		return ProvisionMember{}, err
	}

	now := s.now()
	var userID string
	if active {
		err = tx.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, external_id, role_id, invited_at, joined_at)
			VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $7)
			RETURNING id::text`, stored, hashes[0], keyID, name, extID, roleID, now).Scan(&userID)
	} else {
		err = tx.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, external_id, role_id, invited_at, disabled_at)
			VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $7)
			RETURNING id::text`, stored, hashes[0], keyID, name, extID, roleID, now).Scan(&userID)
	}
	if err != nil {
		return ProvisionMember{}, mapProvisionUserErr(err)
	}

	if active {
		for _, tid := range teamIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, tid, userID); err != nil {
				return ProvisionMember{}, mapProvisionUserErr(err)
			}
		}
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionMemberCreate,
		Outcome:    audit.Success,
		TargetType: "member",
		TargetID:   userID,
		Changes: audit.Changes{
			Fields: []string{"active", "email", "key_id", "name", "role", "teams"},
			Values: map[string]audit.Change{
				"key_id": {To: in.KeyID},
				"role":   {To: string(role)},
				"active": {To: active},
				"teams":  {To: teamExtIDs},
			},
		},
	}); err != nil {
		return ProvisionMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProvisionMember{}, mapProvisionUserErr(err)
	}

	if !active {
		teamExtIDs = []string{}
	}
	return ProvisionMember{
		ID:         userID,
		ExternalID: extID,
		Email:      email,
		Name:       name,
		Role:       string(role),
		Active:     active,
		Teams:      teamExtIDs,
	}, nil
}

// UpdateMember updates a provisioned user by external_id.
func (s ProvisionStore) UpdateMember(ctx context.Context, externalID string, in ProvisionMemberUpdate) (ProvisionMember, error) {
	extID, err := domain.CleanExternalID(externalID)
	if err != nil {
		return ProvisionMember{}, err
	}
	email, err := cleanMemberEmail(in.Email)
	if err != nil {
		return ProvisionMember{}, err
	}
	name, err := cleanMemberName(in.Name)
	if err != nil {
		return ProvisionMember{}, err
	}
	role, err := domain.MapProvisionRole(in.Role)
	if err != nil {
		return ProvisionMember{}, err
	}

	stored, hashes, keyID, err := sealEmail(s.Emails, email)
	if err != nil {
		return ProvisionMember{}, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ProvisionMember{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userID     string
		prevRole   string
		disabledAt *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT u.id::text, COALESCE(r.key, ''), u.disabled_at
		FROM identity.users AS u
		JOIN identity.roles AS r ON r.id = u.role_id
		WHERE u.external_id = $1
		FOR NO KEY UPDATE OF u`, extID).Scan(&userID, &prevRole, &disabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProvisionMember{}, domain.ErrNoSuchMember
	}
	if err != nil {
		return ProvisionMember{}, err
	}

	var emailTaken bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM identity.users
			WHERE email_hash = ANY($1) AND id <> $2::uuid
		)`, hashes, userID).Scan(&emailTaken); err != nil {
		return ProvisionMember{}, err
	}
	if emailTaken {
		return ProvisionMember{}, domain.ErrEmailConflict
	}

	var roleID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = $1`, string(role)).Scan(&roleID); err != nil {
		return ProvisionMember{}, err
	}

	var teamIDs []string
	var teamExtIDs []string
	if in.Teams != nil {
		teamIDs, teamExtIDs, err = resolveTeamsByExternalID(ctx, tx, in.Teams)
		if err != nil {
			return ProvisionMember{}, err
		}
	}

	wasActive := disabledAt == nil
	active := wasActive
	if in.Active != nil {
		active = *in.Active
	}
	now := s.now()

	var sessionsEnded, tokensRevoked bool
	if active {
		if _, err := tx.Exec(ctx, `
			UPDATE identity.users
			SET email = $2,
			    email_hash = $3,
			    email_hash_key_id = $4,
			    name = $5,
			    role_id = $6::uuid,
			    disabled_at = NULL,
			    invited_at = COALESCE(invited_at, $7),
			    joined_at = COALESCE(joined_at, $7)
			WHERE id = $1::uuid`, userID, stored, hashes[0], keyID, name, roleID, now); err != nil {
			return ProvisionMember{}, mapProvisionUserErr(err)
		}
		if in.Teams != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM identity.team_members WHERE user_id = $1::uuid`, userID); err != nil {
				return ProvisionMember{}, err
			}
			for _, tid := range teamIDs {
				if _, err := tx.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, tid, userID); err != nil {
					return ProvisionMember{}, mapProvisionUserErr(err)
				}
			}
		} else {
			teamExtIDs, err = memberTeamExternalIDs(ctx, tx, userID)
			if err != nil {
				return ProvisionMember{}, err
			}
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE identity.users
			SET email = $2,
			    email_hash = $3,
			    email_hash_key_id = $4,
			    name = $5,
			    role_id = $6::uuid,
			    disabled_at = COALESCE(disabled_at, $7),
			    password_hash = NULL,
			    webauthn_handle = NULL
			WHERE id = $1::uuid`, userID, stored, hashes[0], keyID, name, roleID, now); err != nil {
			return ProvisionMember{}, mapProvisionUserErr(err)
		}
		sessionsEnded, tokensRevoked, err = revokeMemberAccessTx(ctx, tx, userID)
		if err != nil {
			return ProvisionMember{}, err
		}
		teamExtIDs = []string{}
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionMemberUpdate,
		Outcome:    audit.Success,
		TargetType: "member",
		TargetID:   userID,
		Changes: audit.Changes{
			Fields: []string{"active", "email", "key_id", "name", "role", "sessions_ended", "teams", "tokens_revoked"},
			Values: map[string]audit.Change{
				"key_id":         {To: in.KeyID},
				"role":           {From: prevRole, To: string(role)},
				"active":         {From: wasActive, To: active},
				"teams":          {To: teamExtIDs},
				"sessions_ended": {To: sessionsEnded},
				"tokens_revoked": {To: tokensRevoked},
			},
		},
	}); err != nil {
		return ProvisionMember{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProvisionMember{}, mapProvisionUserErr(err)
	}

	return ProvisionMember{
		ID:         userID,
		ExternalID: extID,
		Email:      email,
		Name:       name,
		Role:       string(role),
		Active:     active,
		Teams:      teamExtIDs,
	}, nil
}

// DeleteMember disables a provisioned member by external_id, immediately ending
// all active sessions and revoking all API tokens.
func (s ProvisionStore) DeleteMember(ctx context.Context, externalID, keyID string) error {
	extID, err := domain.CleanExternalID(externalID)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID, oldAvatarKey string
	err = tx.QueryRow(ctx, `
		SELECT id::text, COALESCE(avatar_key, '')
		FROM identity.users
		WHERE external_id = $1
		FOR NO KEY UPDATE`, extID).Scan(&userID, &oldAvatarKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchMember
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE identity.users
		SET disabled_at = COALESCE(disabled_at, $2),
		    password_hash = NULL,
		    webauthn_handle = NULL,
		    avatar_key = NULL
		WHERE id = $1::uuid`, userID, s.now()); err != nil {
		return mapProvisionUserErr(err)
	}
	sessionsEnded, tokensRevoked, err := revokeMemberAccessTx(ctx, tx, userID)
	if err != nil {
		return err
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionMemberDelete,
		Outcome:    audit.Success,
		TargetType: "member",
		TargetID:   userID,
		Changes: audit.Changes{
			Fields: []string{"key_id", "sessions_ended", "tokens_revoked"},
			Values: map[string]audit.Change{
				"key_id":         {To: keyID},
				"sessions_ended": {To: sessionsEnded},
				"tokens_revoked": {To: tokensRevoked},
			},
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapProvisionUserErr(err)
	}
	if oldAvatarKey != "" && s.Blobs != nil {
		_ = s.Blobs.Remove(ctx, oldAvatarKey)
	}
	return nil
}

// CreateTeam creates a provisioned team keyed by external_id.
func (s ProvisionStore) CreateTeam(ctx context.Context, in ProvisionTeamInput) (ProvisionTeam, error) {
	extID, err := domain.CleanExternalID(in.ExternalID)
	if err != nil {
		return ProvisionTeam{}, err
	}
	name, err := domain.ParseTeamName(in.Name)
	if err != nil {
		return ProvisionTeam{}, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ProvisionTeam{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.teams WHERE external_id = $1)`, extID).Scan(&exists); err != nil {
		return ProvisionTeam{}, err
	}
	if exists {
		return ProvisionTeam{}, domain.ErrExternalIDConflict
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.teams WHERE lower(name) = lower($1))`, name).Scan(&exists); err != nil {
		return ProvisionTeam{}, err
	}
	if exists {
		return ProvisionTeam{}, domain.ErrTeamNameTaken
	}

	memberIDs, memberExtIDs, err := resolveMembersByExternalID(ctx, tx, in.Members)
	if err != nil {
		return ProvisionTeam{}, err
	}

	var teamID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.teams (name, external_id)
		VALUES ($1, $2)
		RETURNING id::text`, name, extID).Scan(&teamID); err != nil {
		return ProvisionTeam{}, mapProvisionTeamErr(err)
	}
	for _, uid := range memberIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, teamID, uid); err != nil {
			return ProvisionTeam{}, mapProvisionTeamErr(err)
		}
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionTeamCreate,
		Outcome:    audit.Success,
		TargetType: "team",
		TargetID:   teamID,
		Changes: audit.Changes{
			Fields: []string{"key_id", "members", "name"},
			Values: map[string]audit.Change{
				"key_id":  {To: in.KeyID},
				"members": {To: memberExtIDs},
			},
		},
	}); err != nil {
		return ProvisionTeam{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProvisionTeam{}, mapProvisionTeamErr(err)
	}
	return ProvisionTeam{
		ID:         teamID,
		ExternalID: extID,
		Name:       name,
		Members:    memberExtIDs,
	}, nil
}

// UpdateTeam updates a provisioned team's name and member list by external_id.
func (s ProvisionStore) UpdateTeam(ctx context.Context, externalID string, in ProvisionTeamUpdate) (ProvisionTeam, error) {
	extID, err := domain.CleanExternalID(externalID)
	if err != nil {
		return ProvisionTeam{}, err
	}
	name, err := domain.ParseTeamName(in.Name)
	if err != nil {
		return ProvisionTeam{}, err
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ProvisionTeam{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var teamID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM identity.teams WHERE external_id = $1 FOR UPDATE`, extID).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProvisionTeam{}, domain.ErrNoSuchTeam
	}
	if err != nil {
		return ProvisionTeam{}, err
	}

	var nameTaken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.teams WHERE lower(name) = lower($1) AND id <> $2::uuid)`, name, teamID).Scan(&nameTaken); err != nil {
		return ProvisionTeam{}, err
	}
	if nameTaken {
		return ProvisionTeam{}, domain.ErrTeamNameTaken
	}

	if _, err := tx.Exec(ctx, `UPDATE identity.teams SET name = $2 WHERE id = $1::uuid`, teamID, name); err != nil {
		return ProvisionTeam{}, mapProvisionTeamErr(err)
	}

	var memberExtIDs []string
	if in.Members != nil {
		var memberIDs []string
		memberIDs, memberExtIDs, err = resolveMembersByExternalID(ctx, tx, in.Members)
		if err != nil {
			return ProvisionTeam{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity.team_members WHERE team_id = $1::uuid`, teamID); err != nil {
			return ProvisionTeam{}, err
		}
		for _, uid := range memberIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, teamID, uid); err != nil {
				return ProvisionTeam{}, mapProvisionTeamErr(err)
			}
		}
	} else {
		memberExtIDs, err = teamMemberExternalIDs(ctx, tx, teamID)
		if err != nil {
			return ProvisionTeam{}, err
		}
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionTeamUpdate,
		Outcome:    audit.Success,
		TargetType: "team",
		TargetID:   teamID,
		Changes: audit.Changes{
			Fields: []string{"key_id", "members", "name"},
			Values: map[string]audit.Change{
				"key_id":  {To: in.KeyID},
				"members": {To: memberExtIDs},
			},
		},
	}); err != nil {
		return ProvisionTeam{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProvisionTeam{}, mapProvisionTeamErr(err)
	}
	return ProvisionTeam{
		ID:         teamID,
		ExternalID: extID,
		Name:       name,
		Members:    memberExtIDs,
	}, nil
}

// DeleteTeam deletes a provisioned team by external_id.
func (s ProvisionStore) DeleteTeam(ctx context.Context, externalID, keyID string) error {
	extID, err := domain.CleanExternalID(externalID)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var teamID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM identity.teams WHERE external_id = $1 FOR UPDATE`, extID).Scan(&teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchTeam
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM identity.team_members WHERE team_id = $1::uuid`, teamID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity.shares WHERE team_id = $1::uuid`, teamID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity.teams WHERE id = $1::uuid`, teamID); err != nil {
		return err
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionTeamDelete,
		Outcome:    audit.Success,
		TargetType: "team",
		TargetID:   teamID,
		Changes: audit.Changes{
			Fields: []string{"key_id"},
			Values: map[string]audit.Change{
				"key_id": {To: keyID},
			},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IssueProvisionLink mints a one-time 'sso' or 'sso_stepup' link for an
// enabled member keyed by external_id.
func (s ProvisionStore) IssueProvisionLink(ctx context.Context, externalID, purpose, keyID string) (ProvisionLink, error) {
	if purpose != "sso" && purpose != "sso_stepup" {
		return ProvisionLink{}, fmt.Errorf("%w: purpose must be sso or sso_stepup", domain.ErrInvalidProvisionInput)
	}
	extID, err := domain.CleanExternalID(externalID)
	if err != nil {
		return ProvisionLink{}, err
	}
	life, ok := domain.LinkLifetime(purpose)
	if !ok {
		return ProvisionLink{}, domain.ErrInvalidProvisionInput
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ProvisionLink{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID string
	var disabledAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id::text, disabled_at
		FROM identity.users
		WHERE external_id = $1`, extID).Scan(&userID, &disabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProvisionLink{}, domain.ErrNoSuchMember
	}
	if err != nil {
		return ProvisionLink{}, err
	}
	if disabledAt != nil {
		return ProvisionLink{}, domain.ErrMemberDisabled
	}

	token, text, err := domain.NewToken()
	if err != nil {
		return ProvisionLink{}, err
	}
	expiresAt := s.now().Add(life)
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2::uuid, $3, $4)`, domain.Hash(token), userID, purpose, expiresAt); err != nil {
		return ProvisionLink{}, err
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionSignInLinkIssue,
		Outcome:    audit.Success,
		TargetType: "sign_in_link",
		TargetID:   userID,
		Changes: audit.Changes{
			Fields: []string{"key_id", "purpose"},
			Values: map[string]audit.Change{
				"key_id":  {To: keyID},
				"purpose": {To: purpose},
			},
		},
	}); err != nil {
		return ProvisionLink{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProvisionLink{}, err
	}
	return ProvisionLink{
		Token:     text,
		URL:       strings.TrimRight(s.PublicURL, "/") + "/login/link#" + text,
		Purpose:   purpose,
		ExpiresAt: expiresAt.UTC(),
	}, nil
}

// SetSpendCeiling sets identity.workspace.spend_ceiling_micro_usd on the owner pool.
func (s ProvisionStore) SetSpendCeiling(ctx context.Context, microUSD int64, keyID string) (ProvisionQuota, error) {
	if microUSD < 0 {
		return ProvisionQuota{}, domain.ErrInvalidQuota
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return ProvisionQuota{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var prev *int64
	if err := tx.QueryRow(ctx, `SELECT spend_ceiling_micro_usd FROM identity.workspace WHERE id = true FOR UPDATE`).Scan(&prev); err != nil {
		return ProvisionQuota{}, err
	}
	now := s.now()
	var updatedAt time.Time
	if err := tx.QueryRow(ctx, `
		UPDATE identity.workspace
		SET spend_ceiling_micro_usd = $1, updated_at = $2
		WHERE id = true
		RETURNING updated_at`, microUSD, now).Scan(&updatedAt); err != nil {
		return ProvisionQuota{}, err
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionQuotaSet,
		Outcome:    audit.Success,
		TargetType: "quota",
		TargetID:   "workspace",
		Changes: audit.Changes{
			Fields: []string{"key_id", "spend_ceiling_micro_usd"},
			Values: map[string]audit.Change{
				"key_id":                  {To: keyID},
				"spend_ceiling_micro_usd": {From: prev, To: microUSD},
			},
		},
	}); err != nil {
		return ProvisionQuota{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProvisionQuota{}, err
	}
	val := microUSD
	return ProvisionQuota{
		SpendCeilingMicroUSD: &val,
		UpdatedAt:            updatedAt.UTC(),
	}, nil
}

// ClearSpendCeiling clears identity.workspace.spend_ceiling_micro_usd on the owner pool.
func (s ProvisionStore) ClearSpendCeiling(ctx context.Context, keyID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var prev *int64
	if err := tx.QueryRow(ctx, `SELECT spend_ceiling_micro_usd FROM identity.workspace WHERE id = true FOR UPDATE`).Scan(&prev); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE identity.workspace
		SET spend_ceiling_micro_usd = NULL, updated_at = $1
		WHERE id = true`, s.now()); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionQuotaClear,
		Outcome:    audit.Success,
		TargetType: "quota",
		TargetID:   "workspace",
		Changes: audit.Changes{
			Fields: []string{"key_id", "spend_ceiling_micro_usd"},
			Values: map[string]audit.Change{
				"key_id":                  {To: keyID},
				"spend_ceiling_micro_usd": {From: prev, To: nil},
			},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SpendCeiling reads the current workspace spend ceiling (nil if unset).
func (s ProvisionStore) SpendCeiling(ctx context.Context) (*int64, error) {
	var v *int64
	err := s.Pool.QueryRow(ctx, `SELECT spend_ceiling_micro_usd FROM identity.workspace WHERE id = true`).Scan(&v)
	return v, err
}

func revokeMemberAccessTx(ctx context.Context, tx pgx.Tx, userID string) (sessionsEnded, tokensRevoked bool, err error) {
	tag, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return false, false, err
	}
	sessionsEnded = tag.RowsAffected() > 0

	tag, err = tx.Exec(ctx, `DELETE FROM identity.api_tokens WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return false, false, err
	}
	tokensRevoked = tag.RowsAffected() > 0

	for _, q := range []string{
		`DELETE FROM identity.passkeys WHERE user_id = $1::uuid`,
		`DELETE FROM identity.team_members WHERE user_id = $1::uuid`,
		`DELETE FROM identity.shares WHERE user_id = $1::uuid`,
		`DELETE FROM identity.webauthn_challenges WHERE user_id = $1::uuid`,
		`DELETE FROM identity.sign_in_links WHERE user_id = $1::uuid AND used_at IS NULL`,
	} {
		if _, err := tx.Exec(ctx, q, userID); err != nil {
			return false, false, err
		}
	}
	return sessionsEnded, tokensRevoked, nil
}

func resolveTeamsByExternalID(ctx context.Context, tx pgx.Tx, externalIDs []string) ([]string, []string, error) {
	clean, err := dedupeExternalIDs(externalIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(clean) == 0 {
		return []string{}, []string{}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT external_id, id::text
		FROM identity.teams
		WHERE external_id = ANY($1::text[])`, clean)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byExt := make(map[string]string, len(clean))
	for rows.Next() {
		var ext, id string
		if err := rows.Scan(&ext, &id); err != nil {
			return nil, nil, err
		}
		byExt[ext] = id
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(clean))
	for _, ext := range clean {
		id, ok := byExt[ext]
		if !ok {
			return nil, nil, fmt.Errorf("%w: %s", domain.ErrUnknownTeamExternalID, ext)
		}
		ids = append(ids, id)
	}
	return ids, clean, nil
}

func resolveMembersByExternalID(ctx context.Context, tx pgx.Tx, externalIDs []string) ([]string, []string, error) {
	clean, err := dedupeExternalIDs(externalIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(clean) == 0 {
		return []string{}, []string{}, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT external_id, id::text
		FROM identity.users
		WHERE external_id = ANY($1::text[])
		  AND disabled_at IS NULL
		  AND joined_at IS NOT NULL`, clean)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byExt := make(map[string]string, len(clean))
	for rows.Next() {
		var ext, id string
		if err := rows.Scan(&ext, &id); err != nil {
			return nil, nil, err
		}
		byExt[ext] = id
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(clean))
	for _, ext := range clean {
		id, ok := byExt[ext]
		if !ok {
			return nil, nil, fmt.Errorf("%w: %s", domain.ErrUnknownMemberExternalID, ext)
		}
		ids = append(ids, id)
	}
	return ids, clean, nil
}

func dedupeExternalIDs(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		clean, err := domain.CleanExternalID(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, clean) {
			out = append(out, clean)
		}
	}
	return out, nil
}

func memberTeamExternalIDs(ctx context.Context, tx pgx.Tx, userID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(t.external_id, '')
		FROM identity.team_members AS tm
		JOIN identity.teams AS t ON t.id = tm.team_id
		WHERE tm.user_id = $1::uuid AND t.external_id IS NOT NULL
		ORDER BY t.external_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ext string
		if err := rows.Scan(&ext); err != nil {
			return nil, err
		}
		out = append(out, ext)
	}
	return out, rows.Err()
}

func teamMemberExternalIDs(ctx context.Context, tx pgx.Tx, teamID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT u.external_id
		FROM identity.team_members AS tm
		JOIN identity.users AS u ON u.id = tm.user_id
		WHERE tm.team_id = $1::uuid AND u.external_id IS NOT NULL
		ORDER BY u.external_id`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var ext string
		if err := rows.Scan(&ext); err != nil {
			return nil, err
		}
		out = append(out, ext)
	}
	return out, rows.Err()
}

func mapProvisionUserErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "users_external_id":
			return domain.ErrExternalIDConflict
		case "users_email_hash":
			return domain.ErrEmailConflict
		}
	}
	return mapLastOwner(err)
}

func mapProvisionTeamErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.ConstraintName {
		case "teams_external_id":
			return domain.ErrExternalIDConflict
		case "teams_name_ci":
			return domain.ErrTeamNameTaken
		}
	}
	return err
}
