package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestRouteTrimsAndRefusesWhatIsNoName(t *testing.T) {
	if got, err := Route("  draft "); err != nil || got != "draft" {
		t.Fatalf("route = %q %v", got, err)
	}
	if got, err := Route(""); err != nil || got != "" {
		t.Fatalf("empty route = %q %v", got, err)
	}
	for _, bad := range []string{"two words", "-lead", "a/b", "line\nbreak", strings.Repeat("a", 65)} {
		if _, err := Route(bad); !errors.Is(err, ErrRoute) {
			t.Fatalf("route %q error = %v", bad, err)
		}
	}
}

func TestReasonNamesEveryClassWithoutProviderWords(t *testing.T) {
	want := map[string]string{
		"rate_limit":   "rate limited",
		"transient":    "not reachable",
		"credential":   "key rejected",
		"model_access": "model unavailable",
		"overloaded":   "model unavailable",
		"timeout":      "timed out",
		"canceled":     "stopped",
		"internal":     "stopped",
		"caller":       "stopped",
		"anything":     "stopped",
	}
	for class, reason := range want {
		if got := Reason(class); got != reason {
			t.Fatalf("Reason(%q) = %q, want %q", class, got, reason)
		}
	}
}
