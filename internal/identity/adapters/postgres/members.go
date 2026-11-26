package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// ListRoles returns every role, system key or empty.
func (p Passkeys) ListRoles(ctx context.Context) ([]domain.MemberRole, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT role.id::text, role.name, COALESCE(role.key, ''), role.permissions,
		       (SELECT count(*) FROM identity.users AS person
		        WHERE person.role_id = role.id AND person.disabled_at IS NULL)
		FROM identity.roles AS role
		ORDER BY role.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MemberRole
	for rows.Next() {
		var id, name, system string
		var raw []string
		var members int
		if err := rows.Scan(&id, &name, &system, &raw, &members); err != nil {
			return nil, err
		}
		roleID, err := domain.ParseRoleID(id)
		if err != nil {
			return nil, err
		}
		permissions, err := rolePermissions(system, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.MemberRole{
			ID: roleID, Name: name, System: system, Permissions: permissions, Members: members,
		})
	}
	return out, rows.Err()
}

// ListMembers returns enabled people with their teams, ordered by address.
// The addresses come from the worker's directory for the session tokenHash;
// a person missing from it is an error, never an empty address.
func (p Passkeys) ListMembers(ctx context.Context, tokenHash []byte) ([]domain.Member, error) {
	emails, err := readyEmails(p.Emails)
	if err != nil {
		return nil, err
	}
	directory, err := emails.EmailDirectory(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	rows, err := p.Pool.Query(ctx, `
		SELECT person.id::text, person.name,
		       role.id::text, role.name, COALESCE(role.key, ''),
		       person.joined_at IS NULL, COALESCE(team.name, ''),
		       COALESCE(person.avatar_key, '')
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		LEFT JOIN identity.team_members AS membership ON membership.user_id = person.id
		LEFT JOIN identity.teams AS team ON team.id = membership.team_id
		WHERE person.disabled_at IS NULL
		ORDER BY person.id, team.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Member
	for rows.Next() {
		var id, name, roleID, roleName, system, team, avatarKey string
		var invited bool
		if err := rows.Scan(&id, &name, &roleID, &roleName, &system, &invited, &team, &avatarKey); err != nil {
			return nil, err
		}
		userID, err := domain.ParseUserID(id)
		if err != nil {
			return nil, err
		}
		parsedRole, err := domain.ParseRoleID(roleID)
		if err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == userID {
			if team != "" {
				out[n-1].Teams = append(out[n-1].Teams, team)
			}
			continue
		}
		email, ok := directory[userID]
		if !ok {
			return nil, errNoAddress
		}
		member := domain.Member{
			ID: userID, Email: email, Name: name,
			RoleID: parsedRole, RoleName: roleName, System: system, Invited: invited,
			AvatarURL: domain.AvatarURL(userID, avatarKey),
		}
		if team != "" {
			member.Teams = []string{team}
		}
		out = append(out, member)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The address is sealed, so the database cannot order by it.
	sort.SliceStable(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].Email), strings.ToLower(out[j].Email)
		if left != right {
			return left < right
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out, nil
}

// errNoAddress is a listed person the worker's directory did not name.
var errNoAddress = errors.New("identity: a member's address is missing")

// MemberActor loads an enabled person for an access check.
func (p Passkeys) MemberActor(ctx context.Context, id domain.UserID) (domain.RoleID, string, bool, []string, error) {
	var roleText, system string
	var joined bool
	var raw []string
	err := p.Pool.QueryRow(ctx, `
		SELECT role.id::text, COALESCE(role.key, ''), person.joined_at IS NOT NULL, role.permissions
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1 AND person.disabled_at IS NULL`, id).Scan(&roleText, &system, &joined, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RoleID{}, "", false, nil, domain.ErrNoSuchMember
	}
	if err != nil {
		return domain.RoleID{}, "", false, nil, err
	}
	roleID, err := domain.ParseRoleID(roleText)
	if err != nil {
		return domain.RoleID{}, "", false, nil, err
	}
	return roleID, system, joined, raw, nil
}

// MemberTeams returns the ids of the teams a person is on.
func (p Passkeys) MemberTeams(ctx context.Context, id domain.UserID) ([]string, error) {
	rows, err := p.Pool.Query(ctx, `SELECT team_id::text FROM identity.team_members WHERE user_id = $1`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SharedObjects returns the objects of one type shared with a person, their
// teams, or their role, with the share level.
func (p Passkeys) SharedObjects(ctx context.Context, objectType domain.ObjectType, user domain.UserID, teams []string, role domain.RoleID) ([]app.ObjectShare, error) {
	if teams == nil {
		teams = []string{}
	}
	rows, err := p.Pool.Query(ctx, domain.SharedObjectsSQL(), string(objectType), user.String(), teams, role.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.ObjectShare
	for rows.Next() {
		var id, level string
		if err := rows.Scan(&id, &level); err != nil {
			return nil, err
		}
		out = append(out, app.ObjectShare{ObjectID: id, Level: domain.Level(level)})
	}
	return out, rows.Err()
}

func rolePermissions(system string, raw []string) ([]domain.Permission, error) {
	if system != "" {
		return domain.SystemPermissions(domain.Role(system))
	}
	var out []domain.Permission
	for _, name := range raw {
		permission, err := domain.ParsePermission(name)
		if err != nil {
			continue
		}
		out = append(out, permission)
	}
	return out, nil
}

// RoleSystem returns the system key for a role, or empty for a custom role.
func (p Passkeys) RoleSystem(ctx context.Context, id domain.RoleID) (string, error) {
	var system string
	err := p.Pool.QueryRow(ctx, `SELECT COALESCE(key, '') FROM identity.roles WHERE id = $1`, id).Scan(&system)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrMemberInput
	}
	return system, err
}

// InviteMember inserts a person, or brings a disabled row back, and stores one invite link.
func (p Passkeys) InviteMember(ctx context.Context, actorTokenHash []byte, email, name string, role domain.RoleID, handle, hash []byte, now, expires time.Time) (domain.Member, error) {
	stored, hashes, keyID, err := sealEmail(ctx, p.Emails, email)
	if err != nil {
		return domain.Member{}, err
	}
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return domain.Member{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldAvatarKey string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(avatar_key, '')
		FROM identity.users
		WHERE email_hash = ANY($1) AND disabled_at IS NOT NULL`, hashes).Scan(&oldAvatarKey); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.Member{}, err
	}
	var idText string
	err = tx.QueryRow(ctx, `
		SELECT identity.member_invite($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)::text`,
		actorTokenHash, stored, hashes, keyID, name, role, handle, hash, now, expires).Scan(&idText)
	if err != nil {
		return domain.Member{}, mapIdentityDefinerError(err)
	}
	userID, err := domain.ParseUserID(idText)
	if err != nil {
		return domain.Member{}, err
	}
	member, err := p.memberByID(ctx, tx, userID)
	if err != nil {
		return domain.Member{}, err
	}
	// The invite holds the address it sealed: brain reads no stored one.
	member.Email = strings.TrimSpace(email)
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.MemberInvite,
		Outcome:  audit.Success,
		TargetID: userID.String(),
	}); err != nil {
		return domain.Member{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Member{}, err
	}
	if oldAvatarKey != "" && p.Blobs != nil {
		_ = p.Blobs.Remove(ctx, oldAvatarKey)
	}
	return member, nil
}

