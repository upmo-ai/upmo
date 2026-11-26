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

// errNoDatabase reports a TaskSandboxes with no pool.
var errNoDatabase = errors.New("runtime: no database")

// TaskSandboxes is the TaskSandboxStore in runtime.task_sandboxes. Each method is one statement, so
// it needs no transaction and no access settings: a task sandbox row is not a work object.
type TaskSandboxes struct {
	Pool *pgxpool.Pool
}

var _ app.TaskSandboxStore = TaskSandboxes{}

// Claim inserts a pending task sandbox that lapses after ttl, with the hash
// of its proxy secret. upmo_app may insert the hash but never read it back.
func (s TaskSandboxes) Claim(ctx context.Context, taskID string, ttl time.Duration, secretHash []byte) error {
	if s.Pool == nil {
		return errNoDatabase
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO runtime.task_sandboxes (task_id, expires_at, proxy_secret_hash)
		VALUES ($1::uuid, now() + make_interval(secs => $2::double precision), $3)`,
		taskID, ttl.Seconds(), secretHash)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return app.ErrAlreadyHeld
	}
	if err != nil {
		return fmt.Errorf("runtime: claim task sandbox: %w", err)
	}
	return nil
}

// Fill records the sandbox on a row that is pending or already holds that
// sandbox, through runtime.fill_task_sandbox: upmo_app may not write
// sandbox_id itself.
func (s TaskSandboxes) Fill(ctx context.Context, taskID, sandboxID string, expires time.Time) (bool, error) {
	if s.Pool == nil {
		return false, errNoDatabase
	}
	var until *time.Time
	if !expires.IsZero() {
		until = &expires
	}
	var filled bool
	err := s.Pool.QueryRow(ctx, `SELECT runtime.fill_task_sandbox($1::uuid, $2, $3::timestamptz)`,
		taskID, sandboxID, until).Scan(&filled)
	if err != nil {
		return false, fmt.Errorf("runtime: fill task sandbox: %w", err)
	}
	return filled, nil
}

// Lookup reads the sandbox a task holds.
func (s TaskSandboxes) Lookup(ctx context.Context, taskID string) (string, bool, error) {
	if s.Pool == nil {
		return "", false, errNoDatabase
	}
	var id string
	err := s.Pool.QueryRow(ctx, `
		SELECT coalesce(sandbox_id, '') FROM runtime.task_sandboxes WHERE task_id = $1::uuid`,
		taskID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("runtime: read task sandbox: %w", err)
	}
	return id, true, nil
}

// Drop deletes the row if it still holds sandboxID, or is still pending
// when sandboxID is empty.
func (s TaskSandboxes) Drop(ctx context.Context, taskID, sandboxID string) error {
	if s.Pool == nil {
		return errNoDatabase
	}
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM runtime.task_sandboxes
		WHERE task_id = $1::uuid AND sandbox_id IS NOT DISTINCT FROM nullif($2::text, '')`,
		taskID, sandboxID)
	if err != nil {
		return fmt.Errorf("runtime: drop task sandbox: %w", err)
	}
	return nil
}
