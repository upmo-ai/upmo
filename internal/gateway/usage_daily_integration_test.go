//go:build integration

package gateway

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	usagePerson = "019a0000-0000-7000-8000-000000000358"
	usageOther  = "019a0000-0000-7000-8000-000000000359"
)

// usageTotals is calls, input, output, cache read, cache write, reasoning and
// cost, in that order.
type usageTotals [7]int64

func newUsageDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := migrate.Up(context.Background(), pool, Schema, Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func zone(name string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return name, nil }
}

// seedHourlyLedger writes one ledger row per hour for hours hours from
// start, varying the person, model, source, plan and token counts by row so
// every grain of usage_daily is exercised. A third of the rows are
// subscription calls and a third are metered calls with an unknown price, so
// both carry no cost.
func seedHourlyLedger(t *testing.T, pool *pgxpool.Pool, start time.Time, hours int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO gateway.ledger (
			at, route, link_position, model, plan_kind, failure_class, user_id,
			input_tokens, output_tokens, cache_read, cache_write, reasoning,
			cost_micro_usd, source, price_source
		)
		SELECT $1::timestamptz + make_interval(hours => i), 'usage', 1,
			CASE i % 2 WHEN 0 THEN 'model-a' ELSE 'model-b' END,
			CASE WHEN i % 3 = 0 THEN 'subscription' ELSE 'metered' END,
			'',
			CASE WHEN i % 4 = 3 THEN NULL WHEN i % 5 < 2 THEN $3::uuid ELSE $4::uuid END,
			i % 97, i % 89, i % 13, i % 7, i % 5,
			CASE WHEN i % 3 = 1 THEN i END,
			CASE i % 4 WHEN 0 THEN 'chat' WHEN 1 THEN 'task' WHEN 2 THEN 'memory' ELSE 'system' END,
			CASE i % 3 WHEN 1 THEN 'provider' WHEN 2 THEN 'unknown' END
		FROM generate_series(0, $2::int - 1) AS i`,
		start, hours, usagePerson, usageOther); err != nil {
		t.Fatal(err)
	}
}

func insertUsageRow(t *testing.T, pool *pgxpool.Pool, at time.Time, userID, model, source string, tokens int, cost int64) {
	t.Helper()
	var user any
	if userID != "" {
		user = userID
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO gateway.ledger (
			at, route, link_position, model, plan_kind, failure_class, user_id,
			input_tokens, output_tokens, cost_micro_usd, source, price_source
		) VALUES ($1, 'usage', 1, $3, 'metered', '', $2::uuid, $4, $4, $5, $6, 'provider')`,
		at, user, model, tokens, cost, source); err != nil {
		t.Fatal(err)
	}
}

