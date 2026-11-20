//go:build integration

package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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
			sum(cache_write)::bigint, sum(reasoning)::bigint, sum(cost_micro_usd)::bigint
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
	if err := pool.QueryRow(ctx, `SELECT sum(cost_micro_usd)::bigint FROM gateway.usage_daily`).Scan(&cost); err != nil || cost != 14 {
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
				(SELECT sum(cost_micro_usd)::bigint FROM gateway.usage_daily)`).Scan(&v[0], &v[1], &v[2], &v[3], &v[4]); err != nil {
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
	if err := RollupUsageOnce(ctx, pool, zone("UTC"), now, nil); err != nil {
		t.Fatal(err)
	}
	if got := dailyMonthly(t, pool); got["2026-11"] != merged["2026-11"] {
		t.Fatalf("re-rolled totals = %v, merged = %v", got, merged)
	}

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

	for _, stmt := range []string{
		`INSERT INTO gateway.usage_daily (day, user_id, model, source) VALUES ('2026-11-11', '` + usagePerson + `', 'm', 'chat')`,
		`UPDATE gateway.usage_daily SET calls = 0`,
		`DELETE FROM gateway.usage_daily`,
	} {
		_, err := app.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: err = %v, want insufficient_privilege", stmt, err)
		}
	}
}
