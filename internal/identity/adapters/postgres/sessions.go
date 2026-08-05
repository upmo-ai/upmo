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
)

// Sessions is the identity.sessions table.
type Sessions struct {
	Pool *pgxpool.Pool
}

func (s Sessions) Insert(ctx context.Context, user domain.UserID, hash []byte, created, expires time.Time) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $3, $4)
		RETURNING id::text`, user, hash, created, expires).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s Sessions) Find(ctx context.Context, hash []byte) (app.Row, bool, error) {
	var row app.Row
	err := s.Pool.QueryRow(ctx, `
		SELECT session.id::text, session.user_id, session.created_at, session.last_seen_at, session.expires_at
		FROM identity.sessions AS session
		JOIN identity.users AS person ON person.id = session.user_id
		WHERE session.token_hash = $1
		  AND person.disabled_at IS NULL`, hash).Scan(&row.ID, &row.UserID, &row.Created, &row.LastSeen, &row.Expires)
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

func (s Sessions) DeleteUser(ctx context.Context, user domain.UserID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1`, user)
	return err
}

func (s Sessions) DeleteEnded(ctx context.Context, user domain.UserID, now time.Time, idle time.Duration) error {
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM identity.sessions
		WHERE user_id = $1
		  AND (expires_at <= $2 OR last_seen_at <= $3)`, user, now, now.Add(-idle))
	return err
}
