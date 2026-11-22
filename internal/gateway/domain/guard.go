package domain

import (
	"errors"
	"strings"
	"time"
)

const (
	// CapTools is a model that accepts tool calls.
	CapTools = "tools"
	// CapVision is a model that accepts images.
	CapVision = "vision"
	// CapStructured is a model that accepts a structured output schema.
	CapStructured = "structured"
	// OpenAfter is how many provider failures open a cooldown.
	OpenAfter = 3
	// DefaultCool is the cooldown when the provider does not name a retry-after.
	DefaultCool = 30 * time.Second
)

var (
	// ErrCapability is a required capability the model does not offer.
	ErrCapability = errors.New("gateway: capability")
	// ErrCooling is a provider key that is still in cooldown.
	ErrCooling = errors.New("gateway: cooling")
)

// Fit refuses a required capability the model does not offer.
// An empty requirement fits. A blank or unknown name does not.
func Fit(need, have []string) error {
	if len(need) == 0 {
		return nil
	}
	got := map[string]bool{}
	for _, h := range have {
		h = strings.ToLower(strings.TrimSpace(h))
		if knownCap(h) {
			got[h] = true
		}
	}
	for _, n := range need {
		n = strings.ToLower(strings.TrimSpace(n))
		if !knownCap(n) || !got[n] {
			return ErrCapability
		}
	}
	return nil
}

func knownCap(s string) bool {
	switch s {
	case CapTools, CapVision, CapStructured:
		return true
	default:
		return false
	}
}

// Gate is the cooldown for one provider key.
type Gate struct {
	Failures int
	Until    time.Time
}

// Closed reports whether the key may be used at now.
func (g Gate) Closed(now time.Time) bool {
	return g.Until.IsZero() || !now.Before(g.Until)
}

// Note records one attempt. A success clears the gate. A positive retry-after
// cools the key immediately. The third provider failure cools it for DefaultCool
// when the provider did not name a longer wait.
func (g Gate) Note(now time.Time, class FailureClass, retryAfter time.Duration) Gate {
	if retryAfter < 0 {
		retryAfter = 0
	}
	switch class {
	case ClassOK:
		return Gate{}
	case ClassCaller, ClassCanceled:
		return g
	case ClassCredential, ClassModelAccess:
		g.Until = later(g.Until, now.Add(DefaultCool))
		return g
	case ClassTransient, ClassRateLimit, ClassOverloaded, ClassTimeout:
		g.Failures++
		cool := retryAfter
		if g.Failures >= OpenAfter && cool < DefaultCool {
			cool = DefaultCool
		}
		if cool > 0 {
			g.Until = later(g.Until, now.Add(cool))
		}
		return g
	default:
		return g
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
