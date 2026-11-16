package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
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

// CreateRole inserts a custom role and records the role.create event in the
// same transaction. Role duplication uses it too.
func (p Passkeys) CreateRole(ctx context.Context, name string, permissions []string) (domain.RoleID, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return domain.RoleID{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var idText string
	err = tx.QueryRow(ctx, `
		INSERT INTO identity.roles (name, permissions)
		VALUES ($1, $2)
		RETURNING id::text`, name, permissions).Scan(&idText)
	if err != nil {
		return domain.RoleID{}, mapRoleWrite(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.RoleCreate,
		Outcome:  audit.Success,
		TargetID: idText,
	}); err != nil {
		return domain.RoleID{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RoleID{}, err
	}
	return domain.ParseRoleID(idText)
}

// ChangeRole updates a custom role's name and permissions and records the
// role.update event, with the permissions from and to, in the same transaction.
func (p Passkeys) ChangeRole(ctx context.Context, id domain.RoleID, name string, permissions []string) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var system *string
	var before []string
	err = tx.QueryRow(ctx, `SELECT key, permissions FROM identity.roles WHERE id = $1 FOR NO KEY UPDATE`, id).Scan(&system, &before)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNoSuchRole
	}
	if err != nil {
		return err
	}
	if system != nil {
		return domain.ErrSystemRole
	}
	tag, err := tx.Exec(ctx, `
		UPDATE identity.roles SET name = $2, permissions = $3
		WHERE id = $1 AND key IS NULL`, id, name, permissions)
	if err != nil {
		return mapRoleWrite(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrNoSuchRole
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.RoleUpdate,
		Outcome:  audit.Success,
		TargetID: id.String(),
		Changes: audit.Changes{
			Values: map[string]audit.Change{
				"permissions": {From: before, To: permissions},
			},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.RoleDelete,
		Outcome:  audit.Success,
		TargetID: id.String(),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetMemberRole gives a person another role through the proof-carrying definer
// function. The role.change event records the role ids from and to in the same
// transaction.
func (p Passkeys) SetMemberRole(ctx context.Context, actorTokenHash []byte, user domain.UserID, role domain.RoleID, _ func(current, next string, permissions []domain.Permission) error) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var from string
	err = tx.QueryRow(ctx, `
		SELECT old_role_id::text
		FROM identity.member_set_role($1, $2, $3)`, actorTokenHash, user, role).Scan(&from)
	if err != nil {
		return mapIdentityDefinerError(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.MemberRoleChange,
		Outcome:  audit.Success,
		TargetID: user.String(),
		Changes: audit.Changes{
			Values: map[string]audit.Change{
				"role_id": {From: from, To: role.String()},
			},
		},
	}); err != nil {
		return err
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
