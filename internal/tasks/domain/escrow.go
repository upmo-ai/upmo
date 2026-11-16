package domain

import (
	"errors"
	"strings"

	"github.com/upmo-ai/upmo/internal/kernel"
)

const (
	// NoticeBudget is the one line written when a budget is exhausted.
	NoticeBudget = "Budget exhausted."
)

// ErrExhausted is a budget that cannot take another spend, or a paused task.
var ErrExhausted = errors.New("tasks: exhausted")

// ErrAuthority is an allow list wider than the parent's.
var ErrAuthority = errors.New("tasks: authority")

// ErrTaint is a child that drops a parent's taint, or a taint that is cleared.
var ErrTaint = errors.New("tasks: taint")

// BudgetFits reports whether amount can be reserved from a budget that already
// holds held. The amount is at least 1 and at most TemplateBudgetMax.
func BudgetFits(budget, held, amount int) error {
	if err := kernel.BudgetFits(int64(budget), int64(held), int64(amount), int64(TemplateBudgetMax)); err != nil {
		return ErrBudget
	}
	return nil
}

// BudgetFitsMicroUSD validates a micro-USD reserve against budget - held.
func BudgetFitsMicroUSD(budget, held, amount int64) error {
	err := kernel.BudgetFits(budget, held, amount, kernel.DefaultMaxBudgetMicroUSD)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, kernel.ErrBudget):
		return ErrBudget
	default:
		return err
	}
}

// Charge applies amount to spent. Landing on the budget pauses. An amount past
// the budget pauses and leaves spent unchanged. A paused budget refuses.
func Charge(budget, spent, amount int, paused bool) (int, bool, error) {
	nextSpent, nextPaused, err := kernel.ChargeCapped(int64(budget), int64(spent), int64(amount), paused)
	switch {
	case errors.Is(err, kernel.ErrBudget):
		return int(nextSpent), nextPaused, ErrBudget
	case errors.Is(err, kernel.ErrExhausted):
		return int(nextSpent), nextPaused, ErrExhausted
	default:
		return int(nextSpent), nextPaused, err
	}
}

// ChargeMicroUSD applies actual post-call micro-USD spend, preserving full
// actual spent and setting paused = true when spent crosses budget.
func ChargeMicroUSD(budget, spent, amount int64, paused bool) (int64, bool, error) {
	nextSpent, nextPaused, err := kernel.ChargeActual(budget, spent, amount, paused)
	switch {
	case errors.Is(err, kernel.ErrBudget):
		return nextSpent, nextPaused, ErrBudget
	case errors.Is(err, kernel.ErrExhausted):
		return nextSpent, nextPaused, ErrExhausted
	default:
		return nextSpent, nextPaused, err
	}
}

// Intersect keeps the requested names that the parent already has.
// The result never contains a name the parent does not have.
func Intersect(parent, requested string) string {
	have := map[string]struct{}{}
	for _, name := range splitAllow(parent) {
		have[name] = struct{}{}
	}
	var kept []string
	seen := map[string]struct{}{}
	for _, name := range splitAllow(requested) {
		if _, ok := have[name]; !ok {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		kept = append(kept, name)
	}
	return strings.Join(kept, ",")
}

func splitAllow(list string) []string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	parts := strings.Split(list, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			names = append(names, part)
		}
	}
	return names
}
