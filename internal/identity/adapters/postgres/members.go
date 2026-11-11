package postgres

import (
	"context"
	"errors"
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
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.MemberInvite,
		Outcome:  audit.Success,
		TargetID: userID.String(),
	}); err != nil {
		return domain.Member{}, err
	}
	return member, tx.Commit(ctx)
}

// RemoveMember disables a person and clears their access, inside one
// transaction with the member.disable event. The disable trigger ends the
// person's sessions, which the event reports as sessions_ended.
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
		return audit.Record(ctx, tx, audit.Event{
			Action:   audit.MemberDisable,
			Outcome:  audit.Success,
			TargetID: id.String(),
			Changes: audit.Changes{
				Values: map[string]audit.Change{"sessions_ended": {To: true}},
			},
		})
	})
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

func clearMemberAccess(ctx context.Context, tx pgx.Tx, id domain.UserID) error {
	statements := []string{
		`DELETE FROM identity.passkeys WHERE user_id = $1`,
		`DELETE FROM identity.sessions WHERE user_id = $1`,
		`DELETE FROM identity.team_members WHERE user_id = $1`,
		`DELETE FROM identity.shares WHERE user_id = $1`,
		`DELETE FROM identity.webauthn_challenges WHERE user_id = $1`,
		`DELETE FROM identity.sign_in_links WHERE user_id = $1 AND used_at IS NULL`,
		`DELETE FROM identity.api_tokens WHERE user_id = $1`,
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
