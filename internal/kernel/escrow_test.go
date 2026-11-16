package kernel

import (
	"errors"
	"testing"
)

func int64Ptr(v int64) *int64 { return &v }

func TestCheckRootBudget(t *testing.T) {
	if err := CheckRootBudget(1, DefaultMaxBudgetMicroUSD); err != nil {
		t.Fatal(err)
	}
	if err := CheckRootBudget(DefaultMaxBudgetMicroUSD, DefaultMaxBudgetMicroUSD); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []int64{0, -1, DefaultMaxBudgetMicroUSD + 1} {
		if err := CheckRootBudget(bad, DefaultMaxBudgetMicroUSD); !errors.Is(err, ErrBudget) {
			t.Fatalf("CheckRootBudget(%d) = %v, want %v", bad, err, ErrBudget)
		}
	}
}

func TestRemainingAndAvailable(t *testing.T) {
	if got := Remaining(5_000_000, 1_250_000); got != 3_750_000 {
		t.Fatalf("Remaining = %d, want 3750000", got)
	}
	if got := Remaining(1_000, 1_500); got != 0 {
		t.Fatalf("Remaining over = %d, want 0", got)
	}
	e := Escrow{BudgetMicroUSD: 10 * MicroUSDPerDollar, HeldMicroUSD: 3 * MicroUSDPerDollar, SpentMicroUSD: 2 * MicroUSDPerDollar}
	if got := e.Available(); got != 5*MicroUSDPerDollar {
		t.Fatalf("Available = %d, want %d", got, 5*MicroUSDPerDollar)
	}
	e.Paused = true
	if got := e.Available(); got != 0 {
		t.Fatalf("Available while paused = %d, want 0", got)
	}
}

func TestAllocateAndReleaseChildEscrow(t *testing.T) {
	parent := Escrow{BudgetMicroUSD: 100 * MicroUSDPerDollar, SpentMicroUSD: 20 * MicroUSDPerDollar}
	nextParent, child, err := AllocateChild(parent, 50*MicroUSDPerDollar, DefaultMaxBudgetMicroUSD)
	if err != nil {
		t.Fatal(err)
	}
	if nextParent.HeldMicroUSD != 50*MicroUSDPerDollar || child.BudgetMicroUSD != 50*MicroUSDPerDollar {
		t.Fatalf("parent=%+v child=%+v", nextParent, child)
	}
	if _, _, err := AllocateChild(nextParent, 31*MicroUSDPerDollar, DefaultMaxBudgetMicroUSD); !errors.Is(err, ErrBudget) {
		t.Fatalf("over-allocation err = %v, want %v", err, ErrBudget)
	}
	released, err := ReleaseChild(nextParent, 50*MicroUSDPerDollar)
	if err != nil {
		t.Fatal(err)
	}
	if released.HeldMicroUSD != 0 {
		t.Fatalf("held after release = %d, want 0", released.HeldMicroUSD)
	}
	if _, err := ReleaseChild(released, 1); !errors.Is(err, ErrEscrow) {
		t.Fatalf("over-release err = %v, want %v", err, ErrEscrow)
	}
}

func TestResolveRunChargeEstimatesUnknownProviderCostOnBudgetedRun(t *testing.T) {
	price := TokenPrice{
		InputMicroUSDPerMTok:     int64Ptr(3_000_000),  // $3 / MTok
		OutputMicroUSDPerMTok:    int64Ptr(15_000_000), // $15 / MTok
		CacheReadMicroUSDPerMTok: int64Ptr(300_000),    // $0.30 / MTok
	}
	usage := TokenUsage{
		InputTokens:     1_000,
		OutputTokens:    200,
		CacheReadTokens: 2_000,
	}
	// Expected: 1000*3 + 200*15 + 2000*0.3 = 3000 + 3000 + 600 = 6600 micro-USD.
	charge, estimated, err := ResolveRunCharge(true, nil, usage, &price, 0)
	if err != nil || !estimated || charge != 6_600 {
		t.Fatalf("estimated charge = %d estimated=%v err=%v", charge, estimated, err)
	}

	// Known provider cost takes precedence over estimate.
	reported := int64(4_200)
	charge, estimated, err = ResolveRunCharge(true, &reported, usage, &price, 0)
	if err != nil || estimated || charge != 4_200 {
		t.Fatalf("reported charge = %d estimated=%v err=%v", charge, estimated, err)
	}

	// Unbudgeted run with unknown cost charges 0 without error.
	charge, estimated, err = ResolveRunCharge(false, nil, usage, nil, 0)
	if err != nil || estimated || charge != 0 {
		t.Fatalf("unbudgeted unknown = %d estimated=%v err=%v", charge, estimated, err)
	}

	// Budgeted run with no price table falls back to fallbackMicroUSD if set.
	charge, estimated, err = ResolveRunCharge(true, nil, usage, nil, 12_345)
	if err != nil || !estimated || charge != 12_345 {
		t.Fatalf("fallback = %d estimated=%v err=%v", charge, estimated, err)
	}

	// Budgeted run with no price and no fallback fails closed.
	if _, _, err := ResolveRunCharge(true, nil, usage, nil, 0); !errors.Is(err, ErrUnknownCost) {
		t.Fatalf("no price no fallback err = %v, want %v", err, ErrUnknownCost)
	}
}

func TestChargeActualPreservesFullSpendAndPausesOnCross(t *testing.T) {
	budget := int64(1_000_000) // $1.00
	spent, paused, err := ChargeActual(budget, 800_000, 100_000, false)
	if err != nil || spent != 900_000 || paused {
		t.Fatalf("under budget = spent:%d paused:%v err:%v", spent, paused, err)
	}

	// Final call crosses budget - spent (100_000 remaining, call costs 350_000).
	// Full actual spend (1_250_000) MUST be preserved and paused set to true.
	spent, paused, err = ChargeActual(budget, spent, 350_000, false)
	if err != nil || spent != 1_250_000 || !paused {
		t.Fatalf("crossed budget = spent:%d paused:%v err:%v", spent, paused, err)
	}

	// Subsequent call while paused is refused.
	spent2, paused2, err := ChargeActual(budget, spent, 1_000, paused)
	if !errors.Is(err, ErrExhausted) || spent2 != 1_250_000 || !paused2 {
		t.Fatalf("while paused = spent:%d paused:%v err:%v", spent2, paused2, err)
	}
}

func TestChargeCappedMatchesCappedSemantics(t *testing.T) {
	spent, paused, err := ChargeCapped(10, 0, 10, false)
	if err != nil || spent != 10 || !paused {
		t.Fatalf("exact = %d paused:%v err:%v", spent, paused, err)
	}
	spent, paused, err = ChargeCapped(10, 0, 11, false)
	if !errors.Is(err, ErrExhausted) || spent != 0 || !paused {
		t.Fatalf("over = %d paused:%v err:%v", spent, paused, err)
	}
}
