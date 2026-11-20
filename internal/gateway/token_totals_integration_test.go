//go:build integration

package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
)

const upm389Run = "019a0000-0000-7000-8000-000000000401"

// insertUPM389TaskRow writes one task ledger row whose OpenAI-style usage
// carries reasoning inside output (completion_tokens already includes
// reasoning_tokens).
func insertUPM389TaskRow(t *testing.T, pool *pgxpool.Pool, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO gateway.ledger (
			at, route, link_position, model, plan_kind, failure_class, user_id, run_id,
			input_tokens, output_tokens, cache_read, cache_write, reasoning,
			usage_arrived, cost_micro_usd, source, price_source
		) VALUES (
			$1, 'upm389', 1, 'gpt-4o', 'metered', '', $3::uuid, $2::uuid,
			60, 100, 0, 0, 40, true, 250, 'task', 'provider'
		)`,
		at, upm389Run, usagePerson); err != nil {
		t.Fatal(err)
	}
}

func TestFeature_UPM389_TaskTokenTotalsCountReasoningOnce(t *testing.T) {
	pool := newUsageDB(t)
	insertUPM389TaskRow(t, pool, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))

	var tokens int64
	if err := pool.QueryRow(context.Background(),
		`SELECT tokens FROM gateway.task_tokens WHERE task_id = $1::uuid`, upm389Run).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if tokens != 160 {
		t.Fatalf("task tokens = %d, want 160 (input 60 + output 100, reasoning 40 not added)", tokens)
	}
	var reasoning int
	if err := pool.QueryRow(context.Background(),
		`SELECT reasoning FROM gateway.ledger WHERE run_id = $1::uuid`, upm389Run).Scan(&reasoning); err != nil {
		t.Fatal(err)
	}
	if reasoning != 40 {
		t.Fatalf("stored reasoning = %d, want 40 kept as its own figure", reasoning)
	}
}

func TestFeature_UPM389_MonthlyUsageTotalsCountReasoningOnce(t *testing.T) {
	ctx := context.Background()
	pool := newUsageDB(t)
	insertUPM389TaskRow(t, pool, time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))

	res, err := postgres.QueryMonthlyUsage(ctx, pool, "2026-11",
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if res.Totals.TotalTokens != 160 || res.Totals.OutputTokens != 100 {
		t.Fatalf("totals = %+v, want total 160 and output 100 (reasoning 40 not added)", res.Totals)
	}
	if res.Totals.ReasoningTokens != 40 {
		t.Fatalf("reasoning tokens = %d, want 40 kept as its own figure", res.Totals.ReasoningTokens)
	}
}
