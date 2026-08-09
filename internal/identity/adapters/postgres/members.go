package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// ListRoles returns every role, system key or empty.
func (p Passkeys) ListRoles(ctx context.Context) ([]domain.MemberRole, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT id::text, name, COALESCE(key, '')
		FROM identity.roles
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MemberRole
	for rows.Next() {
		var id, name, system string
		if err := rows.Scan(&id, &name, &system); err != nil {
			return nil, err
		}
		roleID, err := domain.ParseRoleID(id)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.MemberRole{ID: roleID, Name: name, System: system})
	}
	return out, rows.Err()
}

// ListMembers returns enabled people with their teams.
func (p Passkeys) ListMembers(ctx context.Context) ([]domain.Member, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT person.id::text, person.email, person.name,
		       role.id::text, role.name, COALESCE(role.key, ''),
		       person.joined_at IS NULL, COALESCE(team.name, '')
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		LEFT JOIN identity.team_members AS membership ON membership.user_id = person.id
		LEFT JOIN identity.teams AS team ON team.id = membership.team_id
		WHERE person.disabled_at IS NULL
		ORDER BY person.email, team.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Member
	for rows.Next() {
		var id, email, name, roleID, roleName, system, team string
		var invited bool
		if err := rows.Scan(&id, &email, &name, &roleID, &roleName, &system, &invited, &team); err != nil {
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
		member := domain.Member{
			ID: userID, Email: email, Name: name,
			RoleID: parsedRole, RoleName: roleName, System: system, Invited: invited,
		}
		if team != "" {
			member.Teams = []string{team}
		}
		out = append(out, member)
	}
	return out, rows.Err()
}

// MemberActor loads an enabled person for an access check.
func (p Passkeys) MemberActor(ctx context.Context, id domain.UserID) (domain.RoleID, string, bool, error) {
	var roleText, system string
	var joined bool
	err := p.Pool.QueryRow(ctx, `
		SELECT role.id::text, COALESCE(role.key, ''), person.joined_at IS NOT NULL
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1 AND person.disabled_at IS NULL`, id).Scan(&roleText, &system, &joined)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RoleID{}, "", false, domain.ErrNoSuchMember
	}
	if err != nil {
		return domain.RoleID{}, "", false, err
	}
	roleID, err := domain.ParseRoleID(roleText)
	if err != nil {
		return domain.RoleID{}, "", false, err
	}
	return roleID, system, joined, nil
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
func (p Passkeys) InviteMember(ctx context.Context, email, name string, role domain.RoleID, handle, hash []byte, now, expires time.Time) (domain.Member, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return domain.Member{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var idText string
	err = tx.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, webauthn_handle)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (lower(email)) DO UPDATE
		SET name = EXCLUDED.name,
		    role_id = EXCLUDED.role_id,
		    invited_at = EXCLUDED.invited_at,
		    joined_at = NULL,
		    disabled_at = NULL,
		    webauthn_handle = EXCLUDED.webauthn_handle,
		    password_hash = NULL,
		    passkey_offered_at = NULL,
		    external_id = NULL
		WHERE identity.users.disabled_at IS NOT NULL
		RETURNING id::text`, email, name, role, now, handle).Scan(&idText)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Member{}, domain.ErrAlreadyMember
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23514") {
			return domain.Member{}, domain.ErrMemberInput
		}
		return domain.Member{}, err
	}
	userID, err := domain.ParseUserID(idText)
	if err != nil {
		return domain.Member{}, err
	}
	if err := clearMemberAccess(ctx, tx, userID); err != nil {
		return domain.Member{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'invite', $3)`, hash, userID, expires); err != nil {
		return domain.Member{}, err
	}
	member, err := memberByID(ctx, tx, userID)
	if err != nil {
		return domain.Member{}, err
	}
	return member, tx.Commit(ctx)
}

// RemoveMember disables a person and clears their access, inside one transaction.
func (p Passkeys) RemoveMember(ctx context.Context, id domain.UserID, now time.Time, rule func(system string) error) error {
	return p.changeMember(ctx, id, func(system string, _ bool) error {
		return rule(system)
	}, func(tx pgx.Tx) error {
		if err := clearMemberAccess(ctx, tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE identity.users
			SET disabled_at = $2, password_hash = NULL, webauthn_handle = NULL
			WHERE id = $1 AND disabled_at IS NULL`, id, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.ErrNoSuchMember
		}
		return nil
	})
}

// ReplaceMemberLink voids unused links and stores one new link.
func (p Passkeys) ReplaceMemberLink(ctx context.Context, id domain.UserID, purpose string, hash []byte, expires time.Time, rule func(system string, invited bool) error) error {
	return p.changeMember(ctx, id, rule, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			DELETE FROM identity.sign_in_links
			WHERE user_id = $1 AND used_at IS NULL`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
			VALUES ($1, $2, $3, $4)`, hash, id, purpose, expires)
		return err
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

func clearMemberAccess(ctx context.Context, tx pgx.Tx, id domain.UserID) error {
	statements := []string{
		`DELETE FROM identity.passkeys WHERE user_id = $1`,
		`DELETE FROM identity.sessions WHERE user_id = $1`,
		`DELETE FROM identity.team_members WHERE user_id = $1`,
		`DELETE FROM identity.shares WHERE user_id = $1`,
		`DELETE FROM identity.webauthn_challenges WHERE user_id = $1`,
		`DELETE FROM identity.sign_in_links WHERE user_id = $1 AND used_at IS NULL`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement, id); err != nil {
			return err
		}
	}
	return nil
}

func memberByID(ctx context.Context, tx pgx.Tx, id domain.UserID) (domain.Member, error) {
	var member domain.Member
	var idText, roleText string
	err := tx.QueryRow(ctx, `
		SELECT person.id::text, person.email, person.name,
		       role.id::text, role.name, COALESCE(role.key, ''),
		       person.joined_at IS NULL
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1`, id).Scan(
		&idText, &member.Email, &member.Name, &roleText, &member.RoleName, &member.System, &member.Invited)
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
	return member, nil
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
