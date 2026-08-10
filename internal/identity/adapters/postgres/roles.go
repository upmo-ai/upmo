package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// RoleByID returns one role, with a system role's permissions filled from code.
func (p Passkeys) RoleByID(ctx context.Context, id domain.RoleID) (domain.MemberRole, error) {
	var role domain.MemberRole
	var idText, system string
	var raw []string
	err := p.Pool.QueryRow(ctx, `
		SELECT role.id::text, role.name, COALESCE(role.key, ''), role.permissions,
		       (SELECT count(*) FROM identity.users AS person
		        WHERE person.role_id = role.id AND person.disabled_at IS NULL)
		FROM identity.roles AS role
		WHERE role.id = $1`, id).Scan(&idText, &role.Name, &system, &raw, &role.Members)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.MemberRole{}, domain.ErrNoSuchRole
	}
	if err != nil {
		return domain.MemberRole{}, err
	}
	role.ID, err = domain.ParseRoleID(idText)
	if err != nil {
		return domain.MemberRole{}, err
	}
	role.System = system
	role.Permissions, err = rolePermissions(system, raw)
	return role, err
}

// CreateRole inserts a custom role.
func (p Passkeys) CreateRole(ctx context.Context, name string, permissions []string) (domain.RoleID, error) {
	var idText string
	err := p.Pool.QueryRow(ctx, `
		INSERT INTO identity.roles (name, permissions)
		VALUES ($1, $2)
		RETURNING id::text`, name, permissions).Scan(&idText)
	if err != nil {
		return domain.RoleID{}, mapRoleWrite(err)
	}
	return domain.ParseRoleID(idText)
}

// ChangeRole updates a custom role's name and permissions.
func (p Passkeys) ChangeRole(ctx context.Context, id domain.RoleID, name string, permissions []string) error {
	var system *string
	err := p.Pool.QueryRow(ctx, `SELECT key FROM identity.roles WHERE id = $1`, id).Scan(&system)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchRole
	}
	if err != nil {
		return err
	}
	if system != nil {
		return domain.ErrSystemRole
	}
	tag, err := p.Pool.Exec(ctx, `
		UPDATE identity.roles SET name = $2, permissions = $3
		WHERE id = $1 AND key IS NULL`, id, name, permissions)
	if err != nil {
		return mapRoleWrite(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrNoSuchRole
	}
	return nil
}

// DeleteRole removes a custom role that nobody holds.
func (p Passkeys) DeleteRole(ctx context.Context, id domain.RoleID) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var system *string
	err = tx.QueryRow(ctx, `SELECT key FROM identity.roles WHERE id = $1 FOR UPDATE`, id).Scan(&system)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchRole
	}
	if err != nil {
		return err
	}
	if system != nil {
		return domain.ErrSystemRole
	}
	var holders int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE role_id = $1`, id).Scan(&holders); err != nil {
		return err
	}
	if holders > 0 {
		return &domain.RoleHolders{Count: holders}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity.roles WHERE id = $1 AND key IS NULL`, id); err != nil {
		return mapRoleWrite(err)
	}
	return tx.Commit(ctx)
}

// SetMemberRole gives a person another role. The rule sees the locked row.
func (p Passkeys) SetMemberRole(ctx context.Context, user domain.UserID, role domain.RoleID, rule func(current, next string, permissions []domain.Permission) error) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(role.key, '')
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1 AND person.disabled_at IS NULL
		FOR NO KEY UPDATE OF person`, user).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchMember
	}
	if err != nil {
		return err
	}
	var next string
	var raw []string
	err = tx.QueryRow(ctx, `SELECT COALESCE(key, ''), permissions FROM identity.roles WHERE id = $1`, role).Scan(&next, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchRole
	}
	if err != nil {
		return err
	}
	permissions, err := rolePermissions(next, raw)
	if err != nil {
		return err
	}
	if rule != nil {
		if err := rule(current, next, permissions); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = $2 WHERE id = $1`, user, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrNoSuchMember
	}
	if err := tx.Commit(ctx); err != nil {
		return mapLastOwner(err)
	}
	return nil
}

func mapRoleWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505":
			return domain.ErrRoleNameTaken
		case pgErr.Code == "23514":
			return domain.ErrBadRole
		case pgErr.Message == "a system role cannot be changed":
			return domain.ErrSystemRole
		}
	}
	return mapLastOwner(err)
}
