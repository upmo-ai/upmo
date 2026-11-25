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
func BudgetFits(budget, held, amount int64) error {
	if err := kernel.BudgetFits(budget, held, amount, TemplateBudgetMax); err != nil {
		return ErrBudget
	}
	return nil
}

// Charge applies amount to spent. Reaching or crossing the budget pauses and
// keeps the full actual spend. A paused budget refuses without adding.
func Charge(budget, spent, amount int64, paused bool) (int64, bool, error) {
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