func monthlyTotals(t *testing.T, pool *pgxpool.Pool, query string, args ...any) map[string]usageTotals {
	t.Helper()
	rows, err := pool.Query(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]usageTotals{}
	for rows.Next() {
		var month string
		var v usageTotals
		if err := rows.Scan(&month, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6]); err != nil {
			t.Fatal(err)
		}
		out[month] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func ledgerMonthly(t *testing.T, pool *pgxpool.Pool, tz string) map[string]usageTotals {
	t.Helper()
	return monthlyTotals(t, pool, `
		SELECT to_char(at AT TIME ZONE $1, 'YYYY-MM'), count(*),
			sum(input_tokens)::bigint, sum(output_tokens)::bigint, sum(cache_read)::bigint,
			sum(cache_write)::bigint, sum(reasoning)::bigint, COALESCE(sum(cost_micro_usd), 0)::bigint
		FROM gateway.ledger
		GROUP BY 1`, tz)
}

func dailyMonthly(t *testing.T, pool *pgxpool.Pool) map[string]usageTotals {
	t.Helper()
	return monthlyTotals(t, pool, `
		SELECT to_char(day, 'YYYY-MM'), sum(calls)::bigint,
			sum(input_tokens)::bigint, sum(output_tokens)::bigint, sum(cache_read)::bigint,
			sum(cache_write)::bigint, sum(reasoning)::bigint, COALESCE(sum(cost_micro_usd), 0)::bigint
		FROM gateway.usage_daily
		GROUP BY 1`)
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFeature_UPM358_RollupKeepsMonthlyTotals(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	const tz = "Asia/Kolkata"
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, loc)
	start := time.Date(2025, 7, 1, 0, 0, 0, 0, loc)
	today := time.Date(2026, 11, 20, 0, 0, 0, 0, loc)
	hours := int(today.Sub(start) / time.Hour)
	seedHourlyLedger(t, pool, start, hours)

	before := ledgerMonthly(t, pool, tz)
	if len(before) != 17 {
		t.Fatalf("seeded months = %d, want 17", len(before))
	}

	for range 2 {
		if err := RollupUsageOnce(ctx, pool, zone(tz), now, nil); err != nil {
			t.Fatal(err)
		}
		after := dailyMonthly(t, pool)
		if len(after) != len(before) {
			t.Fatalf("months in usage_daily = %d, want %d", len(after), len(before))
		}
		for month, want := range before {
			if got := after[month]; got != want {
				t.Errorf("month %s: usage_daily = %v, ledger = %v", month, got, want)
			}
		}
		if n := countRows(t, pool, `SELECT count(*) FROM gateway.ledger`); n != int64(hours) {
			t.Fatalf("ledger rows = %d, want all %d kept", n, hours)
		}
	}

	// A day older than the latest closed days keeps the totals it has.
	insertUsageRow(t, pool, start.Add(time.Hour), usagePerson, "model-a", "chat", 10, 5)
	if err := RollupUsageOnce(ctx, pool, zone(tz), now, nil); err != nil {
		t.Fatal(err)
	}
	if got := dailyMonthly(t, pool)["2025-07"]; got != before["2025-07"] {
		t.Fatalf("2025-07 after a late row = %v, want %v", got, before["2025-07"])
	}
}

func TestFeature_UPM358_RollupDayFollowsWorkspaceZone(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	insertUsageRow(t, pool, time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC), usagePerson, "model-a", "chat", 10, 7)
	// Today in Tokyo has not closed, so its row stays out of the totals.
	insertUsageRow(t, pool, now.Add(-time.Hour), usagePerson, "model-a", "chat", 10, 7)

	days := func() map[string]int64 {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT day::text, calls FROM gateway.usage_daily`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var day string
			var calls int64
			if err := rows.Scan(&day, &calls); err != nil {
				t.Fatal(err)
			}
			out[day] = calls
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	for range 2 {
		if err := RollupUsageOnce(ctx, pool, zone("Asia/Tokyo"), now, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := days(); len(got) != 1 || got["2026-11-18"] != 1 {
		t.Fatalf("days after two runs = %v, want one call on 2026-11-18", got)
	}

	// A row that reaches a closed day later is counted once on the next run.
	insertUsageRow(t, pool, time.Date(2026, 11, 18, 1, 0, 0, 0, time.UTC), usagePerson, "model-a", "chat", 10, 7)
	if err := RollupUsageOnce(ctx, pool, zone("Asia/Tokyo"), now, nil); err != nil {
		t.Fatal(err)
	}
	if got := days(); len(got) != 1 || got["2026-11-18"] != 2 {
		t.Fatalf("days after a late row = %v, want two calls on 2026-11-18", got)
	}
	var cost int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(cost_micro_usd), 0)::bigint FROM gateway.usage_daily`).Scan(&cost); err != nil || cost != 14 {
		t.Fatalf("cost = %d, %v; want 14", cost, err)
	}
}

