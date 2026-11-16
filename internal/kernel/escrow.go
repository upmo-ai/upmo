package kernel

import (
	"errors"
	"fmt"
	"math"
)

const (
	// MicroUSDPerDollar is 1 USD in micro-USD (1e-6 USD).
	MicroUSDPerDollar int64 = 1_000_000
	// MicroUSDPerCent is 1 US cent in micro-USD.
	MicroUSDPerCent int64 = 10_000
	// DefaultMaxBudgetMicroUSD is the default ceiling for a single root budget ($10,000).
	DefaultMaxBudgetMicroUSD int64 = 10_000 * MicroUSDPerDollar
)

var (
	// ErrBudget reports a negative, zero, over-ceiling, or overflowing budget amount.
	ErrBudget = errors.New("kernel: budget")
	// ErrExhausted reports a budget that is already paused or cannot take another spend.
	ErrExhausted = errors.New("kernel: exhausted")
	// ErrEscrow reports an invalid escrow reserve or release.
	ErrEscrow = errors.New("kernel: escrow")
	// ErrUnknownCost reports a budgeted call whose cost and price-table estimate are unavailable.
	ErrUnknownCost = errors.New("kernel: unknown cost")
)

// Escrow holds integer micro-USD budget accounting for one task or run.
type Escrow struct {
	BudgetMicroUSD int64
	HeldMicroUSD   int64
	SpentMicroUSD  int64
	Paused         bool
}

// TokenUsage holds token counts for one model call.
type TokenUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
}

// TokenPrice holds per-million-token prices in micro-USD (1e-6 USD per MTok).
// A nil rate means that dimension has no catalog rate.
type TokenPrice struct {
	InputMicroUSDPerMTok      *int64
	OutputMicroUSDPerMTok     *int64
	CacheReadMicroUSDPerMTok  *int64
	CacheWriteMicroUSDPerMTok *int64
	ReasoningMicroUSDPerMTok  *int64
}

// Remaining returns budget minus used, clamped at zero.
func Remaining(budget, used int64) int64 {
	if budget <= 0 || used < 0 {
		if budget <= 0 {
			return 0
		}
		return budget
	}
	if used >= budget {
		return 0
	}
	return budget - used
}

// Available returns the unspent, unheld micro-USD remaining in e.
func (e Escrow) Available() int64 {
	if e.Paused || e.HeldMicroUSD < 0 || e.SpentMicroUSD < 0 {
		return 0
	}
	if e.HeldMicroUSD > e.BudgetMicroUSD || e.SpentMicroUSD > e.BudgetMicroUSD-e.HeldMicroUSD {
		return 0
	}
	return e.BudgetMicroUSD - e.HeldMicroUSD - e.SpentMicroUSD
}

// CheckRootBudget validates a root funding amount against [1, maxBudget].
// A non-positive maxBudget skips the upper bound.
func CheckRootBudget(budgetMicroUSD, maxBudgetMicroUSD int64) error {
	if budgetMicroUSD < 1 {
		return ErrBudget
	}
	if maxBudgetMicroUSD > 0 && budgetMicroUSD > maxBudgetMicroUSD {
		return ErrBudget
	}
	return nil
}

// BudgetFits reports whether amount can be reserved from budget when held is
// already reserved. When maxAmount > 0, amount must not exceed maxAmount.
func BudgetFits(budget, held, amount, maxAmount int64) error {
	if amount < 1 {
		return ErrBudget
	}
	if maxAmount > 0 && amount > maxAmount {
		return ErrBudget
	}
	if held < 0 || budget < held || amount > budget-held {
		return ErrBudget
	}
	return nil
}

// AllocateChild reserves childBudget from parent and returns the updated parent
// and new child escrow states.
func AllocateChild(parent Escrow, childBudget, maxChildBudget int64) (Escrow, Escrow, error) {
	if parent.Paused {
		return parent, Escrow{}, ErrExhausted
	}
	if parent.SpentMicroUSD < 0 || parent.HeldMicroUSD < 0 || parent.SpentMicroUSD > parent.BudgetMicroUSD {
		return parent, Escrow{}, ErrEscrow
	}
	spendable := parent.BudgetMicroUSD - parent.SpentMicroUSD
	if err := BudgetFits(spendable, parent.HeldMicroUSD, childBudget, maxChildBudget); err != nil {
		return parent, Escrow{}, err
	}
	nextParent := parent
	nextParent.HeldMicroUSD += childBudget
	child := Escrow{BudgetMicroUSD: childBudget}
	return nextParent, child, nil
}