// RemoveMember disables a person and clears their access, inside one
// transaction with the member.disable event. The disable trigger ends the
// person's sessions, which the event reports as sessions_ended.
func (p Passkeys) RemoveMember(ctx context.Context, actorTokenHash []byte, id domain.UserID, now time.Time, _ func(system string) error) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT identity.member_disable($1, $2, $3)`, actorTokenHash, id, now); err != nil {
		return mapIdentityDefinerError(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.MemberDisable,
		Outcome:  audit.Success,
		TargetID: id.String(),
		Changes: audit.Changes{
			Values: map[string]audit.Change{"sessions_ended": {To: true}},
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapLastOwner(err)
	}
	return nil
}

// ReplaceMemberLink voids unused links and stores one new link, with the
// invite or reset event in the same transaction.
func (p Passkeys) ReplaceMemberLink(ctx context.Context, id domain.UserID, purpose string, hash []byte, expires time.Time, rule func(system string, invited bool) error) error {
	action := audit.MemberInviteLink
	if purpose != "invite" {
		action = audit.MemberResetLink
	}
	return p.changeMember(ctx, id, rule, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			DELETE FROM identity.sign_in_links
			WHERE user_id = $1 AND used_at IS NULL`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
			VALUES ($1, $2, $3, $4)`, hash, id, purpose, expires); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Action:   action,
			Outcome:  audit.Success,
			TargetID: id.String(),
		})
	})
}

func (p Passkeys) changeMember(ctx context.Context, id domain.UserID, rule func(system string, invited bool) error, mutate func(pgx.Tx) error) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var system string
	var invited bool
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(role.key, ''), person.joined_at IS NULL
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1 AND person.disabled_at IS NULL
		FOR NO KEY UPDATE OF person`, id).Scan(&system, &invited)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchMember
	}
	if err != nil {
		return err
	}
	if rule != nil {
		if err := rule(system, invited); err != nil {
			return err
		}
	}
	if err := mutate(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapLastOwner(err)
	}
	return nil
}