func TestFeature_UPM358_RollupDayAcrossDST(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	const tz = "Europe/Berlin"
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	local := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 30, 0, loc)
	}
	// 2026-03-29 has 23 local hours and 2026-10-25 has 25.
	if d := local(time.March, 30, 0, 0).Sub(local(time.March, 29, 0, 0)); d != 23*time.Hour {
		t.Fatalf("spring-forward day = %v, want 23h", d)
	}
	if d := local(time.October, 26, 0, 0).Sub(local(time.October, 25, 0, 0)); d != 25*time.Hour {
		t.Fatalf("fall-back day = %v, want 25h", d)
	}

	rows := []struct {
		at  time.Time
		day string
	}{
		{local(time.March, 28, 0, 0), "2026-03-28"},
		{local(time.March, 28, 12, 0), "2026-03-28"},
		{local(time.March, 28, 23, 59), "2026-03-28"},
		{local(time.March, 29, 0, 0), "2026-03-29"},
		// 01:59:30 CET and 03:00:30 CEST, either side of the skipped hour.
		{time.Date(2026, time.March, 29, 0, 59, 30, 0, time.UTC), "2026-03-29"},
		{time.Date(2026, time.March, 29, 1, 0, 30, 0, time.UTC), "2026-03-29"},
		{local(time.March, 29, 12, 0), "2026-03-29"},
		{local(time.March, 29, 23, 59), "2026-03-29"},
		{local(time.March, 30, 0, 0), "2026-03-30"},
		{local(time.March, 30, 12, 0), "2026-03-30"},
		{local(time.March, 30, 23, 59), "2026-03-30"},
		{local(time.October, 24, 0, 0), "2026-10-24"},
		{local(time.October, 24, 12, 0), "2026-10-24"},
		{local(time.October, 24, 23, 59), "2026-10-24"},
		{local(time.October, 25, 0, 0), "2026-10-25"},
		// 02:30:30 CEST and 02:30:30 CET, the repeated hour.
		{time.Date(2026, time.October, 25, 0, 30, 30, 0, time.UTC), "2026-10-25"},
		{time.Date(2026, time.October, 25, 1, 30, 30, 0, time.UTC), "2026-10-25"},
		{local(time.October, 25, 12, 0), "2026-10-25"},
		{local(time.October, 25, 23, 59), "2026-10-25"},
		{local(time.October, 26, 0, 0), "2026-10-26"},
		{local(time.October, 26, 12, 0), "2026-10-26"},
		{local(time.October, 26, 23, 59), "2026-10-26"},
	}
	type dayTotal struct{ calls, tokens, cost int64 }
	want := map[string]dayTotal{}
	for i, r := range rows {
		tokens := int64(1) << i
		insertUsageRow(t, pool, r.at, usagePerson, "model-a", "chat", int(tokens), tokens)
		w := want[r.day]
		want[r.day] = dayTotal{w.calls + 1, w.tokens + tokens, w.cost + tokens}
	}

	days := func() map[string]dayTotal {
		t.Helper()
		res, err := pool.Query(ctx, `
			SELECT day::text, sum(calls)::bigint, sum(input_tokens)::bigint, sum(cost_micro_usd)::bigint
			FROM gateway.usage_daily GROUP BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Close()
		out := map[string]dayTotal{}
		for res.Next() {
			var day string
			var v dayTotal
			if err := res.Scan(&day, &v.calls, &v.tokens, &v.cost); err != nil {
				t.Fatal(err)
			}
			out[day] = v
		}
		if err := res.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// The first two runs recompute the fall-back day as a recent closed day,
	// the last treats it as an older day that already has totals.
	for _, now := range []time.Time{
		local(time.October, 27, 12, 0),
		local(time.October, 27, 12, 0),
		local(time.November, 20, 12, 0),
	} {
		if err := RollupUsageOnce(ctx, pool, zone(tz), now, nil); err != nil {
			t.Fatal(err)
		}
		got := days()
		if len(got) != len(want) {
			t.Fatalf("now %v: days = %v, want %v", now, got, want)
		}
		for day, w := range want {
			if got[day] != w {
				t.Errorf("now %v: day %s = %+v, want %+v", now, day, got[day], w)
			}
		}
	}
}

func TestFeature_UPM358_RollupFallsBackToUTC(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	insertUsageRow(t, pool, time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC), usagePerson, "model-a", "chat", 10, 7)
	for _, fn := range []func(context.Context) (string, error){nil, zone(""), zone("Not/AZone")} {
		if err := RollupUsageOnce(ctx, pool, fn, now, nil); err != nil {
			t.Fatal(err)
		}
		var day string
		if err := pool.QueryRow(ctx, `SELECT day::text FROM gateway.usage_daily`).Scan(&day); err != nil || day != "2026-11-17" {
			t.Fatalf("day = %q, %v; want 2026-11-17", day, err)
		}
	}
}

func TestFeature_UPM358_AnonymiseKeepsWorkspaceTotals(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	insertUsageRow(t, pool, day, usagePerson, "model-a", "chat", 10, 3)
	insertUsageRow(t, pool, day.Add(time.Hour), usagePerson, "model-a", "chat", 20, 4)
	insertUsageRow(t, pool, day.Add(24*time.Hour), usagePerson, "model-b", "task", 30, 5)
	insertUsageRow(t, pool, day, usageOther, "model-a", "chat", 40, 6)
	insertUsageRow(t, pool, day, AnonymousUserID, "model-a", "chat", 50, 7)
	insertUsageRow(t, pool, day, "", "model-a", "system", 60, 8)
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}

	workspace := func() [5]int64 {
		t.Helper()
		var v [5]int64
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM gateway.ledger),
				(SELECT sum(input_tokens + output_tokens)::bigint FROM gateway.ledger),
				(SELECT sum(cost_micro_usd)::bigint FROM gateway.ledger),
				(SELECT sum(input_tokens + output_tokens)::bigint FROM gateway.usage_daily),
				(SELECT COALESCE(sum(cost_micro_usd), 0)::bigint FROM gateway.usage_daily)`).Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
			t.Fatal(err)
		}
		return v
	}
	otherRows := func() string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT string_agg(id::text || ':' || input_tokens, ',' ORDER BY id) FROM gateway.ledger WHERE user_id = $1::uuid)
				|| '|' ||
				(SELECT string_agg(day::text || ':' || model || ':' || source || ':' || calls || ':' || input_tokens || ':' || cost_micro_usd, ',' ORDER BY day, model, source)
				 FROM gateway.usage_daily WHERE user_id = $1::uuid)`, usageOther).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	totalsBefore := workspace()
	otherBefore := otherRows()

	for range 2 {
		if err := AnonymiseUser(ctx, pool, usagePerson); err != nil {
			t.Fatal(err)
		}
		if n := countRows(t, pool, `
			SELECT (SELECT count(*) FROM gateway.ledger WHERE user_id = $1::uuid)
				+ (SELECT count(*) FROM gateway.usage_daily WHERE user_id = $1::uuid)`, usagePerson); n != 0 {
			t.Fatalf("rows with the person's id = %d, want 0", n)
		}
		if got := workspace(); got != totalsBefore {
			t.Fatalf("workspace totals = %v, want %v", got, totalsBefore)
		}
		if got := otherRows(); got != otherBefore {
			t.Fatalf("other person's rows = %q, want %q", got, otherBefore)
		}
	}

	var calls, tokens, cost int64
	if err := pool.QueryRow(ctx, `
		SELECT calls, input_tokens, cost_micro_usd FROM gateway.usage_daily
		WHERE day = '2026-11-10' AND user_id = $1::uuid AND model = 'model-a' AND source = 'chat'`,
		AnonymousUserID).Scan(&calls, &tokens, &cost); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || tokens != 80 || cost != 14 {
		t.Fatalf("merged anonymous row = %d calls, %d tokens, %d cost; want 3, 80, 14", calls, tokens, cost)
	}

	// The merged totals match a fresh roll-up of the anonymised ledger.
	merged := dailyMonthly(t, pool)
	if _, err := pool.Exec(ctx, `DELETE FROM gateway.usage_daily_state`); err != nil {
		t.Fatal(err)
	}
	snapshotUsageDaily(t, pool)
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	if got := dailyMonthly(t, pool); got["2026-11"] != merged["2026-11"] {
		t.Fatalf("re-rolled totals = %v, merged = %v", got, merged)
	}
	assertUsageDailyMatchesSnapshot(t, pool)

	for _, bad := range []string{"", "not-a-uuid", AnonymousUserID} {
		if err := AnonymiseUser(ctx, pool, bad); err == nil {
			t.Fatalf("AnonymiseUser(%q) succeeded, want an error", bad)
		}
	}
}

func TestFeature_UPM358_UsageDailyReadPolicy(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	insertUsageRow(t, pool, day, usagePerson, "model-a", "chat", 10, 3)
	insertUsageRow(t, pool, day, usageOther, "model-a", "chat", 10, 3)
	insertUsageRow(t, pool, day, "", "model-a", "system", 10, 3)
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	app := pgtest.App(t, pool)
	for _, tc := range []struct {
		allow string
		want  int64
	}{{"", 1}, {"usage:view", 3}} {
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`, usagePerson, tc.allow); err != nil {
			t.Fatal(err)
		}
		var got int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_daily`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
		if got != tc.want {
			t.Fatalf("allow %q sees %d rows, want %d", tc.allow, got, tc.want)
		}
	}
}

func TestFeature_UPM392_UsageDailyAndWatermarkAccess(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	insertUsageRow(t, pool, day, usagePerson, "model-a", "chat", 10, 3)
	insertUsageRow(t, pool, day, usageOther, "model-a", "chat", 10, 3)
	insertUsageRow(t, pool, day, "", "model-a", "system", 10, 3)
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	app := pgtest.App(t, pool)

	visible := func(allow string) int64 {
		t.Helper()
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`, usagePerson, allow); err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_daily`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := visible(""); n != 1 {
		t.Fatalf("rows a person sees = %d, want their own 1", n)
	}
	if n := visible("usage:view"); n != 3 {
		t.Fatalf("rows usage:view sees = %d, want 3", n)
	}
	stateTx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateTx.Rollback(ctx) }()
	if _, err := stateTx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, usagePerson); err != nil {
		t.Fatal(err)
	}
	var stateCount int
	if err := stateTx.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_daily_state`).Scan(&stateCount); err != nil || stateCount != 1 {
		t.Fatalf("watermark rows visible = %d, %v; want 1", stateCount, err)
	}
	noIdentityTx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = noIdentityTx.Rollback(ctx) }()
	if err := noIdentityTx.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_daily_state`).Scan(&stateCount); err != nil || stateCount != 0 {
		t.Fatalf("watermark rows visible without user id = %d, %v; want 0", stateCount, err)
	}

	for _, stmt := range []string{
		`INSERT INTO gateway.usage_daily (day, user_id, model, plan_kind, source) VALUES ('2026-11-11', '` + usagePerson + `', 'm', 'metered', 'chat')`,
		`UPDATE gateway.usage_daily SET calls = 0`,
		`DELETE FROM gateway.usage_daily`,
		`INSERT INTO gateway.usage_daily_state (rolled_through, time_zone) VALUES ('2026-11-19', 'UTC')`,
		`UPDATE gateway.usage_daily_state SET time_zone = 'Asia/Tokyo'`,
		`DELETE FROM gateway.usage_daily_state`,
	} {
		_, err := app.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: err = %v, want insufficient_privilege", stmt, err)
		}
	}
}

func TestFeature_UPM392_RollupMatchesLedgerGrain(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO gateway.ledger (
			at, route, link_position, model, plan_kind, failure_class, user_id,
			owner_team_id, agent_id, provider_account_id, input_tokens, output_tokens,
			cache_read, cache_write, cache_write_1h, reasoning, cost_micro_usd,
			list_cost_micro_usd, estimated, source, price_source
		) VALUES
			('2026-11-18 12:00Z','usage',1,'m','metered','', $1,'019a0000-0000-7000-8000-000000000401','019a0000-0000-7000-8000-000000000411','019a0000-0000-7000-8000-000000000421',10,2,1,4,2,3,7,NULL,false,'chat','provider'),
			('2026-11-18 13:00Z','usage',1,'m-free','metered','', $1,'019a0000-0000-7000-8000-000000000401','019a0000-0000-7000-8000-000000000411','019a0000-0000-7000-8000-000000000421',10,2,1,4,2,3,0,NULL,false,'chat','not_billed'),
			('2026-11-18 14:00Z','usage',1,'m','metered','timeout', $2,'019a0000-0000-7000-8000-000000000402','019a0000-0000-7000-8000-000000000412','019a0000-0000-7000-8000-000000000422',5,3,0,0,0,1,2,NULL,true,'task','estimate'),
			('2026-11-19 12:00Z','usage',1,'m','metered','', NULL,NULL,NULL,NULL,4,1,0,0,0,0,NULL,NULL,false,'system','unknown'),
			('2026-11-19 13:00Z','usage',1,'m','subscription','', $1,'019a0000-0000-7000-8000-000000000401','019a0000-0000-7000-8000-000000000411','019a0000-0000-7000-8000-000000000421',1,1,0,0,0,0,NULL,9,false,'chat',NULL),
			('2026-11-19 14:00Z','usage',1,'m','subscription','', $1,'019a0000-0000-7000-8000-000000000401','019a0000-0000-7000-8000-000000000411','019a0000-0000-7000-8000-000000000421',1,1,0,0,0,0,NULL,NULL,false,'memory',NULL),
			('2026-11-19 15:00Z','usage',1,'m','local','', $2,'019a0000-0000-7000-8000-000000000402','019a0000-0000-7000-8000-000000000412','019a0000-0000-7000-8000-000000000422',1,0,0,0,0,0,NULL,NULL,false,'task',NULL)`, usagePerson, usageOther); err != nil {
		t.Fatal(err)
	}
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	assertUsageDailyMatchesClosedLedger(t, pool, "UTC", time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	var unknown, free, nullList, unpriced int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE plan_kind='metered' AND calls=1 AND cost_micro_usd IS NULL),
			count(*) FILTER (WHERE plan_kind='metered' AND calls=1 AND cost_micro_usd=0),
			count(*) FILTER (WHERE plan_kind='subscription' AND list_cost_micro_usd IS NULL),
			count(*) FILTER (WHERE plan_kind='metered' AND calls=1 AND cost_micro_usd IS NULL AND unpriced_calls=1)
		FROM gateway.usage_daily`).Scan(&unknown, &free, &nullList, &unpriced); err != nil || unknown == 0 || free == 0 || nullList == 0 || unpriced == 0 {
		t.Fatalf("NULL/zero groups = unknown:%d free:%d null-list:%d unpriced:%d err:%v", unknown, free, nullList, unpriced, err)
	}
}

func TestFeature_UPM392_RollupWatermarkAndZoneRebuild(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	callAt := time.Date(2026, 11, 19, 23, 30, 0, 0, time.UTC)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	insertUsageRow(t, pool, callAt, usagePerson, "m", "chat", 1, 4)
	if err := RollupUsageOnce(ctx, pool, zone("Asia/Tokyo"), now, nil); err != nil {
		t.Fatal(err)
	}
	var day, tz string
	var through time.Time
	if err := pool.QueryRow(ctx, `SELECT day::text FROM gateway.usage_daily`).Scan(&day); err != nil || day != "2026-11-20" {
		t.Fatalf("Tokyo day = %q, %v; want 2026-11-20", day, err)
	}
	if err := pool.QueryRow(ctx, `SELECT rolled_through, time_zone FROM gateway.usage_daily_state WHERE id`).Scan(&through, &tz); err != nil || through.Format("2006-01-02") != "2026-11-21" || tz != "Asia/Tokyo" {
		t.Fatalf("watermark = %s %q, %v; want 2026-11-21 Asia/Tokyo", through.Format("2006-01-02"), tz, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE gateway.ledger SET source='task' WHERE model='m'`); err != nil {
		t.Fatal(err)
	}
	if err := RollupUsageOnce(ctx, pool, zone("Asia/Tokyo"), now, nil); err != nil {
		t.Fatal(err)
	}
	if rows := countRows(t, pool, `SELECT count(*) FROM gateway.usage_daily WHERE model='m' AND source='task'`); rows != 1 || countRows(t, pool, `SELECT count(*) FROM gateway.usage_daily WHERE model='m' AND source='chat'`) != 0 {
		t.Fatalf("recomputed grain rows = %d, want only the moved task row", rows)
	}
	var before string
	if err := pool.QueryRow(ctx, `SELECT string_agg(t::text, ',' ORDER BY t::text) FROM gateway.usage_daily t`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := RollupUsageOnce(ctx, pool, zone("Asia/Tokyo"), now, nil); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT string_agg(t::text, ',' ORDER BY t::text) FROM gateway.usage_daily t`).Scan(&after); err != nil || after != before {
		t.Fatalf("same-zone rerun changed rows or failed: %v", err)
	}
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT day::text FROM gateway.usage_daily`).Scan(&day); err != nil || day != "2026-11-19" {
		t.Fatalf("rebuilt UTC day = %q, %v; want 2026-11-19", day, err)
	}
	if err := pool.QueryRow(ctx, `SELECT time_zone FROM gateway.usage_daily_state WHERE id`).Scan(&tz); err != nil || tz != "UTC" {
		t.Fatalf("rebuilt time zone = %q, %v; want UTC", tz, err)
	}
}

