// Package postgres keeps the runtime context's state in the runtime schema.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/runtime/app"
)

// errNoDatabase reports a Seats with no pool.
var errNoDatabase = errors.New("runtime: no database")

// Seats is the SeatBook in runtime.seats. Each method is one statement, so
// it needs no transaction and no access settings: a seat is not a work object.
type Seats struct {
	Pool *pgxpool.Pool
}

var _ app.SeatBook = Seats{}

// Claim inserts a pending seat that lapses after ttl.
func (s Seats) Claim(ctx context.Context, taskID string, ttl time.Duration) error {
	if s.Pool == nil {
		return errNoDatabase
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO runtime.seats (task_id, expires_at)
		VALUES ($1::uuid, now() + make_interval(secs => $2::double precision))`,
		taskID, ttl.Seconds())
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return app.ErrSeated
	}
	if err != nil {
		return fmt.Errorf("runtime: claim seat: %w", err)
	}
	return nil
}

// Fill records the sandbox on a seat that is still pending.
func (s Seats) Fill(ctx context.Context, taskID, sandboxID string, expires time.Time) (bool, error) {
	if s.Pool == nil {
		return false, errNoDatabase
	}
	var until *time.Time
	if !expires.IsZero() {
		until = &expires
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE runtime.seats
		SET sandbox_id = $2, expires_at = coalesce($3::timestamptz, expires_at)
		WHERE task_id = $1::uuid AND sandbox_id IS NULL`,
		taskID, sandboxID, until)
	if err != nil {
		return false, fmt.Errorf("runtime: fill seat: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Seat reads the sandbox a task holds.
func (s Seats) Seat(ctx context.Context, taskID string) (string, bool, error) {
	if s.Pool == nil {
		return "", false, errNoDatabase
	}
	var id string
	err := s.Pool.QueryRow(ctx, `
		SELECT coalesce(sandbox_id, '') FROM runtime.seats WHERE task_id = $1::uuid`,
		taskID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("runtime: read seat: %w", err)
	}
	return id, true, nil
}

// Drop deletes the seat if it still holds sandboxID, or is still pending
// when sandboxID is empty.
func (s Seats) Drop(ctx context.Context, taskID, sandboxID string) error {
	if s.Pool == nil {
		return errNoDatabase
	}
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM runtime.seats
		WHERE task_id = $1::uuid AND sandbox_id IS NOT DISTINCT FROM nullif($2::text, '')`,
		taskID, sandboxID)
	if err != nil {
		return fmt.Errorf("runtime: drop seat: %w", err)
	}
	return nil
}