// ReleaseChild releases childBudget back to parent's held balance when a child
// finishes or cancels.
func ReleaseChild(parent Escrow, childBudget int64) (Escrow, error) {
	if childBudget < 0 || parent.HeldMicroUSD < childBudget {
		return parent, ErrEscrow
	}
	next := parent
	next.HeldMicroUSD -= childBudget
	return next, nil
}

// EstimateMicroUSD computes a pure integer micro-USD estimate from usage and price.
// Non-zero usage on a dimension with a nil rate makes the estimate unknown (false).
func EstimateMicroUSD(usage TokenUsage, price TokenPrice) (int64, bool) {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 ||
		usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 || usage.ReasoningTokens < 0 {
		return 0, false
	}
	var total int64
	add := func(tokens int64, rate *int64) bool {
		if tokens == 0 {
			return true
		}
		if rate == nil || *rate < 0 {
			return false
		}
		if *rate == 0 {
			return true
		}
		if tokens > (math.MaxInt64-(MicroUSDPerDollar-1))/(*rate) {
			return false
		}
		// Ceiling division so non-zero usage at a positive rate is at least 1 micro-USD.
		dim := (tokens*(*rate) + MicroUSDPerDollar - 1) / MicroUSDPerDollar
		if total > math.MaxInt64-dim {
			return false
		}
		total += dim
		return true
	}
	if !add(usage.InputTokens, price.InputMicroUSDPerMTok) ||
		!add(usage.OutputTokens, price.OutputMicroUSDPerMTok) ||
		!add(usage.CacheReadTokens, price.CacheReadMicroUSDPerMTok) ||
		!add(usage.CacheWriteTokens, price.CacheWriteMicroUSDPerMTok) {
		return 0, false
	}
	if usage.ReasoningTokens > 0 {
		rate := price.ReasoningMicroUSDPerMTok
		if rate == nil {
			rate = price.OutputMicroUSDPerMTok
		}
		if !add(usage.ReasoningTokens, rate) {
			return 0, false
		}
	}
	return total, true
}

// ResolveRunCharge determines the micro-USD charge for one model call.
// On a budgeted run, when reportedCostMicroUSD is nil, it computes a price-table
// estimate (or uses fallbackMicroUSD if > 0 when price is missing/incomplete).
func ResolveRunCharge(
	budgeted bool,
	reportedCostMicroUSD *int64,
	usage TokenUsage,
	price *TokenPrice,
	fallbackMicroUSD int64,
) (charge int64, estimated bool, err error) {
	if reportedCostMicroUSD != nil {
		if *reportedCostMicroUSD < 0 {
			return 0, false, fmt.Errorf("%w: negative reported cost", ErrBudget)
		}
		return *reportedCostMicroUSD, false, nil
	}
	if !budgeted {
		return 0, false, nil
	}
	if price != nil {
		if est, ok := EstimateMicroUSD(usage, *price); ok {
			return est, true, nil
		}
	}
	if fallbackMicroUSD > 0 {
		return fallbackMicroUSD, true, nil
	}
	return 0, false, ErrUnknownCost
}

// ChargeActual adds amount to spent on a post-call basis. If spent + amount
// reaches or crosses budget, paused becomes true while preserving full actual
// spent. A run that was already paused returns ErrExhausted without changing spent.
func ChargeActual(budget, spent, amount int64, paused bool) (int64, bool, error) {
	if amount < 0 || budget < 0 || spent < 0 {
		return spent, paused, ErrBudget
	}
	if paused {
		return spent, true, ErrExhausted
	}
	if amount > math.MaxInt64-spent {
		return spent, true, ErrBudget
	}
	nextSpent := spent + amount
	nextPaused := budget > 0 && nextSpent >= budget
	return nextSpent, nextPaused, nil
}

// ChargeCapped applies amount to spent only when it fits within budget - spent.
// Landing on budget pauses; an amount past budget pauses without mutating spent
// and returns ErrExhausted.
func ChargeCapped(budget, spent, amount int64, paused bool) (int64, bool, error) {
	if amount < 1 {
		return spent, paused, ErrBudget
	}
	if paused {
		return spent, true, ErrExhausted
	}
	if spent > budget || amount > budget-spent {
		return spent, true, ErrExhausted
	}
	spent += amount
	if spent == budget {
		return spent, true, nil
	}
	return spent, false, nil
}
