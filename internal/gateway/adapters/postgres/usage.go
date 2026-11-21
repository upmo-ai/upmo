package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// Usage reads the ledger for the usage reports on the app pool, under row
// security, in read-only transactions.
type Usage struct {
	Pool *pgxpool.Pool
}

var _ app.UsageStore = Usage{}

// usageKeys are the workspace grouping keys. They are fixed SQL, never input.
var usageKeys = map[domain.UsageGroup]string{
	domain.GroupPerson:   "COALESCE(l.user_id::text, '')",
	domain.GroupTeam:     "COALESCE(l.owner_team_id::text, '')",
	domain.GroupAgent:    "COALESCE(l.agent_id::text, '')",
	domain.GroupModel:    "l.model",
	domain.GroupProvider: "COALESCE(l.provider_account_id::text, '')",
	domain.GroupSource:   "l.source",
}

// usageWorkMem is the sort memory of a usage read, set for its transaction
// only. It keeps the grouped sort of about 330,000 ledger rows (a 30-day
// window at a million rows per 90 days) in memory; each sort node may use it.
const usageWorkMem = "64MB"

// begin opens a read-only transaction and sets who reads for row security.
func (u Usage) begin(ctx context.Context, who app.Caller) (pgx.Tx, error) {
	if u.Pool == nil {
		return nil, fmt.Errorf("gateway: no database")
	}
	if _, err := uuid.Parse(who.User); err != nil {
		return nil, fmt.Errorf("gateway: usage reader")
	}
	tx, err := u.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
			set_config('upmo.allow', $2, true),
			set_config('upmo.teams', $3, true),
			set_config('work_mem', $4, true)`, who.User, who.Allow, uuidList(who.Teams), usageWorkMem); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// PersonalUsage reads the caller's own rows. Allow is always empty here, so
// usage:view cannot widen it, and the user filter uses ledger_user_at.
func (u Usage) PersonalUsage(ctx context.Context, who app.Caller, w domain.UsageWindow) (domain.UsageScan, error) {
	who.Allow = ""
	tx, err := u.begin(ctx, who)
	if err != nil {
		return domain.UsageScan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	scan, err := scanUsage(ctx, tx, "l.source", who.User, w)
	if err != nil {
		return domain.UsageScan{}, err
	}
	if scan.Latency, err = usageLatency(ctx, tx, who.User, w); err != nil {
		return domain.UsageScan{}, err
	}
	if scan.Tasks, err = usageTasks(ctx, tx, who.User, w); err != nil {
		return domain.UsageScan{}, err
	}
	if err := usageNames(ctx, tx, who.User, &scan); err != nil {
		return domain.UsageScan{}, err
	}
	return scan, tx.Commit(ctx)
}

// WorkspaceUsage reads every row the caller's allow list admits, system rows
// included, grouped by group.
func (u Usage) WorkspaceUsage(ctx context.Context, who app.Caller, w domain.UsageWindow, group domain.UsageGroup) (domain.UsageScan, error) {
	key, ok := usageKeys[group]
	if !ok {
		return domain.UsageScan{}, fmt.Errorf("gateway: usage group %q", group)
	}
	tx, err := u.begin(ctx, who)
	if err != nil {
		return domain.UsageScan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	scan, err := scanUsage(ctx, tx, key, "", w)
	if err != nil {
		return domain.UsageScan{}, err
	}
	if err := usageNames(ctx, tx, who.User, &scan); err != nil {
		return domain.UsageScan{}, err
	}
	return scan, tx.Commit(ctx)
}

// RecordWorkspaceView seals one workspace usage read in its own short
// transaction, since the read ran read-only.
func (u Usage) RecordWorkspaceView(ctx context.Context, who app.Caller, r domain.UsageRange, group domain.UsageGroup, format string) error {
	if u.Pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := u.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.UsageWorkspaceView,
		Outcome:  audit.Success,
		TargetID: string(group),
		Changes: audit.Changes{
			Fields: []string{"format", "group", "range"},
			Values: map[string]audit.Change{
				"range":  {To: string(r)},
				"group":  {To: string(group)},
				"format": {To: format},
			},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const usageScanQuery = `
		SELECT GROUPING(s.bucket) = 1, s.bucket, s.key, s.account, s.model, s.plan_kind,
			count(*),
			count(*) FILTER (WHERE s.failed),
			COALESCE(sum(s.input_tokens), 0)::bigint,
			COALESCE(sum(s.output_tokens), 0)::bigint,
			COALESCE(sum(s.cache_read), 0)::bigint,
			COALESCE(sum(s.cache_write), 0)::bigint,
			COALESCE(sum(s.reasoning), 0)::bigint,
			(sum(s.cost_micro_usd) FILTER (WHERE s.plan_kind = 'metered'))::bigint,
			COALESCE(bool_or(s.estimated) FILTER (WHERE s.plan_kind = 'metered'), false),
			count(*) FILTER (WHERE s.plan_kind = 'metered' AND (s.estimated OR s.cost_micro_usd IS NULL)),
			sum(s.list_cost_micro_usd) FILTER (WHERE s.plan_kind <> 'metered')::bigint
		FROM (
			SELECT date_trunc($1, l.at, $2) AS bucket,
				%s AS key,
				COALESCE(l.provider_account_id::text, '') AS account,
				l.model, l.plan_kind, l.failure_class <> '' AS failed,
				l.input_tokens, l.output_tokens, l.cache_read, l.cache_write, l.cache_write_1h, l.reasoning,
				l.cost_micro_usd, l.estimated, l.list_cost_micro_usd
			FROM gateway.ledger AS l
			WHERE l.at >= $3 AND l.at < $4 %s
		) AS s
		GROUP BY GROUPING SETS (
			(s.bucket, s.account, s.model, s.plan_kind),
			(s.key, s.account, s.model, s.plan_kind)
		)`

const usageHybridScanQuery = `
		SELECT GROUPING(s.bucket) = 1, s.bucket, s.key, s.account, s.model, s.plan_kind,
			sum(s.calls)::bigint, sum(s.errors)::bigint,
			COALESCE(sum(s.input_tokens), 0)::bigint, COALESCE(sum(s.output_tokens), 0)::bigint,
			COALESCE(sum(s.cache_read), 0)::bigint, COALESCE(sum(s.cache_write), 0)::bigint,
			COALESCE(sum(s.reasoning), 0)::bigint,
			(sum(s.cost_micro_usd) FILTER (WHERE s.plan_kind = 'metered'))::bigint,
			COALESCE(bool_or(s.estimated) FILTER (WHERE s.plan_kind = 'metered'), false),
			sum(s.unpriced)::bigint,
			sum(s.list_cost_micro_usd) FILTER (WHERE s.plan_kind <> 'metered')::bigint
		FROM (
			SELECT date_trunc($1, l.at, $2) AS bucket, %s AS key,
				COALESCE(l.provider_account_id::text, '') AS account, l.model, l.plan_kind,
				1::bigint AS calls, (l.failure_class <> '')::int AS errors,
				l.input_tokens, l.output_tokens, l.cache_read, l.cache_write, l.reasoning,
				l.cost_micro_usd, l.estimated,
				(l.plan_kind = 'metered' AND (l.estimated OR l.cost_micro_usd IS NULL))::int AS unpriced,
				l.list_cost_micro_usd
			FROM gateway.ledger AS l
			WHERE l.at >= $3 AND l.at < $4
				AND l.at >= ($6::date::timestamp AT TIME ZONE $2)
				AND (l.at AT TIME ZONE $2)::date > $6::date %s
			UNION ALL
			SELECT date_trunc($1, l.at, $2), %s AS key,
				COALESCE(l.provider_account_id::text, ''), l.model, l.plan_kind,
				1::bigint, (l.failure_class <> '')::int,
				l.input_tokens, l.output_tokens, l.cache_read, l.cache_write, l.reasoning,
				l.cost_micro_usd, l.estimated,
				(l.plan_kind = 'metered' AND (l.estimated OR l.cost_micro_usd IS NULL))::int,
				l.list_cost_micro_usd
			FROM gateway.ledger AS l
			WHERE l.at >= $3 AND l.at < ($5::date::timestamp AT TIME ZONE $2)
				AND (l.at AT TIME ZONE $2)::date < $5::date %s
			UNION ALL
			SELECT date_trunc($1, (l.day::timestamp AT TIME ZONE $2), $2), %s AS key,
				COALESCE(l.provider_account_id::text, ''), l.model, l.plan_kind,
				l.calls, l.errors, l.input_tokens, l.output_tokens, l.cache_read, l.cache_write,
				l.reasoning, l.cost_micro_usd, l.estimated, l.unpriced_calls, l.list_cost_micro_usd
			FROM gateway.usage_daily AS l
			WHERE l.day >= $5::date AND l.day <= $6::date %s
		) AS s
		GROUP BY GROUPING SETS (
			(s.bucket, s.account, s.model, s.plan_kind),
			(s.key, s.account, s.model, s.plan_kind))`

// UsageScanExplainQuery returns the unfiltered workspace form of the exact scan query.
func UsageScanExplainQuery() string { return fmt.Sprintf(usageScanQuery, "l.source", "") }

// UsageHybridScanExplainQuery returns the hybrid scan for EXPLAIN.
func UsageHybridScanExplainQuery(personal bool) string {
	return usageHybridScanQueryFor("l.source", personal)
}

func usageHybridScanQueryFor(key string, personal bool) string {
	filter := ""
	if personal {
		filter = "AND l.user_id = $7::uuid"
	}
	return fmt.Sprintf(usageHybridScanQuery, key, filter, key, filter, key, filter)
}

// scanUsage is one pass over the window: totals by bucket, provider account,
// model and plan kind, and by key, provider account, model and plan kind. A
// non-empty user limits it to that person's rows.
//
// List cost is frozen on the ledger row; NULL means at least one used rate
// was unknown when the call was recorded.
func scanUsage(ctx context.Context, q querier, key, user string, w domain.UsageWindow) (domain.UsageScan, error) {
	if w.Unit != domain.BucketDay {
		return scanLedgerUsage(ctx, q, key, user, w)
	}
	return scanUsageWithQuery(ctx, q, key, user, w, true)
}

func scanLedgerUsage(ctx context.Context, q querier, key, user string, w domain.UsageWindow) (domain.UsageScan, error) {
	return scanUsageWithQuery(ctx, q, key, user, w, false)
}

func scanUsageWithQuery(ctx context.Context, q querier, key, user string, w domain.UsageWindow, hybrid bool) (domain.UsageScan, error) {
	filter := ""
	args := []any{w.Unit, w.Zone.String(), w.Start, w.End}
	if user != "" {
		filter = "AND l.user_id = $5::uuid"
		args = append(args, user)
	}
	query := fmt.Sprintf(usageScanQuery, key, filter)
	if hybrid && w.Unit == domain.BucketDay {
		var rolled time.Time
		var zone string
		err := q.QueryRow(ctx, `SELECT rolled_through, time_zone FROM gateway.usage_daily_state WHERE id`).Scan(&rolled, &zone)
		if err == nil && zone == w.Zone.String() {
			first := w.Start.In(w.Zone)
			firstDay := civilDate(first)
			if before := w.Start.Add(-time.Nanosecond).In(w.Zone); sameCivilDate(before, first) {
				firstDay = firstDay.AddDate(0, 0, 1)
			}
			end := w.End.Add(-time.Nanosecond).In(w.Zone)
			today := civilDate(end)
			cutoff := today.AddDate(0, 0, -3)
			rolledDay := civilDate(rolled)
			if rolledDay.Before(cutoff) {
				cutoff = rolledDay
			}
			if !cutoff.Before(firstDay) {
				// Postgres casts timestamptz to date in the session zone. Pass
				// civil dates explicitly so a local midnight stays that date.
				firstDate := firstDay.Format("2006-01-02")
				cutoffDate := cutoff.Format("2006-01-02")
				query = usageHybridScanQueryFor(key, user != "")
				args = []any{w.Unit, w.Zone.String(), w.Start, w.End, firstDate, cutoffDate}
				if user != "" {
					args = append(args, user)
				}
			}
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.UsageScan{}, err
		}
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return domain.UsageScan{}, err
	}
	defer rows.Close()
	var scan domain.UsageScan
	for rows.Next() {
		var keyed bool
		var bucket *time.Time
		var keyValue *string
		var c domain.UsageCell
		if err := rows.Scan(&keyed, &bucket, &keyValue, &c.Account, &c.Model, &c.PlanKind,
			&c.Requests, &c.Errors, &c.Input, &c.Output, &c.CacheRead, &c.CacheWrite, &c.Reasoning,
			&c.SpendMicroUSD, &c.Estimated, &c.Unpriced, &c.ListMicroUSD); err != nil {
			return domain.UsageScan{}, err
		}
		if keyed {
			if keyValue != nil {
				c.Key = *keyValue
			}
			scan.Keyed = append(scan.Keyed, c)
			continue
		}
		if bucket != nil {
			c.Bucket = *bucket
		}
		scan.Buckets = append(scan.Buckets, c)
	}
	return scan, rows.Err()
}

func civilDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func sameCivilDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// usageLatency is the latency percentiles of the person's calls per provider
// account.
func usageLatency(ctx context.Context, q querier, user string, w domain.UsageWindow) (map[string]domain.UsageLatency, error) {
	rows, err := q.Query(ctx, `
		SELECT COALESCE(l.provider_account_id::text, ''),
			percentile_cont(ARRAY[0.5, 0.95, 0.99]) WITHIN GROUP (ORDER BY l.latency_ms)
		FROM gateway.ledger AS l
		WHERE l.user_id = $1::uuid AND l.at >= $2 AND l.at < $3 AND l.latency_ms IS NOT NULL
		GROUP BY 1`, user, w.Start, w.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.UsageLatency{}
	for rows.Next() {
		var account string
		var p []float64
		if err := rows.Scan(&account, &p); err != nil {
			return nil, err
		}
		if len(p) == 3 {
			out[account] = domain.UsageLatency{P50: domain.LatencyMS(p[0]), P95: domain.LatencyMS(p[1]), P99: domain.LatencyMS(p[2])}
		}
	}
	return out, rows.Err()
}

// usageTasks rolls the person's run calls up to their task, the parent run
// when there is one, per model.
func usageTasks(ctx context.Context, q querier, user string, w domain.UsageWindow) ([]domain.UsageTaskCell, error) {
	rows, err := q.Query(ctx, `
		SELECT coalesce(l.parent_run_id, l.run_id)::text, l.model,
			COALESCE(sum(l.input_tokens), 0)::bigint,
			COALESCE(sum(l.output_tokens), 0)::bigint,
			COALESCE(sum(l.cache_read), 0)::bigint,
			COALESCE(sum(l.cache_write), 0)::bigint,
			(sum(l.cost_micro_usd) FILTER (WHERE l.plan_kind = 'metered'))::bigint
		FROM gateway.ledger AS l
		WHERE l.user_id = $1::uuid AND l.at >= $2 AND l.at < $3 AND l.run_id IS NOT NULL
		GROUP BY 1, 2`, user, w.Start, w.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.UsageTaskCell{}
	for rows.Next() {
		var c domain.UsageTaskCell
		if err := rows.Scan(&c.Task, &c.Model, &c.Input, &c.Output, &c.CacheRead, &c.CacheWrite, &c.SpendMicroUSD); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// usageNames reads the provider accounts in the scan that the reader can
// see. An account row security hides stays out, so it is shown as a private
// provider.
func usageNames(ctx context.Context, q querier, user string, scan *domain.UsageScan) error {
	accounts := []string{}
	seen := map[string]bool{}
	for _, cells := range [][]domain.UsageCell{scan.Buckets, scan.Keyed} {
		for _, c := range cells {
			if c.Account != "" && !seen[c.Account] {
				seen[c.Account] = true
				accounts = append(accounts, c.Account)
			}
		}
	}
	scan.Accounts = map[string]domain.UsageAccount{}
	if len(accounts) == 0 {
		return nil
	}
	rows, err := q.Query(ctx, `
		SELECT a.id::text, a.name, NOT a.house AND a.owner_user_id IS NOT DISTINCT FROM $2::uuid
		FROM gateway.provider_accounts AS a
		WHERE a.id = ANY ($1::uuid[])`, accounts, user)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a domain.UsageAccount
		if err := rows.Scan(&a.ID, &a.Name, &a.Own); err != nil {
			return err
		}
		scan.Accounts[a.ID] = a
	}
	return rows.Err()
}
