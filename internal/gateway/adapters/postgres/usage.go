package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/kernel"
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
	tx, err := u.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
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

// scanUsage is one pass over the window: totals by bucket, provider account,
// model and plan kind, and by key, provider account, model and plan kind. A
// non-empty user limits it to that person's rows.
//
// A subscription or local call is priced at the catalog rates of its
// account's vendor, else at the model-only fallback, and only when every rate
// it used is known. An account row security hides has no vendor here, so its
// calls take the fallback.
func scanUsage(ctx context.Context, q querier, key, user string, w domain.UsageWindow) (domain.UsageScan, error) {
	filter := ""
	args := []any{w.Unit, w.Zone.String(), w.Start, w.End, kernel.MicroUSDPerDollar}
	if user != "" {
		filter = "AND l.user_id = $6::uuid"
		args = append(args, user)
	}
	// key is one of usageKeys and filter is fixed text.
	rows, err := q.Query(ctx, `
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
			sum(s.list)::bigint
		FROM (
			SELECT date_trunc($1, l.at, $2) AS bucket,
				`+key+` AS key,
				COALESCE(l.provider_account_id::text, '') AS account,
				l.model, l.plan_kind, l.failure_class <> '' AS failed,
				l.input_tokens, l.output_tokens, l.cache_read, l.cache_write, l.cache_write_1h, l.reasoning,
				l.cost_micro_usd, l.estimated,
				CASE WHEN (pv.model IS NOT NULL OR pf.model IS NOT NULL)
					AND (l.input_tokens = 0 OR r.input IS NOT NULL)
					AND (l.output_tokens = 0 OR r.output IS NOT NULL)
					AND (l.cache_read = 0 OR r.cache_read IS NOT NULL)
					AND (l.cache_write-l.cache_write_1h = 0 OR r.cache_write IS NOT NULL) AND (l.cache_write_1h = 0 OR r.input IS NOT NULL)
				THEN div(
					l.input_tokens * COALESCE(r.input, 0) + l.output_tokens * COALESCE(r.output, 0)
					+ l.cache_read * COALESCE(r.cache_read, 0) + (l.cache_write-l.cache_write_1h) * COALESCE(r.cache_write, 0)
					+ l.cache_write_1h * COALESCE(r.input, 0) * 2 + 500000, 1000000)
				END AS list
			FROM gateway.ledger AS l
			LEFT JOIN gateway.provider_accounts AS a
				ON l.plan_kind <> 'metered' AND a.id = l.provider_account_id
			LEFT JOIN gateway.prices AS pv
				ON l.plan_kind <> 'metered' AND pv.vendor = lower(a.provider) AND pv.model = l.model
			LEFT JOIN gateway.prices AS pf
				ON l.plan_kind <> 'metered' AND pv.model IS NULL AND pf.vendor = '' AND pf.model = l.model
			LEFT JOIN LATERAL (
				SELECT context_min_tokens, input_usd_per_mtok, output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok
				FROM gateway.price_tiers
				WHERE (pv.model IS NOT NULL OR pf.model IS NOT NULL)
				  AND vendor = COALESCE(pv.vendor, pf.vendor) AND model = l.model
				  AND context_min_tokens < l.input_tokens::bigint + l.cache_read + l.cache_write
				ORDER BY context_min_tokens DESC LIMIT 1
			) AS pt ON true
			LEFT JOIN LATERAL (
				SELECT round(COALESCE(pt.input_usd_per_mtok, pv.input_usd_per_mtok, pf.input_usd_per_mtok) * $5)::numeric AS input,
					round(COALESCE(pt.output_usd_per_mtok, pv.output_usd_per_mtok, pf.output_usd_per_mtok) * $5)::numeric AS output,
					round(COALESCE(pt.cache_read_usd_per_mtok, pv.cache_read_usd_per_mtok, pf.cache_read_usd_per_mtok) * $5)::numeric AS cache_read,
					round(COALESCE(pt.cache_write_usd_per_mtok, pv.cache_write_usd_per_mtok, pf.cache_write_usd_per_mtok) * $5)::numeric AS cache_write
				WHERE pv.model IS NOT NULL OR pf.model IS NOT NULL
			) AS r ON true
			WHERE l.at >= $3 AND l.at < $4 `+filter+`
		) AS s
		GROUP BY GROUPING SETS (
			(s.bucket, s.account, s.model, s.plan_kind),
			(s.key, s.account, s.model, s.plan_kind)
		)`, args...)
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
