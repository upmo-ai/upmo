//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func usageWindow(now time.Time, days int, loc *time.Location) domain.UsageWindow {
	r := domain.UsageRange(fmt.Sprint(days))
	return domain.UsageWindowFor(r, now, loc)
}

func sortUsageScan(s *domain.UsageScan) {
	less := func(a, b domain.UsageCell) bool {
		return fmt.Sprintf("%s/%s/%s/%s/%s", a.Bucket.Format(time.RFC3339Nano), a.Key, a.Account, a.Model, a.PlanKind) <
			fmt.Sprintf("%s/%s/%s/%s/%s", b.Bucket.Format(time.RFC3339Nano), b.Key, b.Account, b.Model, b.PlanKind)
	}
	slices.SortFunc(s.Buckets, func(a, b domain.UsageCell) int {
		if less(a, b) {
			return -1
		}
		if less(b, a) {
			return 1
		}
		return 0
	})
	slices.SortFunc(s.Keyed, func(a, b domain.UsageCell) int {
		if less(a, b) {
			return -1
		}
		if less(b, a) {
			return 1
		}
		return 0
	})
}

func ledgerReference(t *testing.T, r usageRig, who app.Caller, w domain.UsageWindow, group domain.UsageGroup, personal bool) domain.UsageScan {
	t.Helper()
	ctx := context.Background()
	if _, err := r.owner.Exec(ctx, `DELETE FROM gateway.usage_daily_state`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = r.owner.Exec(ctx, `INSERT INTO gateway.usage_daily_state (id, rolled_through, time_zone) VALUES (true, $1::date, $2) ON CONFLICT (id) DO UPDATE SET rolled_through=EXCLUDED.rolled_through,time_zone=EXCLUDED.time_zone`, w.End.AddDate(0, 0, -1).Format("2006-01-02"), w.Zone.String())
	}()
	store := postgres.Usage{Pool: r.app}
	var got domain.UsageScan
	var err error
	if personal {
		got, err = store.PersonalUsage(ctx, who, w)
	} else {
		got, err = store.WorkspaceUsage(ctx, who, w, group)
	}
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFeature_UPM392_DailyReadMatchesLedger(t *testing.T) {
	ctx := context.Background()
	zones := []struct {
		name string
		loc  *time.Location
		now  time.Time
	}{
		{"UTC", time.UTC, time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)},
		{"Europe/Berlin", mustLocation(t, "Europe/Berlin"), time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)},
		{"America/Havana", mustLocation(t, "America/Havana"), time.Date(2025, 11, 3, 12, 0, 0, 0, time.UTC)},
	}
	for _, z := range zones {
		t.Run(z.name, func(t *testing.T) {
			r := newUsageRig(t)
			start := z.now.AddDate(0, 0, -95)
			for i := 0; i < 20; i++ {
				at := start.AddDate(0, 0, i*4).Add(time.Hour)
				if z.name == "America/Havana" && i == 19 {
					at = time.Date(2025, 11, 2, 4, 30, 0, 0, time.UTC)
				}
				user := usageAdmin
				if i%2 == 1 {
					user = usageMember
				}
				account := usageHouse
				if i%3 == 0 {
					account = usageMemberOwn
				}
				if i%5 == 0 {
					account = usageAdminSub
				}
				plan := domain.PlanMetered
				cost := micro(int64(i))
				list := (*int64)(nil)
				estimated := false
				if i%7 == 0 {
					cost = nil
				}
				if i%7 == 1 {
					cost = micro(0)
				}
				if i%7 == 2 {
					estimated = true
					cost = micro(1)
				}
				if i%6 == 0 {
					plan = domain.PlanSubscription
					cost = nil
					list = micro(10)
					estimated = false
				}
				if i%6 == 3 {
					plan = domain.PlanSubscription
					cost = nil
					estimated = false
				}
				r.call(t, usageCall{at: at, user: user, account: account, model: []string{"claude-x", "llama", "mystery"}[i%3], team: usageTeam, agent: usageAgent, in: i + 1, cost: cost, list: list, plan: plan, estimated: estimated, failure: func() string {
					if i%4 == 0 {
						return "timeout"
					}
					return ""
				}()})
			}
			if z.name == "Europe/Berlin" {
				for _, at := range []time.Time{
					time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC),
					time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC),
					time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC),
				} {
					r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "claude-x", in: 1, cost: micro(1)})
				}
			}
			// A system row and paired priced/unpriced calls exercise nullable cell money.
			special := z.now.AddDate(0, 0, -20)
			r.call(t, usageCall{at: special.Add(2 * time.Hour), account: usageHouse, model: "nil-only", in: 1, cost: nil})
			r.call(t, usageCall{at: special.Add(2 * time.Hour), user: usageAdmin, account: usageHouse, model: "claude-x", in: 1, cost: micro(0)})
			r.call(t, usageCall{at: special.Add(3 * time.Hour), user: usageAdmin, account: usageHouse, model: "mixed-cell", in: 1, cost: nil})
			r.call(t, usageCall{at: special.Add(3 * time.Hour), user: usageAdmin, account: usageHouse, model: "mixed-cell", in: 1, cost: micro(0)})
			r.call(t, usageCall{at: special.Add(4 * time.Hour), user: usageAdmin, account: usageAdminLocal, model: "local-model", plan: domain.PlanLocal, in: 1})
			if err := postgres.RollupUsage(ctx, r.owner, z.name, z.now); err != nil {
				t.Fatal(err)
			}
			store := postgres.Usage{Pool: r.app}
			caller := app.Caller{User: usageAdmin, Allow: "usage:view"}
			w90 := usageWindow(z.now, 90, z.loc)
			before, err := store.WorkspaceUsage(ctx, caller, w90, domain.GroupModel)
			if err != nil {
				t.Fatal(err)
			}
			var nilSpend, zeroSpend bool
			var mixedCell bool
			for _, c := range append(append([]domain.UsageCell{}, before.Buckets...), before.Keyed...) {
				nilSpend = nilSpend || (c.PlanKind == domain.PlanMetered && c.SpendMicroUSD == nil)
				zeroSpend = zeroSpend || (c.PlanKind == domain.PlanMetered && c.SpendMicroUSD != nil && *c.SpendMicroUSD == 0)
				mixedCell = mixedCell || (c.Model == "mixed-cell" && c.PlanKind == domain.PlanMetered && c.Unpriced == 1 && c.SpendMicroUSD != nil && *c.SpendMicroUSD == 0)
			}
			if !nilSpend || !zeroSpend || !mixedCell {
				t.Fatalf("metered nullable money: nil=%v zero=%v mixed-cell=%v cells=%+v", nilSpend, zeroSpend, mixedCell, before.Keyed)
			}
			if _, err := r.owner.Exec(ctx, `UPDATE gateway.usage_daily SET calls=calls+1 WHERE ctid=(SELECT ctid FROM gateway.usage_daily WHERE day >= $1::date AND day <= $2::date ORDER BY day LIMIT 1)`, w90.Start.In(z.loc).Format("2006-01-02"), w90.End.AddDate(0, 0, -4).In(z.loc).Format("2006-01-02")); err != nil {
				t.Fatal(err)
			}
			after, err := store.WorkspaceUsage(ctx, caller, w90, domain.GroupModel)
			if err != nil {
				t.Fatal(err)
			}
			total := func(s domain.UsageScan) int64 {
				var n int64
				for _, c := range s.Keyed {
					n += c.Requests
				}
				return n
			}
			if delta := total(after) - total(before); delta != 1 {
				t.Fatalf("daily anti-vacuous mutation changed requests by %d, want 1", delta)
			}
			if _, err := r.owner.Exec(ctx, `UPDATE gateway.usage_daily SET calls=calls-1 WHERE ctid=(SELECT ctid FROM gateway.usage_daily WHERE day >= $1::date AND day <= $2::date ORDER BY day LIMIT 1)`, w90.Start.In(z.loc).Format("2006-01-02"), w90.End.AddDate(0, 0, -4).In(z.loc).Format("2006-01-02")); err != nil {
				t.Fatal(err)
			}
			for _, n := range []int{0, 7, 30, 90} {
				var w domain.UsageWindow
				if n == 0 {
					w = domain.UsageWindowFor(domain.UsageToday, z.now, z.loc)
				} else {
					w = usageWindow(z.now, n, z.loc)
				}
				for _, user := range []string{usageAdmin, usageMember} {
					who := app.Caller{User: user}
					got, err := store.PersonalUsage(ctx, who, w)
					if err != nil {
						t.Fatal(err)
					}
					want := ledgerReference(t, r, who, w, "", true)
					sortUsageScan(&got)
					sortUsageScan(&want)
					if !reflect.DeepEqual(got.Buckets, want.Buckets) || !reflect.DeepEqual(got.Keyed, want.Keyed) {
						t.Fatalf("personal %s/%d differs from ledger", user, n)
					}
					if err := postgres.RollupUsage(ctx, r.owner, z.name, z.now); err != nil {
						t.Fatal(err)
					}
				}
				for _, g := range []domain.UsageGroup{domain.GroupPerson, domain.GroupTeam, domain.GroupAgent, domain.GroupModel, domain.GroupProvider, domain.GroupSource} {
					got, err := store.WorkspaceUsage(ctx, caller, w, g)
					if err != nil {
						t.Fatal(err)
					}
					want := ledgerReference(t, r, caller, w, g, false)
					sortUsageScan(&got)
					sortUsageScan(&want)
					if !reflect.DeepEqual(got.Buckets, want.Buckets) || !reflect.DeepEqual(got.Keyed, want.Keyed) {
						t.Fatalf("workspace %s/%d/%s differs from ledger", z.name, n, g)
					}
					if err := postgres.RollupUsage(ctx, r.owner, z.name, z.now); err != nil {
						t.Fatal(err)
					}
				}
			}
			if z.name == "America/Havana" {
				havanaNow := time.Date(2025, 11, 12, 12, 0, 0, 0, time.UTC)
				if err := postgres.RollupUsage(ctx, r.owner, z.name, havanaNow); err != nil {
					t.Fatal(err)
				}
				firstDay := usageWindow(havanaNow, 11, z.loc)
				if firstDay.Start.In(z.loc).Format("2006-01-02") != "2025-11-02" {
					t.Fatalf("Havana first day=%s, want 2025-11-02", firstDay.Start.In(z.loc).Format("2006-01-02"))
				}
				for _, read := range []struct {
					user  bool
					group domain.UsageGroup
				}{{true, ""}, {false, domain.GroupModel}} {
					var got, want domain.UsageScan
					var err error
					if read.user {
						who := app.Caller{User: usageAdmin}
						got, err = store.PersonalUsage(ctx, who, firstDay)
						if err == nil {
							want = ledgerReference(t, r, who, firstDay, "", true)
						}
					} else {
						got, err = store.WorkspaceUsage(ctx, caller, firstDay, read.group)
						if err == nil {
							want = ledgerReference(t, r, caller, firstDay, read.group, false)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					sortUsageScan(&got)
					sortUsageScan(&want)
					if !reflect.DeepEqual(got.Buckets, want.Buckets) || !reflect.DeepEqual(got.Keyed, want.Keyed) {
						t.Fatalf("Havana ambiguous first-day read differs from ledger (personal=%v)", read.user)
					}
				}
			}
		})
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestFeature_UPM392_FreshnessAndZoneFallback(t *testing.T) {
	ctx := context.Background()
	r := newUsageRig(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	loc := mustLocation(t, "UTC")
	w := usageWindow(now, 90, loc)
	who := app.Caller{User: usageAdmin, Allow: "usage:view"}
	store := postgres.Usage{Pool: r.app}
	r.call(t, usageCall{at: now.AddDate(0, 0, -8), user: usageAdmin, account: usageHouse, model: "claude-x", in: 5, cost: micro(50)})
	if err := postgres.RollupUsage(ctx, r.owner, "UTC", now); err != nil {
		t.Fatal(err)
	}
	// No state means an injected daily value must not be visible.
	if _, err := r.owner.Exec(ctx, `DELETE FROM gateway.usage_daily_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_daily(day,user_id,model,plan_kind,source,calls,cost_micro_usd) VALUES ($1::date,$2::uuid,'bogus','metered','chat',99,999)`, now.AddDate(0, 0, -8).Format("2006-01-02"), usageAdmin); err != nil {
		t.Fatal(err)
	}
	got, err := store.WorkspaceUsage(ctx, who, w, domain.GroupModel)
	if err != nil {
		t.Fatal(err)
	}
	want := ledgerReference(t, r, who, w, domain.GroupModel, false)
	sortUsageScan(&got)
	sortUsageScan(&want)
	if !reflect.DeepEqual(got.Buckets, want.Buckets) || !reflect.DeepEqual(got.Keyed, want.Keyed) {
		t.Fatal("no-state fallback differs from ledger")
	}
	if len(got.Keyed) != 1 || got.Keyed[0].Requests != 1 || got.Keyed[0].Model != "claude-x" {
		t.Fatalf("no-state fallback included bogus daily data: %+v", got.Keyed)
	}
	// A watermark older than today-3 excludes later daily rows.
	if _, err := r.owner.Exec(ctx, `DELETE FROM gateway.usage_daily WHERE model='bogus'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_daily_state(id,rolled_through,time_zone) VALUES(true,$1::date,'UTC') ON CONFLICT(id) DO UPDATE SET rolled_through=EXCLUDED.rolled_through,time_zone='UTC'`, now.AddDate(0, 0, -10).Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_daily(day,user_id,model,plan_kind,source,calls,cost_micro_usd) VALUES($1::date,$2::uuid,'bogus','metered','chat',99,999)`, now.AddDate(0, 0, -5).Format("2006-01-02"), usageAdmin); err != nil {
		t.Fatal(err)
	}
	got, err = store.WorkspaceUsage(ctx, who, w, domain.GroupModel)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Keyed {
		if c.Model == "bogus" {
			t.Fatalf("lagging watermark included later bogus daily row")
		}
	}
	want = ledgerReference(t, r, who, w, domain.GroupModel, false)
	sortUsageScan(&got)
	sortUsageScan(&want)
	if !reflect.DeepEqual(got.Buckets, want.Buckets) || !reflect.DeepEqual(got.Keyed, want.Keyed) {
		t.Fatal("lagging watermark read differs from ledger")
	}
	if _, err := r.owner.Exec(ctx, `DELETE FROM gateway.usage_daily WHERE model='bogus'`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.RollupUsage(ctx, r.owner, "UTC", now); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.AddDate(0, 0, -1).Truncate(24 * time.Hour).Add(23*time.Hour + 59*time.Minute + 59*time.Second), now.AddDate(0, 0, -2).Truncate(24 * time.Hour).Add(23*time.Hour + 59*time.Minute + 59*time.Second), time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)} {
		r.call(t, usageCall{at: at, user: usageAdmin, account: usageHouse, model: "late-settle", in: 1, cost: micro(1)})
	}
	late, err := store.WorkspaceUsage(ctx, who, w, domain.GroupModel)
	if err != nil {
		t.Fatal(err)
	}
	want = ledgerReference(t, r, who, w, domain.GroupModel, false)
	sortUsageScan(&late)
	sortUsageScan(&want)
	if !reflect.DeepEqual(late.Buckets, want.Buckets) || !reflect.DeepEqual(late.Keyed, want.Keyed) {
		t.Fatalf("late-settle read differs from ledger: got=%+v want=%+v", late.Keyed, want.Keyed)
	}
	var lateRequests int64
	for _, c := range late.Keyed {
		if c.Model == "late-settle" {
			lateRequests += c.Requests
		}
	}
	if lateRequests != 3 {
		t.Fatalf("late-settle requests=%d, want 3", lateRequests)
	}
	// A zone mismatch forces the ledger-only path.
	if err := postgres.RollupUsage(ctx, r.owner, "UTC", now); err != nil {
		t.Fatal(err)
	}
	r.call(t, usageCall{at: time.Date(2026, 11, 11, 23, 30, 0, 0, time.UTC), user: usageAdmin, account: usageHouse, model: "zone-boundary", in: 1, cost: micro(1)})
	berlin := mustLocation(t, "Europe/Berlin")
	wb := usageWindow(now, 90, berlin)
	got, err = store.WorkspaceUsage(ctx, who, wb, domain.GroupModel)
	if err != nil {
		t.Fatal(err)
	}
	want = ledgerReference(t, r, who, wb, domain.GroupModel, false)
	sortUsageScan(&got)
	sortUsageScan(&want)
	if !reflect.DeepEqual(got.Buckets, want.Buckets) || !reflect.DeepEqual(got.Keyed, want.Keyed) {
		t.Fatal("zone mismatch did not match ledger")
	}
}

func TestFeature_UPM392_Benchmark90DayMillionRows(t *testing.T) {
	if os.Getenv("UPM392_BENCHMARK") != "1" {
		t.Skip("set UPM392_BENCHMARK=1")
	}
	ctx := context.Background()
	r := newUsageRig(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.ledger(at,route,link_position,provider_account_id,model,plan_kind,failure_class,user_id,agent_id,owner_team_id,input_tokens,output_tokens,cost_micro_usd,source,price_source) SELECT '2026-08-22 00:00Z'::timestamptz+(i%90)*interval '1 day'+(i%86400)*interval '1 second','usage',1,('00000000-0000-7000-8000-'||lpad((i%100)::text,12,'0'))::uuid,'model-'||(i%20),'metered','',('00000000-0000-7000-8000-'||lpad((i%1000)::text,12,'0'))::uuid,('00000000-0000-7000-8000-'||lpad((i%50)::text,12,'0'))::uuid,('00000000-0000-7000-8000-'||lpad((i%10)::text,12,'0'))::uuid,i%1000,i%500,1,'chat','provider' FROM generate_series(1,1000000) i`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.RollupUsage(ctx, r.owner, "UTC", now); err != nil {
		t.Fatal(err)
	}
	store := postgres.Usage{Pool: r.app}
	caller := app.Caller{User: usageAdmin, Allow: "usage:view"}
	w := usageWindow(now, 90, time.UTC)
	groups := []domain.UsageGroup{domain.GroupPerson, domain.GroupModel}
	for _, g := range groups {
		times := make([]time.Duration, 0, 10)
		for i := 0; i < 12; i++ {
			start := time.Now()
			_, err := store.WorkspaceUsage(ctx, caller, w, g)
			if err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			if i >= 2 {
				times = append(times, elapsed)
			}
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		median := times[5]
		p95 := times[9]
		t.Logf("%s hybrid median=%s p95=%s", g, median, p95)
		if median >= 300*time.Millisecond {
			t.Errorf("%s median %s exceeds 300ms", g, median)
		}
	}
	start := time.Now()
	if _, err := store.PersonalUsage(ctx, app.Caller{User: usageAdmin}, w); err != nil {
		t.Fatal(err)
	}
	t.Logf("personal 90-day hybrid=%s", time.Since(start))
	for _, g := range groups {
		_, err := r.owner.Exec(ctx, `DELETE FROM gateway.usage_daily_state`)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = store.WorkspaceUsage(ctx, caller, w, g)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s ledger-only=%s", g, time.Since(start))
		if err = postgres.RollupUsage(ctx, r.owner, "UTC", now); err != nil {
			t.Fatal(err)
		}
	}
	var plan strings.Builder
	rows, err := r.owner.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) `+postgres.UsageHybridScanExplainQuery(), "day", "UTC", w.Start, w.End, w.Start.Format("2006-01-02"), w.End.AddDate(0, 0, -4).Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()
	if strings.Contains(plan.String(), "Seq Scan on usage_daily") {
		t.Fatal("hybrid workspace read did a sequential scan of usage_daily")
	}
	t.Logf("hybrid explain:\n%s", plan.String())
}