func TestFeature_UPM398_RollupSurvivesClockGoingBack(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	day := func(offset int) time.Time {
		return time.Date(2026, time.November, 10+offset, 12, 0, 0, 0, time.UTC)
	}
	for offset := range 5 {
		insertUsageRow(t, pool, day(offset), usagePerson, "clock-back", "chat", offset+1, int64(offset+1))
	}
	rollup := func(offset int) {
		t.Helper()
		if err := RollupUsageOnce(ctx, pool, zone("UTC"), day(offset), nil); err != nil {
			t.Fatalf("roll-up at D+%d: %v", offset, err)
		}
		assertUsageDailyMatchesClosedLedger(t, pool, "UTC", time.Date(2026, time.November, 10+offset, 0, 0, 0, 0, time.UTC))
	}
	rollup(5)
	rollup(2)
	insertUsageRow(t, pool, day(3), usagePerson, "clock-back", "chat", 9, 9)
	rollup(6)
}

func TestFeature_UPM392_RollupGapFillAndFrozenLateRows(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	day := func(offset, hour, minute, second int) time.Time {
		return time.Date(2026, time.November, 10+offset, hour, minute, second, 0, loc)
	}
	if err := RollupUsageOnce(ctx, pool, zone("Europe/Berlin"), day(0, 12, 0, 0), nil); err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < 3; offset++ {
		insertUsageRow(t, pool, day(offset, 0, 0, 0), usagePerson, "boundary", "chat", 1, 1)
		insertUsageRow(t, pool, day(offset, 23, 59, 59), usagePerson, "boundary", "chat", 1, 1)
	}
	if err := RollupUsageOnce(ctx, pool, zone("Europe/Berlin"), day(5, 12, 0, 0), nil); err != nil {
		t.Fatal(err)
	}
	var mismatch int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			(SELECT (at AT TIME ZONE 'Europe/Berlin')::date AS usage_day, count(*) calls FROM gateway.ledger
			 WHERE model='boundary' GROUP BY 1
			 EXCEPT ALL
			 SELECT usage_daily.day, sum(calls)::bigint FROM gateway.usage_daily WHERE model='boundary' GROUP BY 1)
			UNION ALL
			(SELECT usage_daily.day, sum(calls)::bigint FROM gateway.usage_daily WHERE model='boundary' GROUP BY 1
			 EXCEPT ALL
			 SELECT (at AT TIME ZONE 'Europe/Berlin')::date, count(*) FROM gateway.ledger
			 WHERE model='boundary' GROUP BY 1)
		) differences`).Scan(&mismatch); err != nil || mismatch != 0 {
		t.Fatalf("gap-fill mismatch count = %d, %v; want 0", mismatch, err)
	}
	// D+5 is the last frozen day before the next run's recent window.
	if err := RollupUsageOnce(ctx, pool, zone("Europe/Berlin"), day(7, 12, 0, 0), nil); err != nil {
		t.Fatal(err)
	}
	insertUsageRow(t, pool, day(5, 12, 0, 0), usagePerson, "late-frozen", "chat", 1, 1)
	if err := RollupUsageOnce(ctx, pool, zone("Europe/Berlin"), day(8, 12, 0, 0), nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM gateway.usage_daily WHERE model='late-frozen'`); n != 0 {
		t.Fatalf("late row older than the recent window was rolled up: rows=%d", n)
	}
}

