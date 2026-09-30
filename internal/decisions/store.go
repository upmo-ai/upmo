package decisions

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the Postgres record of the last gate a route passed.
type Store struct {
	Pool *pgxpool.Pool
}

// Accepted returns the fingerprint the route is allowed to use.
func (s Store) Accepted(ctx context.Context, access Access, route string) (string, bool, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var fingerprint string
	err = tx.QueryRow(ctx, `SELECT fingerprint FROM decisions.gates WHERE route = $1`, route).Scan(&fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("decisions: read: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return fingerprint, true, nil
}

// Save records a passing gate.
func (s Store) Save(ctx context.Context, access Access, row Gate) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		INSERT INTO decisions.gates (route, kind, fingerprint, verdict, hit, total, bar)
		VALUES ($1, $2, $3, 'pass', $4, $5, $6)
		ON CONFLICT (route) DO UPDATE
		SET kind = EXCLUDED.kind,
		    fingerprint = EXCLUDED.fingerprint,
		    verdict = EXCLUDED.verdict,
		    hit = EXCLUDED.hit,
		    total = EXCLUDED.total,
		    bar = EXCLUDED.bar,
		    updated_at = now()`,
		row.Route, row.Kind, row.Fingerprint, row.Hit, row.Total, row.Bar)
	if err != nil {
		return fmt.Errorf("decisions: save: %w", err)
	}
	return tx.Commit(ctx)
}

func (s Store) begin(ctx context.Context, access Access) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("decisions: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true),
		       set_config('upmo.shared_view', $4, true),
		       set_config('upmo.shared_edit', $5, true)`,
		accessArgs(access)...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// accessArgs are the begin settings. An empty team or share list is "{}".
func accessArgs(access Access) []any {
	return []any{access.UserID, access.Allow, arrayOr(access.Teams), arrayOr(access.SharedView), arrayOr(access.SharedEdit)}
}

func arrayOr(literal string) string {
	if literal == "" {
		return "{}"
	}
	return literal
}
