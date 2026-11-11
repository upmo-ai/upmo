// Package postgres stores identity in its own schema.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// Sessions is the identity.sessions table.
type Sessions struct {
	Pool *pgxpool.Pool
}

func (s Sessions) Insert(ctx context.Context, user domain.UserID, hash []byte, created, expires time.Time, method, agent string) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method, signed_in_at, user_agent)
		VALUES ($1, $2, $3, $3, $4, $5, $3, $6)
		RETURNING id::text`, user, hash, created, expires, method, agent).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s Sessions) Find(ctx context.Context, hash []byte) (app.Row, bool, error) {
	var row app.Row
	err := s.Pool.QueryRow(ctx, `
		SELECT session.id::text, session.user_id, session.created_at, session.last_seen_at, session.expires_at,
		       session.sign_in_method, session.signed_in_at, person.role_id::text
		FROM identity.sessions AS session
		JOIN identity.users AS person ON person.id = session.user_id
		WHERE session.token_hash = $1
		  AND person.disabled_at IS NULL`, hash).Scan(&row.ID, &row.UserID, &row.Created, &row.LastSeen, &row.Expires, &row.Method, &row.SignedIn, &row.RoleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Row{}, false, nil
	}
	if err != nil {
		return app.Row{}, false, err
	}
	return row, true, nil
}

func (s Sessions) Touch(ctx context.Context, id string, seen time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE identity.sessions SET last_seen_at = $2 WHERE id = $1`, id, seen)
	return err
}

func (s Sessions) Delete(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM identity.sessions WHERE id = $1`, id)
	return err
}

// EndAudited ends one session and records its sign-out event in the same
// transaction, so the session cannot end without its audit row.
func (s Sessions) EndAudited(ctx context.Context, id string, event audit.Event) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := audit.Record(ctx, tx, event); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Sessions) DeleteUser(ctx context.Context, user domain.UserID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1`, user)
	return err
}

func (s Sessions) DeleteOthers(ctx context.Context, user domain.UserID, keep string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1 AND id <> $2`, user, keep)
	return err
}

func (s Sessions) Refresh(ctx context.Context, id, method string, at time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE identity.sessions SET sign_in_method = $2, signed_in_at = $3 WHERE id = $1`, id, method, at)
	return err
}

func (s Sessions) List(ctx context.Context, user domain.UserID) ([]app.Device, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, sign_in_method, signed_in_at, last_seen_at, user_agent
		FROM identity.sessions WHERE user_id = $1 ORDER BY signed_in_at DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.Device
	for rows.Next() {
		var device app.Device
		if err := rows.Scan(&device.ID, &device.Method, &device.SignedIn, &device.LastSeen, &device.Agent); err != nil {
			return nil, err
		}
		out = append(out, device)
	}
	return out, rows.Err()
}

func (s Sessions) DeleteEnded(ctx context.Context, user domain.UserID, now time.Time, idle time.Duration) error {
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM identity.sessions
		WHERE user_id = $1
		  AND (expires_at <= $2 OR last_seen_at <= $3)`, user, now, now.Add(-idle))
	return err
}
