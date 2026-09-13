package domain

import (
	"errors"
	"strings"
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
	if amount < 1 || amount > TemplateBudgetMax {
		return ErrBudget
	}
	if held < 0 || budget < held || amount > budget-held {
		return ErrBudget
	}
	return nil
}

// Charge applies amount to spent. Landing on the budget pauses. An amount past
// the budget pauses and leaves spent unchanged. A paused budget refuses.
func Charge(budget, spent, amount int, paused bool) (int, bool, error) {
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
