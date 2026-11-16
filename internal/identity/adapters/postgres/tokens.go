package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// SaveToken stores a new token through the proof-carrying definer function and
// records the token.create event, with the scope and the expiry as its safe
// values, in the same transaction. The secret is never recorded.
func (p Passkeys) SaveToken(ctx context.Context, actorTokenHash []byte, _ domain.UserID, name string, hash []byte, scope []string, created, expires time.Time) (string, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, `
		SELECT identity.token_create($1, $2, $3, $4, $5, $6)::text`,
		actorTokenHash, name, hash, scope, created, expires).Scan(&id); err != nil {
		return "", mapIdentityDefinerError(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.TokenCreate,
		Outcome:  audit.Success,
		TargetID: id,
		Changes: audit.Changes{
			Values: map[string]audit.Change{
				"scope":      {To: scope},
				"expires_at": {To: expires.UTC().Format(time.RFC3339)},
			},
		},
	}); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// ListTokens returns the person's tokens, newest first.
func (p Passkeys) ListTokens(ctx context.Context, user domain.UserID) ([]app.TokenRecord, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT id::text, name, scope, created_at, expires_at, last_used_at
		FROM identity.api_tokens
		WHERE user_id = $1
		ORDER BY created_at DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.TokenRecord
	for rows.Next() {
		var row app.TokenRecord
		if err := rows.Scan(&row.ID, &row.Name, &row.Scope, &row.Created, &row.Expires, &row.LastUsed); err != nil {
			return nil, err
		}
		if row.Scope == nil {
			row.Scope = []string{}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteToken removes one of the person's tokens and records the token.revoke
// event in the same transaction.
func (p Passkeys) DeleteToken(ctx context.Context, user domain.UserID, id string) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM identity.api_tokens WHERE user_id = $1 AND id = $2`, user, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrToken
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.TokenRevoke,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// HeldFor returns the permissions the person's role holds now.
func (p Passkeys) HeldFor(ctx context.Context, user domain.UserID) (app.Held, error) {
	var held app.Held
	var idText, system string
	var raw []string
	err := p.Pool.QueryRow(ctx, `
		SELECT person.id::text, person.disabled_at IS NOT NULL, person.joined_at IS NOT NULL,
		       person.role_id::text, COALESCE(role.key, ''), role.permissions
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1`, user).Scan(&idText, &held.Disabled, &held.Joined, &held.RoleID, &system, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Held{}, domain.ErrToken
	}
	if err != nil {
		return app.Held{}, err
	}
	held.UserID, err = domain.ParseUserID(idText)
	if err != nil {
		return app.Held{}, err
	}
	held.Permissions, err = rolePermissions(system, raw)
	return held, err
}

// FindToken loads a token by its hash. An expired token is deleted.
func (p Passkeys) FindToken(ctx context.Context, hash []byte, now time.Time) (app.TokenRecord, app.Held, error) {
	var row app.TokenRecord
	var held app.Held
	var idText, system string
	var raw []string
	err := p.Pool.QueryRow(ctx, `
		SELECT id::text, name, scope, created_at, expires_at, last_used_at,
		       person_id::text, disabled, joined, role_id::text, role_key, permissions
		FROM identity.token_find($1)`, hash).Scan(
		&row.ID, &row.Name, &row.Scope, &row.Created, &row.Expires, &row.LastUsed,
		&idText, &held.Disabled, &held.Joined, &held.RoleID, &system, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.TokenRecord{}, app.Held{}, domain.ErrToken
	}
	if err != nil {
		return app.TokenRecord{}, app.Held{}, err
	}
	if domain.TokenExpired(row.Expires, now) {
		_, _ = p.Pool.Exec(ctx, `DELETE FROM identity.api_tokens WHERE id = $1`, row.ID)
		return app.TokenRecord{}, app.Held{}, domain.ErrToken
	}
	if held.Disabled || !held.Joined {
		return app.TokenRecord{}, app.Held{}, domain.ErrToken
	}
	held.UserID, err = domain.ParseUserID(idText)
	if err != nil {
		return app.TokenRecord{}, app.Held{}, err
	}
	held.Permissions, err = rolePermissions(system, raw)
	if row.Scope == nil {
		row.Scope = []string{}
	}
	return row, held, err
}

// TouchToken writes last use when it is due.
func (p Passkeys) TouchToken(ctx context.Context, id string, at time.Time) error {
	_, err := p.Pool.Exec(ctx, `UPDATE identity.api_tokens SET last_used_at = $2 WHERE id = $1`, id, at)
	return err
}