func mapIdentityDefinerError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Message {
		case "identity: someone already exists":
			return app.ErrExists
		case "identity: invalid actor session":
			return app.ErrNotSignedIn
		case "identity: you cannot do this to yourself":
			return domain.ErrSelf
		case "identity: no such member":
			return domain.ErrNoSuchMember
		case "identity: no such role":
			return domain.ErrNoSuchRole
		case "identity: only owners and admins manage members":
			return domain.ErrNotPermitted
		case "identity: only an owner can change the owner role":
			return domain.ErrOwnersOnly
		case "identity: you cannot grant a permission you do not hold":
			return domain.ErrBeyondOwn
		case "identity: already a member":
			return domain.ErrAlreadyMember
		case "identity: a person needs an email address, a name and a role":
			return domain.ErrMemberInput
		case "identity: a token needs a name":
			return domain.ErrTokenName
		case "identity: a token cannot hold that permission":
			return domain.ErrTokenScope
		}
		if pgErr.ConstraintName == "api_tokens_expiry_check" {
			return domain.ErrTokenDays
		}
		// A concurrent invite of the same address loses the race on the index.
		if pgErr.ConstraintName == "users_email_hash" {
			return domain.ErrAlreadyMember
		}
		if pgErr.Code == "23503" || pgErr.Code == "23514" {
			return domain.ErrMemberInput
		}
	}
	return mapLastOwner(err)
}

func (p Passkeys) memberByID(ctx context.Context, tx pgx.Tx, id domain.UserID) (domain.Member, error) {
	var member domain.Member
	var idText, roleText, avatarKey string
	err := tx.QueryRow(ctx, `
		SELECT person.id::text, person.name,
		       role.id::text, role.name, COALESCE(role.key, ''),
		       person.joined_at IS NULL, COALESCE(person.avatar_key, '')
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1`, id).Scan(
		&idText, &member.Name, &roleText, &member.RoleName, &member.System, &member.Invited, &avatarKey)
	if err != nil {
		return domain.Member{}, err
	}
	member.ID, err = domain.ParseUserID(idText)
	if err != nil {
		return domain.Member{}, err
	}
	member.RoleID, err = domain.ParseRoleID(roleText)
	if err != nil {
		return domain.Member{}, err
	}
	member.AvatarURL = domain.AvatarURL(member.ID, avatarKey)
	return member, nil
}

// SetAvatar updates the caller's avatar_key via identity.avatar_set and records
// the avatar.set audit event in the same transaction, returning the previous key.
func (p Passkeys) SetAvatar(ctx context.Context, actorTokenHash []byte, user domain.UserID, avatarKey string) (string, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldKey string
	if err := tx.QueryRow(ctx, `SELECT identity.avatar_set($1, $2)`, actorTokenHash, avatarKey).Scan(&oldKey); err != nil {
		return "", mapIdentityDefinerError(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.AvatarSet,
		Outcome:  audit.Success,
		TargetID: user.String(),
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return oldKey, nil
}

// ClearAvatar clears the caller's avatar_key to NULL via identity.avatar_set
// and records the avatar.remove audit event in the same transaction.
func (p Passkeys) ClearAvatar(ctx context.Context, actorTokenHash []byte, user domain.UserID) (string, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var oldKey string
	if err := tx.QueryRow(ctx, `SELECT identity.avatar_set($1, NULL)`, actorTokenHash).Scan(&oldKey); err != nil {
		return "", mapIdentityDefinerError(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.AvatarRemove,
		Outcome:  audit.Success,
		TargetID: user.String(),
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return oldKey, nil
}

// LookupAvatarKey returns the current avatar_key for an active user.
func (p Passkeys) LookupAvatarKey(ctx context.Context, user domain.UserID) (string, error) {
	var key string
	err := p.Pool.QueryRow(ctx, `
		SELECT COALESCE(avatar_key, '')
		FROM identity.users
		WHERE id = $1 AND disabled_at IS NULL`, user).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && key == "") {
		return "", domain.ErrAvatarNotFound
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func mapLastOwner(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.Contains(pgErr.Message, "install has no joined owner") {
		return domain.ErrLastOwner
	}
	if strings.Contains(err.Error(), "install has no joined owner") {
		return domain.ErrLastOwner
	}
	return err
}
