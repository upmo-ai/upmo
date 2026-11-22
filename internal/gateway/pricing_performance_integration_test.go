//go:build integration

package gateway

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// This intentionally heavy measurement runs only when UPMO_PRICING_PERF=1.
func TestUPM388PricingAndUsagePerformance(t *testing.T) {
	if os.Getenv("UPMO_PRICING_PERF") != "1" {
		t.Skip("set UPMO_PRICING_PERF=1 to seed and measure the million-row rig")
	}
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'f'))
	store := postgres.Store{Pool: r.app}
	for i := 0; i < 2000; i++ {
		model := fmt.Sprintf("perf-model-%04d", i)
		_, err := r.owner.Exec(ctx, `INSERT INTO gateway.prices(vendor,model,version,input_usd_per_mtok,output_usd_per_mtok,cache_write_usd_per_mtok) VALUES('openai',$1,'perf',1,2,2)`, model)
		if err != nil {
			t.Fatal(err)
		}
		if i < 300 {
			_, err = r.owner.Exec(ctx, `INSERT INTO gateway.price_tiers(vendor,model,context_min_tokens,input_usd_per_mtok,output_usd_per_mtok,cache_write_usd_per_mtok) VALUES('openai',$1,200000,2,3,3)`, model)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	user := uuid.NewString()
	var account string
	if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.provider_accounts(name,adapter,credential_ref,plan_kind,house,provider,visibility) VALUES('perf','fake','perf','subscription',true,'openai','everyone') RETURNING id`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	// Lookup uses an account row so its normal serving path, including tiers, is measured.
	lookup := make([]float64, 500)
	for i := range lookup {
		start := time.Now()
		if _, err := store.LookupCallPricing(ctx, account, "openai", fmt.Sprintf("perf-model-%04d", i%2000)); err != nil {
			t.Fatal(err)
		}
		lookup[i] = float64(time.Since(start).Microseconds()) / 1000
	}
	logPercentiles(t, "LookupCallPricing 500", lookup)
	if p95 := percentile(lookup, .95); p95 >= 5 {
		t.Fatalf("LookupCallPricing p95=%.3fms, want <5ms", p95)
	}
	highest := make([]float64, 500)
	for i := range highest {
		start := time.Now()
		if _, _, err := store.HighestRate(ctx, "openai"); err != nil {
			t.Fatal(err)
		}
		highest[i] = float64(time.Since(start).Microseconds()) / 1000
	}
	logPercentiles(t, "HighestRate 500", highest)

	// Seed representative ledger rows over the prior 90 days. Every twentieth row has
	// large context, every twentieth includes 1h cache writes, and users rotate.
	_, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.ledger(at,route,link_position,provider_account_id,model,plan_kind,failure_class,user_id,input_tokens,output_tokens,cache_write,cache_write_1h,usage_arrived,source,cost_micro_usd,price_source,estimated,list_cost_micro_usd)
		SELECT now() - (g % 90) * interval '1 day', 'perf', 1, $1::uuid,
			CASE WHEN g % 2 = 0 THEN 'perf-model-0001' ELSE 'unknown-perf' END,
			(CASE g % 3 WHEN 0 THEN 'metered' WHEN 1 THEN 'subscription' ELSE 'local' END), '',
			CASE WHEN g % 8 = 0 THEN $2::uuid ELSE (md5($2::text || (g % 8)::text))::uuid END,
			CASE WHEN g % 20 = 0 THEN 250000 ELSE 1000 END, 100, 100,
			CASE WHEN g % 20 = 0 THEN 20 ELSE 0 END, true, 'chat',
			CASE WHEN g % 3 = 0 THEN 100 ELSE NULL END,
			CASE WHEN g % 3 = 0 THEN 'models_dev' ELSE NULL END, false,
			CASE WHEN g % 3 = 0 OR g % 2 <> 0 THEN NULL ELSE div(
				(CASE WHEN g % 20 = 0 THEN 250000 ELSE 1000 END) * CASE WHEN g % 20 = 0 THEN 2 ELSE 1 END
				+ 100 * 2 + (100 - CASE WHEN g % 20 = 0 THEN 20 ELSE 0 END) * CASE WHEN g % 20 = 0 THEN 3 ELSE 2 END
				+ CASE WHEN g % 20 = 0 THEN 20 * 4 ELSE 0 END + 500000, 1000000) END
		FROM generate_series(1,1000000) AS g`, account, user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `ANALYZE gateway.ledger`); err != nil {
		t.Fatal(err)
	}
	appPool := postgres.Usage{Pool: r.app}
	admin := app.Caller{User: user, Allow: domain.UsageViewPermission}
	member := app.Caller{User: user}
	now := time.Now()
	for _, days := range []int{30, 90} {
		w := domain.UsageWindow{Start: now.AddDate(0, 0, -days), End: now, Unit: "day", Zone: time.UTC}
		for _, tc := range []struct {
			name string
			run  func() error
		}{
			{"PersonalUsage", func() error { _, e := appPool.PersonalUsage(ctx, member, w); return e }},
			{"WorkspaceUsage", func() error { _, e := appPool.WorkspaceUsage(ctx, admin, w, domain.GroupModel); return e }},
		} {
			ms := make([]float64, 5)
			for i := range ms {
				start := time.Now()
				if err := tc.run(); err != nil {
					t.Fatal(err)
				}
				ms[i] = float64(time.Since(start).Microseconds()) / 1000
			}
			logPercentiles(t, fmt.Sprintf("%s %dd 5", tc.name, days), ms)
		}
		scan, err := appPool.PersonalUsage(ctx, member, w)
		if err != nil || len(scan.Buckets) == 0 {
			t.Fatalf("PersonalUsage %dd returned no seeded rows: buckets=%d err=%v", days, len(scan.Buckets), err)
		}
	}
	// Show the actual planner's tier lookup path under the application role.
	planTx, err := r.app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = planTx.Rollback(ctx) }()
	if _, err := planTx.Exec(ctx, `SELECT set_config('upmo.user_id',$1,true),set_config('upmo.allow',$2,true),set_config('upmo.teams','{}',true)`, user, domain.UsageViewPermission); err != nil {
		t.Fatal(err)
	}
	if _, err := planTx.Exec(ctx, `SELECT set_config('work_mem', $1, true)`, "64MB"); err != nil {
		t.Fatal(err)
	}
	rows, err := planTx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) `+postgres.UsageScanExplainQuery(), "day", "UTC", now.AddDate(0, 0, -90), now)
	if err != nil {
		t.Fatalf("explain plan: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("EXPLAIN (ANALYZE, BUFFERS):\n%s", plan.String())
}

func logPercentiles(t *testing.T, label string, values []float64) {
	t.Helper()
	sort.Float64s(values)
	p50 := percentile(values, .50)
	p95 := percentile(values, .95)
	t.Logf("%s p50=%.3fms p95=%.3fms", label, p50, p95)
}

func percentile(values []float64, p float64) float64 {
	sort.Float64s(values)
	return values[int(float64(len(values)-1)*p)]
}
