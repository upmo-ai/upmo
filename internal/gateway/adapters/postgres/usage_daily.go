package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// rollupRecentDays is how many of the latest closed days each roll-up
// recomputes, so a call settled after its day closed is still counted.
const rollupRecentDays = 2

// lockUsageDaily serialises the roll-up and an erasure, so neither reads the
// ledger while the other is moving its rows.
func lockUsageDaily(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(pg_catalog.hashtext('gateway.usage_daily'))`)
	return err
}

// RollupUsage writes gateway.usage_daily from gateway.ledger for the days in
// timeZone that closed before now's day. The latest rollupRecentDays closed
// days are replaced by a fresh aggregate of their ledger rows, and every
// older day that has ledger rows and no totals gains them. An older day that
// already has totals is left as it is. All of it runs in one transaction, so
// a re-run never double counts.
func RollupUsage(ctx context.Context, pool *pgxpool.Pool, timeZone string, now time.Time) error {
	if pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockUsageDaily(ctx, tx); err != nil {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	// today is local midnight of now's day in timeZone: the end of the last
	// closed day.
	var today time.Time
	if err := tx.QueryRow(ctx, `
		SELECT date_trunc('day', $2::timestamptz AT TIME ZONE $1) AT TIME ZONE $1`,
		timeZone, now,
	).Scan(&today); err != nil {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM gateway.usage_daily
		WHERE day >= ($2::timestamptz AT TIME ZONE $1)::date - $3::int
		  AND day < ($2::timestamptz AT TIME ZONE $1)::date`,
		timeZone, today, rollupRecentDays); err != nil {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway.usage_daily (
			day, user_id, model, source, calls,
			input_tokens, output_tokens, cache_read, cache_write, reasoning, cost_micro_usd
		)
		SELECT (l.at AT TIME ZONE $1)::date, l.user_id, l.model, l.source, count(*),
			sum(l.input_tokens), sum(l.output_tokens), sum(l.cache_read), sum(l.cache_write), sum(l.reasoning),
			COALESCE(sum(l.cost_micro_usd), 0)::bigint
		FROM gateway.ledger AS l
		WHERE l.at < $2
		  AND NOT EXISTS (
			SELECT 1 FROM gateway.usage_daily AS d
			WHERE d.day = (l.at AT TIME ZONE $1)::date
		  )
		GROUP BY 1, 2, 3, 4`, timeZone, today); err != nil {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	return tx.Commit(ctx)
}

// AnonymiseUser replaces userID with domain.AnonymousUserID in gateway.ledger
// and gateway.usage_daily in one transaction. Daily totals are summed into
// the anonymous row of the same day, model and source when one exists, so
// workspace totals do not change. Running it again changes nothing.
func AnonymiseUser(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	if pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	id, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("gateway: invalid user id")
	}
	if id == uuid.Nil {
		return fmt.Errorf("gateway: cannot anonymise the anonymous user")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockUsageDaily(ctx, tx); err != nil {
		return fmt.Errorf("gateway: anonymise user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE gateway.ledger SET user_id = $2::uuid WHERE user_id = $1::uuid`,
		id, domain.AnonymousUserID); err != nil {
		return fmt.Errorf("gateway: anonymise user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH moved AS (
			DELETE FROM gateway.usage_daily
			WHERE user_id = $1::uuid
			RETURNING day, model, source, calls,
				input_tokens, output_tokens, cache_read, cache_write, reasoning, cost_micro_usd
		)
		INSERT INTO gateway.usage_daily AS d (
			day, user_id, model, source, calls,
			input_tokens, output_tokens, cache_read, cache_write, reasoning, cost_micro_usd
		)
		SELECT day, $2::uuid, model, source, calls,
			input_tokens, output_tokens, cache_read, cache_write, reasoning, cost_micro_usd
		FROM moved
		ON CONFLICT (day, user_id, model, source) DO UPDATE SET
			calls = d.calls + EXCLUDED.calls,
			input_tokens = d.input_tokens + EXCLUDED.input_tokens,
			output_tokens = d.output_tokens + EXCLUDED.output_tokens,
			cache_read = d.cache_read + EXCLUDED.cache_read,
			cache_write = d.cache_write + EXCLUDED.cache_write,
			reasoning = d.reasoning + EXCLUDED.reasoning,
			cost_micro_usd = d.cost_micro_usd + EXCLUDED.cost_micro_usd`,
		id, domain.AnonymousUserID); err != nil {
		return fmt.Errorf("gateway: anonymise user: %w", err)
	}
	return tx.Commit(ctx)
}
