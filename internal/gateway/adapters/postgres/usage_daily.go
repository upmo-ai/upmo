package postgres

import (
	"context"
	"errors"
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

// RollupUsage stores closed ledger days. Recent days are deleted before they
// are recomputed, so reruns never double count. A missing watermark or zone
// change rebuilds all daily totals from the ledger.
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
	var todayStart time.Time
	var todayDay time.Time
	if err := tx.QueryRow(ctx, `
		SELECT date_trunc('day', $2::timestamptz AT TIME ZONE $1) AT TIME ZONE $1,
			($2::timestamptz AT TIME ZONE $1)::date`, timeZone, now).Scan(&todayStart, &todayDay); err != nil {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	var rolledThrough time.Time
	var storedZone string
	err = tx.QueryRow(ctx, `SELECT rolled_through, time_zone FROM gateway.usage_daily_state WHERE id`).Scan(&rolledThrough, &storedZone)
	fullRebuild := errors.Is(err, pgx.ErrNoRows) || storedZone != timeZone
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	firstDay := todayDay.AddDate(0, 0, -rollupRecentDays)
	var rollupSQL string
	var args []any
	if fullRebuild {
		if _, err := tx.Exec(ctx, `DELETE FROM gateway.usage_daily`); err != nil {
			return fmt.Errorf("gateway: roll up usage: %w", err)
		}
		rollupSQL = rollupFullRebuildSQL
		args = []any{timeZone, todayStart, todayDay}
	} else {
		if _, err := tx.Exec(ctx, `DELETE FROM gateway.usage_daily WHERE day < $1::date AND (day >= $1::date - $2::int OR day > $3::date)`, todayDay, rollupRecentDays, rolledThrough); err != nil {
			return fmt.Errorf("gateway: roll up usage: %w", err)
		}
		watermarkStart := rolledThrough.AddDate(0, 0, 1)
		if watermarkStart.Before(firstDay) {
			firstDay = watermarkStart
		}
		lowerBound := firstDay.AddDate(0, 0, -1)
		rollupSQL = rollupIncrementalSQL
		args = []any{timeZone, todayStart, lowerBound, firstDay, todayDay}
	}
	if _, err := tx.Exec(ctx, rollupSQL, args...); err != nil {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway.usage_daily_state (id, rolled_through, time_zone, updated_at)
		VALUES (true, $1::date - 1, $2, now())
		ON CONFLICT (id) DO UPDATE SET rolled_through = EXCLUDED.rolled_through,
			time_zone = EXCLUDED.time_zone, updated_at = EXCLUDED.updated_at`, todayDay, timeZone); err != nil {
		return fmt.Errorf("gateway: roll up usage: %w", err)
	}
	return tx.Commit(ctx)
}

const rollupInsert = `
		INSERT INTO gateway.usage_daily (
			day, user_id, owner_team_id, agent_id, provider_account_id, model, plan_kind, source,
			calls, errors, input_tokens, output_tokens, cache_read, cache_write, cache_write_1h,
			reasoning, unpriced_calls, estimated, cost_micro_usd, list_cost_micro_usd
		)
		SELECT (l.at AT TIME ZONE $1)::date, l.user_id, l.owner_team_id, l.agent_id,
			l.provider_account_id, l.model, l.plan_kind, l.source,
			count(*), count(*) FILTER (WHERE l.failure_class <> ''),
			sum(l.input_tokens), sum(l.output_tokens), sum(l.cache_read), sum(l.cache_write),
			sum(l.cache_write_1h), sum(l.reasoning),
			count(*) FILTER (WHERE l.plan_kind = 'metered' AND (l.estimated OR l.cost_micro_usd IS NULL)),
			COALESCE(bool_or(l.estimated) FILTER (WHERE l.plan_kind = 'metered'), false),
			(sum(l.cost_micro_usd) FILTER (WHERE l.plan_kind = 'metered'))::bigint,
			(sum(l.list_cost_micro_usd) FILTER (WHERE l.plan_kind <> 'metered'))::bigint
		FROM gateway.ledger AS l
`

const rollupFullRebuildSQL = rollupInsert + `
WHERE l.at < $2::timestamptz
  AND (l.at AT TIME ZONE $1)::date < $3::date
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8`

const rollupIncrementalSQL = rollupInsert + `
WHERE l.at < $2::timestamptz
  -- The previous local midnight gives ambiguous fall-back midnights a margin;
  -- the local-date predicates below make the admitted day range exact.
  AND l.at >= ($3::date::timestamp AT TIME ZONE $1)
  AND (l.at AT TIME ZONE $1)::date >= $4::date
  AND (l.at AT TIME ZONE $1)::date < $5::date
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8`

// AnonymiseUser replaces userID with domain.AnonymousUserID in gateway.ledger
// and gateway.usage_daily in one transaction. Daily totals are summed into
// the anonymous row of the same full grain when one exists.
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
	if _, err := tx.Exec(ctx, `UPDATE gateway.ledger SET user_id = $2::uuid WHERE user_id = $1::uuid`, id, domain.AnonymousUserID); err != nil {
		return fmt.Errorf("gateway: anonymise user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH moved AS (
			DELETE FROM gateway.usage_daily WHERE user_id = $1::uuid
			RETURNING day, owner_team_id, agent_id, provider_account_id, model, plan_kind, source,
				calls, errors, input_tokens, output_tokens, cache_read, cache_write, cache_write_1h,
				reasoning, unpriced_calls, estimated, cost_micro_usd, list_cost_micro_usd
		)
		INSERT INTO gateway.usage_daily AS d (
			day, user_id, owner_team_id, agent_id, provider_account_id, model, plan_kind, source,
			calls, errors, input_tokens, output_tokens, cache_read, cache_write, cache_write_1h,
			reasoning, unpriced_calls, estimated, cost_micro_usd, list_cost_micro_usd
		)
		SELECT day, $2::uuid, owner_team_id, agent_id, provider_account_id, model, plan_kind, source,
			calls, errors, input_tokens, output_tokens, cache_read, cache_write, cache_write_1h,
			reasoning, unpriced_calls, estimated, cost_micro_usd, list_cost_micro_usd FROM moved
		ON CONFLICT (day, user_id, owner_team_id, agent_id, provider_account_id, model, plan_kind, source) DO UPDATE SET
			calls = d.calls + EXCLUDED.calls, errors = d.errors + EXCLUDED.errors,
			input_tokens = d.input_tokens + EXCLUDED.input_tokens,
			output_tokens = d.output_tokens + EXCLUDED.output_tokens,
			cache_read = d.cache_read + EXCLUDED.cache_read,
			cache_write = d.cache_write + EXCLUDED.cache_write,
			cache_write_1h = d.cache_write_1h + EXCLUDED.cache_write_1h,
			reasoning = d.reasoning + EXCLUDED.reasoning,
			unpriced_calls = d.unpriced_calls + EXCLUDED.unpriced_calls,
			estimated = d.estimated OR EXCLUDED.estimated,
			cost_micro_usd = CASE WHEN d.cost_micro_usd IS NULL THEN EXCLUDED.cost_micro_usd
				WHEN EXCLUDED.cost_micro_usd IS NULL THEN d.cost_micro_usd ELSE d.cost_micro_usd + EXCLUDED.cost_micro_usd END,
			list_cost_micro_usd = CASE WHEN d.list_cost_micro_usd IS NULL THEN EXCLUDED.list_cost_micro_usd
				WHEN EXCLUDED.list_cost_micro_usd IS NULL THEN d.list_cost_micro_usd ELSE d.list_cost_micro_usd + EXCLUDED.list_cost_micro_usd END`,
		id, domain.AnonymousUserID); err != nil {
		return fmt.Errorf("gateway: anonymise user: %w", err)
	}
	return tx.Commit(ctx)
}
