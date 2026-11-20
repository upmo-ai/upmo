package domain

import (
	"testing"

	"github.com/upmo-ai/upmo/internal/kernel"
)

func microRate(v int64) *int64 { return &v }

func TestMeteredCostNullCases(t *testing.T) {
	price := Price{Version: "v1", InputMicroUSDPerMTok: microRate(kernel.MicroUSDPerDollar), OutputMicroUSDPerMTok: microRate(2 * kernel.MicroUSDPerDollar)}
	usage := Usage{Arrived: true, Input: 1_000_000, Output: 1_000_000}

	for _, plan := range []string{PlanSubscription, PlanLocal} {
		if got, estimated, source := MeteredCost(plan, usage, price, true, 0, 0, 0, false); got != nil || estimated || source != "" {
			t.Fatalf("%s = %v %t %q", plan, got, estimated, source)
		}
	}
	if got, estimated, source := MeteredCost(PlanMetered, Usage{}, price, true, 0, 0, 0, true); got == nil || *got != 0 || estimated || source != "not_billed" {
		t.Fatalf("known zero = %v %t %q", got, estimated, source)
	}
}

func TestMeteredCostUsesIntegerMicroUSD(t *testing.T) {
	price := Price{
		Version:                   "v1",
		InputMicroUSDPerMTok:      microRate(1_000_000),
		OutputMicroUSDPerMTok:     microRate(2_000_000),
		CacheReadMicroUSDPerMTok:  microRate(100_000),
		CacheWriteMicroUSDPerMTok: microRate(500_000),
	}
	got, estimated, source := MeteredCost(PlanMetered, Usage{Arrived: true, Input: 1_000_000, Output: 500_000, CacheRead: 1_000_000, CacheWrite: 2_000_000}, price, true, 0, 0, 0, false)
	if got == nil || *got != 3_100_000 || estimated || source != "models_dev" {
		t.Fatalf("cost = %v %t %q", got, estimated, source)
	}

	halfUp := Price{Version: "v1", InputMicroUSDPerMTok: microRate(1_500_000)}
	if got, estimated, source := MeteredCost(PlanMetered, Usage{Arrived: true, Input: 1}, halfUp, true, 0, 0, 0, false); got == nil || *got != 2 || estimated || source != "models_dev" {
		t.Fatalf("half-up 1.5 = %v %t %q, want 2", got, estimated, source)
	}
	belowHalf := Price{Version: "v1", InputMicroUSDPerMTok: microRate(1_499_999)}
	if got, estimated, source := MeteredCost(PlanMetered, Usage{Arrived: true, Input: 1}, belowHalf, true, 0, 0, 0, false); got == nil || *got != 1 || estimated || source != "models_dev" {
		t.Fatalf("half-up 1.499999 = %v %t %q, want 1", got, estimated, source)
	}
}

func TestNeedsEstimate(t *testing.T) {
	price := Price{Version: "v1", InputMicroUSDPerMTok: microRate(1_000_000)}
	arrived := Usage{Arrived: true, Input: 10}
	if NeedsEstimate(PlanSubscription, Usage{}, price, false, false) {
		t.Fatal("subscription should not estimate")
	}
	if NeedsEstimate(PlanLocal, Usage{}, price, false, false) {
		t.Fatal("local should not estimate")
	}
	if NeedsEstimate(PlanMetered, Usage{}, price, false, true) {
		t.Fatal("knownZero should not estimate")
	}
	if NeedsEstimate(PlanMetered, arrived, price, true, false) {
		t.Fatal("fully priced call should not estimate")
	}
	if !NeedsEstimate(PlanMetered, arrived, Price{}, false, false) {
		t.Fatal("unpriced model should estimate")
	}
	if !NeedsEstimate(PlanMetered, Usage{Input: 10}, price, true, false) {
		t.Fatal("missing usage should estimate")
	}
	if !NeedsEstimate(PlanMetered, Usage{Arrived: true, Input: 10, Output: 5}, price, true, false) {
		t.Fatal("missing output rate should estimate")
	}
}

func TestFeature_UPM353_UnpricedMeteredCostIsAPositiveEstimate(t *testing.T) {
	const vendorRate = int64(75_000_000)
	cost, estimated, source := MeteredCost(PlanMetered, Usage{}, Price{}, false, vendorRate, 17, 0, false)
	if cost == nil || *cost <= 0 || !estimated || source != "estimate" {
		t.Fatalf("unpriced metered cost = %v %t %q", cost, estimated, source)
	}
	if *cost != 375 {
		t.Fatalf("estimate = %d, want 375", *cost)
	}

	withOutputBytes, estimated, source := MeteredCost(PlanMetered, Usage{}, Price{}, false, vendorRate, 5, 8, false)
	if withOutputBytes == nil || !estimated || source != "estimate" || *withOutputBytes != 300 {
		t.Fatalf("estimate with output bytes = %v %t %q, want 300", withOutputBytes, estimated, source)
	}

	withOutputTokens, estimated, source := MeteredCost(PlanMetered, Usage{Output: 2}, Price{}, false, vendorRate, 5, 8, false)
	if withOutputTokens == nil || !estimated || source != "estimate" || *withOutputTokens != 450 {
		t.Fatalf("estimate with output bytes and tokens = %v %t %q, want 450", withOutputTokens, estimated, source)
	}

	arrivedUnpriced, estimated, source := MeteredCost(PlanMetered, Usage{Arrived: true, Input: 10, Output: 5}, Price{}, false, vendorRate, 5, 100, false)
	if arrivedUnpriced == nil || !estimated || source != "estimate" || *arrivedUnpriced != 1125 {
		t.Fatalf("arrived unpriced estimate = %v %t %q, want 1125", arrivedUnpriced, estimated, source)
	}

	unknownCost, unknownEst, unknownSrc := MeteredCost(PlanMetered, Usage{Arrived: true, Input: 10, Output: 5}, Price{}, false, 0, 5, 100, false)
	if unknownCost != nil || unknownEst || unknownSrc != "unknown" {
		t.Fatalf("unknown metered cost = %v %t %q, want nil false unknown", unknownCost, unknownEst, unknownSrc)
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

func TestUSDFromMicro(t *testing.T) {
	if USDFromMicro(nil) != nil {
		t.Fatal("expected nil for nil cost")
	}
	v := int64(1_234_567)
	got := USDFromMicro(&v)
	if got == nil || *got != 1.234567 {
		t.Fatalf("USDFromMicro(%d) = %v", v, got)
	}
}
