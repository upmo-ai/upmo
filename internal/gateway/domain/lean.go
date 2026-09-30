package domain

import (
	"errors"
	"strings"
)

// SaveFloor is the minimum saving, in whole percent, that every canary must
// show before the lean path is admitted.
const SaveFloor = 15

// ErrLean reports a malformed canary set.
var ErrLean = errors.New("gateway: lean")

// Canary is one measured comparison between the baseline and the lean path.
// Baseline and Lean are the same unit of cost (for example, tokens).
type Canary struct {
	Name     string
	Baseline int
	Lean     int
}

// Admit decides whether the lean path may be used. It is a pure check over
// recorded canary figures and never calls a model.
//
// A disabled switch or an empty canary set is never admitted. Every canary
// is validated before any saving is judged, so a malformed set always
// returns ErrLean. The lean path is admitted only when every canary saves at
// least SaveFloor percent, using integer division.
func Admit(enabled bool, canaries []Canary) (bool, error) {
	if !enabled || len(canaries) == 0 {
		return false, nil
	}

	seen := make(map[string]struct{}, len(canaries))
	for _, c := range canaries {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return false, ErrLean
		}
		if _, dup := seen[name]; dup {
			return false, ErrLean
		}
		seen[name] = struct{}{}
		if c.Baseline <= 0 {
			return false, ErrLean
		}
		if c.Lean < 0 || c.Lean > c.Baseline {
			return false, ErrLean
		}
	}

	for _, c := range canaries {
		if saving(c) < SaveFloor {
			return false, nil
		}
	}
	return true, nil
}

// saving is the whole-percent reduction from Baseline to Lean. Callers must
// have validated the canary first.
func saving(c Canary) int {
	return (c.Baseline - c.Lean) * 100 / c.Baseline
}
