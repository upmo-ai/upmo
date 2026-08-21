package domain

import "testing"

func rate(v float64) *float64 { return &v }

func TestCostUSDNullCases(t *testing.T) {
	price := Price{Version: "v1", InputPerMTok: rate(1), OutputPerMTok: rate(2)}
	usage := Usage{Arrived: true, Input: 1_000_000, Output: 1_000_000}

	if got := CostUSD(PlanSubscription, usage, price, true); got != nil {
		t.Fatalf("subscription = %v", *got)
	}
	if got := CostUSD(PlanLocal, usage, price, true); got != nil {
		t.Fatalf("local = %v", *got)
	}
	if got := CostUSD(PlanMetered, Usage{}, price, true); got != nil {
		t.Fatalf("no usage = %v", *got)
	}
	if got := CostUSD(PlanMetered, usage, Price{}, false); got != nil {
		t.Fatalf("no catalog = %v", *got)
	}
	if got := CostUSD(PlanMetered, Usage{Arrived: true, Input: 10}, Price{Version: "v1"}, true); got != nil {
		t.Fatalf("missing dimension = %v", *got)
	}
}

func TestCostUSDMetersWhenPriced(t *testing.T) {
	price := Price{
		Version:        "v1",
		InputPerMTok:   rate(1),
		OutputPerMTok:  rate(2),
		CacheReadMTok:  rate(0.1),
		CacheWriteMTok: rate(0.5),
	}
	got := CostUSD(PlanMetered, Usage{Arrived: true, Input: 1_000_000, Output: 500_000, CacheRead: 1_000_000, CacheWrite: 2_000_000}, price, true)
	if got == nil {
		t.Fatal("cost")
	}
	want := 1 + 1 + 0.1 + 1
	if *got != want {
		t.Fatalf("cost = %v want %v", *got, want)
	}
}

func TestCacheHitAlert(t *testing.T) {
	if CacheHitAlert(0, 0) {
		t.Fatal("empty window")
	}
	if CacheHitAlert(30, 70) {
		t.Fatal("70% should not alert")
	}
	if !CacheHitAlert(40, 60) {
		t.Fatal("60% should alert")
	}
	if CacheHitRate(30, 70) != 0.7 {
		t.Fatalf("rate = %v", CacheHitRate(30, 70))
	}
}

func TestRollupTokens(t *testing.T) {
	got := RollupTokens([]Usage{
		{Input: 10, Output: 2, CacheRead: 3, CacheWrite: 1, Reasoning: 4},
		{Input: 5},
	})
	if got != 25 {
		t.Fatalf("tokens = %d", got)
	}
}
