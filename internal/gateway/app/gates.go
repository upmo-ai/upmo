package app

import (
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Gates is the per-key cooldown for provider credentials.
type Gates struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]domain.Gate
}

// NewGates returns an empty cooldown map that reads the wall clock.
func NewGates() *Gates {
	return &Gates{now: time.Now, m: map[string]domain.Gate{}}
}

func (g *Gates) clock() time.Time {
	if g == nil || g.now == nil {
		return time.Now()
	}
	return g.now()
}

// Closed reports whether ref may be used. A nil map allows every key.
func (g *Gates) Closed(ref string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.m[ref].Closed(g.clock())
}

// Observe records the outcome for ref. retryAfter is the provider's wait.
func (g *Gates) Observe(ref string, class domain.FailureClass, retryAfter time.Duration) {
	if g == nil || ref == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]domain.Gate{}
	}
	g.m[ref] = g.m[ref].Note(g.clock(), class, retryAfter)
}
