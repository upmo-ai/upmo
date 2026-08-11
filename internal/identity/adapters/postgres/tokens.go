package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// SaveToken stores a new token and returns its id.
func (p Passkeys) SaveToken(ctx context.Context, user domain.UserID, name string, hash []byte, scope []string, created, expires time.Time) (string, error) {
	var id string
	err := p.Pool.QueryRow(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text`, user, name, hash, scope, created, expires).Scan(&id)
	return id, err
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

// DeleteToken removes one of the person's tokens.
func (p Passkeys) DeleteToken(ctx context.Context, user domain.UserID, id string) error {
	tag, err := p.Pool.Exec(ctx, `DELETE FROM identity.api_tokens WHERE user_id = $1 AND id = $2`, user, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrToken
	}
	return nil
}

// HeldFor returns the permissions the person's role holds now.
func (p Passkeys) HeldFor(ctx context.Context, user domain.UserID) (app.Held, error) {
	var held app.Held
	var idText, system string
	var raw []string
	err := p.Pool.QueryRow(ctx, `
		SELECT person.id::text, person.disabled_at IS NOT NULL, person.joined_at IS NOT NULL,
		       COALESCE(role.key, ''), role.permissions
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE person.id = $1`, user).Scan(&idText, &held.Disabled, &held.Joined, &system, &raw)
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
		SELECT token.id::text, token.name, token.scope, token.created_at, token.expires_at, token.last_used_at,
		       person.id::text, person.disabled_at IS NOT NULL, person.joined_at IS NOT NULL,
		       COALESCE(role.key, ''), role.permissions
		FROM identity.api_tokens AS token
		JOIN identity.users AS person ON person.id = token.user_id
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE token.token_hash = $1`, hash).Scan(
		&row.ID, &row.Name, &row.Scope, &row.Created, &row.Expires, &row.LastUsed,
		&idText, &held.Disabled, &held.Joined, &system, &raw)
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