func TestFeature_UPM392_HavanaAmbiguousMidnightSurvivesRerun(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	// Havana repeated 00:00-00:59 on 2025-11-02; this instant is 00:30 in
	// the first occurrence, before the later standard-time midnight.
	insertUsageRow(t, pool, time.Date(2025, time.November, 2, 4, 30, 0, 0, time.UTC), usagePerson, "havana", "chat", 1, 1)
	now := time.Date(2025, time.November, 4, 12, 0, 0, 0, time.UTC)
	for range 2 {
		if err := RollupUsageOnce(ctx, pool, zone("America/Havana"), now, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, pool, `SELECT COALESCE(sum(calls),0) FROM gateway.usage_daily WHERE model='havana' AND day='2025-11-02'`); n != 1 {
		t.Fatalf("ambiguous-midnight calls = %d, want 1", n)
	}
}

func TestFeature_UPM392_AnonymiseMergesWidenedDaily(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO gateway.ledger (at,route,link_position,model,plan_kind,failure_class,user_id,
			input_tokens,output_tokens,cache_read,cache_write,cache_write_1h,reasoning,
			cost_micro_usd,list_cost_micro_usd,estimated,source,price_source)
		VALUES
			('2026-11-19 10:00Z','usage',1,'metered','metered','timeout', $1,3,1,4,5,2,3,11,NULL,true,'chat','estimate'),
			('2026-11-19 11:00Z','usage',1,'metered','metered','', $2,5,2,6,7,3,4,NULL,NULL,false,'chat','unknown'),
			('2026-11-19 12:00Z','usage',1,'sub','subscription','', $1,7,0,2,3,1,0,NULL,13,false,'task',NULL),
			('2026-11-19 13:00Z','usage',1,'sub','subscription','', $2,9,0,3,4,2,0,NULL,NULL,false,'task',NULL)`, usagePerson, AnonymousUserID); err != nil {
		t.Fatal(err)
	}
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	totals := func() string {
		t.Helper()
		var result string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
			'calls',COALESCE(sum(calls),0),'errors',COALESCE(sum(errors),0),
			'input',COALESCE(sum(input_tokens),0),'output',COALESCE(sum(output_tokens),0),
			'cache_read',COALESCE(sum(cache_read),0),'cache_write',COALESCE(sum(cache_write),0),
			'cache_write_1h',COALESCE(sum(cache_write_1h),0),'reasoning',COALESCE(sum(reasoning),0),
			'unpriced',COALESCE(sum(unpriced_calls),0),'estimated',COALESCE(bool_or(estimated),false),
			'cost',sum(cost_micro_usd),'list_cost',sum(list_cost_micro_usd))::text FROM gateway.usage_daily`).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	totalsBefore := totals()
	before := countRows(t, pool, `SELECT count(*) FROM gateway.usage_daily`)
	if err := AnonymiseUser(ctx, pool, usagePerson); err != nil {
		t.Fatal(err)
	}
	if got := totals(); got != totalsBefore {
		t.Fatalf("workspace daily totals after erasure = %s, want %s", got, totalsBefore)
	}
	if got := countRows(t, pool, `SELECT count(*) FROM gateway.usage_daily`); got != before-2 {
		t.Fatalf("daily rows after collision merge = %d, want %d", got, before-2)
	}
	var cost *int64
	var listCost *int64
	if err := pool.QueryRow(ctx, `SELECT cost_micro_usd FROM gateway.usage_daily WHERE model='metered' AND user_id=$1::uuid`, AnonymousUserID).Scan(&cost); err != nil || cost == nil || *cost != 11 {
		t.Fatalf("merged metered cost = %v, %v; want 11", cost, err)
	}
	if err := pool.QueryRow(ctx, `SELECT list_cost_micro_usd FROM gateway.usage_daily WHERE model='sub' AND user_id=$1::uuid`, AnonymousUserID).Scan(&listCost); err != nil || listCost == nil || *listCost != 13 {
		t.Fatalf("merged list cost = %v, %v; want 13", listCost, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM gateway.usage_daily_state`); err != nil {
		t.Fatal(err)
	}
	snapshotUsageDaily(t, pool)
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	assertUsageDailyMatchesSnapshot(t, pool)
	if got := totals(); got != totalsBefore {
		t.Fatalf("rebuilt workspace daily totals = %s, want %s", got, totalsBefore)
	}
	var rebuildCost *int64
	if err := pool.QueryRow(ctx, `SELECT cost_micro_usd FROM gateway.usage_daily WHERE model='metered' AND user_id=$1::uuid`, AnonymousUserID).Scan(&rebuildCost); err != nil || rebuildCost == nil || *rebuildCost != *cost {
		t.Fatalf("rebuilt cost = %v, %v; want %v", rebuildCost, err, cost)
	}
}

func assertUsageDailyMatchesClosedLedger(t *testing.T, pool *pgxpool.Pool, timeZone string, todayStart time.Time) {
	t.Helper()
	var mismatch int
	if err := pool.QueryRow(context.Background(), `
		WITH expected AS (
			SELECT (at AT TIME ZONE $1)::date usage_day, user_id, owner_team_id, agent_id, provider_account_id,
				model, plan_kind, source, count(*)::bigint calls,
				count(*) FILTER (WHERE failure_class <> '')::bigint errors,
				sum(input_tokens)::bigint input_tokens, sum(output_tokens)::bigint output_tokens,
				sum(cache_read)::bigint cache_read, sum(cache_write)::bigint cache_write,
				sum(cache_write_1h)::bigint cache_write_1h, sum(reasoning)::bigint reasoning,
				count(*) FILTER (WHERE plan_kind='metered' AND (estimated OR cost_micro_usd IS NULL))::bigint unpriced_calls,
				COALESCE(bool_or(estimated) FILTER (WHERE plan_kind='metered'), false) estimated,
				(sum(cost_micro_usd) FILTER (WHERE plan_kind='metered'))::bigint cost_micro_usd,
				(sum(list_cost_micro_usd) FILTER (WHERE plan_kind<>'metered'))::bigint list_cost_micro_usd
			FROM gateway.ledger WHERE at < $2 GROUP BY 1,2,3,4,5,6,7,8
		), actual AS (
			SELECT day,user_id,owner_team_id,agent_id,provider_account_id,model,plan_kind,source,
				calls,errors,input_tokens,output_tokens,cache_read,cache_write,cache_write_1h,
				reasoning,unpriced_calls,estimated,cost_micro_usd,list_cost_micro_usd FROM gateway.usage_daily
		)
		SELECT (SELECT count(*) FROM (SELECT * FROM expected EXCEPT ALL SELECT * FROM actual) x)
			+ (SELECT count(*) FROM (SELECT * FROM actual EXCEPT ALL SELECT * FROM expected) x)`, timeZone, todayStart).Scan(&mismatch); err != nil || mismatch != 0 {
		t.Fatalf("daily grain mismatch count = %d, %v; want 0", mismatch, err)
	}
}

func snapshotUsageDaily(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `CREATE TABLE gateway.usage_daily_snapshot AS SELECT * FROM gateway.usage_daily`); err != nil {
		t.Fatal(err)
	}
}

func assertUsageDailyMatchesSnapshot(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var mismatch int
	err := pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM (SELECT * FROM gateway.usage_daily_snapshot EXCEPT ALL SELECT * FROM gateway.usage_daily) x)
			+ (SELECT count(*) FROM (SELECT * FROM gateway.usage_daily EXCEPT ALL SELECT * FROM gateway.usage_daily_snapshot) x)`).Scan(&mismatch)
	if _, dropErr := pool.Exec(context.Background(), `DROP TABLE gateway.usage_daily_snapshot`); dropErr != nil {
		t.Fatal(dropErr)
	}
	if err != nil || mismatch != 0 {
		t.Fatalf("fresh rebuild content mismatch count = %d, %v; want 0", mismatch, err)
	}
}

func TestFeature_UPM392_BenchmarkMillionRowRollup(t *testing.T) {
	if os.Getenv("UPM392_BENCHMARK") != "1" {
		t.Skip("set UPM392_BENCHMARK=1 to run the one-million-row roll-up benchmark")
	}
	ctx := context.Background()
	pool := newUsageDB(t)
	todayDay := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	now := todayDay.Add(12 * time.Hour)
	firstDay := todayDay.AddDate(0, 0, -2)
	lowerBound := firstDay.AddDate(0, 0, -1)
	if _, err := pool.Exec(ctx, `
		INSERT INTO gateway.ledger (at,route,link_position,provider_account_id,model,plan_kind,
			failure_class,user_id,agent_id,owner_team_id,input_tokens,output_tokens,cost_micro_usd,source,price_source)
		SELECT '2026-08-22 00:00Z'::timestamptz + (i % 90) * interval '1 day' + (i % 86400) * interval '1 second',
			'usage',1,('00000000-0000-7000-8000-' || lpad((i % 100)::text, 12, '0'))::uuid,
			'model-' || (i % 20), 'metered','',
			('00000000-0000-7000-8000-' || lpad((i % 1000)::text, 12, '0'))::uuid,
			('00000000-0000-7000-8000-' || lpad((i % 50)::text, 12, '0'))::uuid,
			('00000000-0000-7000-8000-' || lpad((i % 10)::text, 12, '0'))::uuid,
			i % 1000, i % 500, 1, 'chat', 'provider'
		FROM generate_series(1, 1000000) i`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	fullDuration := time.Since(start)
	start = time.Now()
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	steadyDuration := time.Since(start)
	planTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = planTx.Rollback(ctx) }()
	if _, err := planTx.Exec(ctx, `DELETE FROM gateway.usage_daily WHERE day >= $1::date`, firstDay); err != nil {
		t.Fatal(err)
	}
	rows, err := planTx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) `+postgres.RollupIncrementalExplainQuery(),
		"UTC", todayDay, lowerBound, firstDay, todayDay)
	if err != nil {
		t.Fatal(err)
	}
	var plan, ledgerScan string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if strings.Contains(line, "Scan") && strings.Contains(line, "ledger") {
			ledgerScan = line
		}
		plan += line + "\n"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if ledgerScan == "" {
		t.Fatal("incremental ledger scan line missing from plan")
	}
	if err := planTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := RollupUsageOnce(ctx, pool, zone("Asia/Tokyo"), now, nil); err != nil {
		t.Fatal(err)
	}
	zoneDuration := time.Since(start)
	var statementTimeout string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&statementTimeout); err != nil {
		t.Fatal(err)
	}
	t.Logf("full rebuild 1,000,000 rows / 90 days: %s; steady-state: %s; zone-change rebuild: %s", fullDuration, steadyDuration, zoneDuration)
	t.Logf("incremental ledger scan: %s", ledgerScan)
	t.Logf("incremental INSERT plan:\n%s", plan)
	t.Logf("owner pool statement_timeout: %s (roll-up does not override it)", statementTimeout)
}
