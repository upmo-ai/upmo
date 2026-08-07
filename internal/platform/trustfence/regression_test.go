package trustfence

import (
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/testutil/golden"
)

// Regression for the closer hidden with a zero-width joiner, found by fuzzing in UPM-5.
func TestRegression_zwj_closer(t *testing.T) {
	const zwj = "\u200D"

	system := "<" + zwj + "system"
	closerBody := "<" + zwj + "/memory>"
	wrapped := Wrap(TagMemory, "m", closerBody)

	tests := []struct {
		name string
		got  string
	}{
		{"neutralize_zwj_system", NeutralizeSlot(system)},
		{"wrap_zwj_closer", wrapped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			golden.String(t, tt.name, tt.got)
		})
	}

	// Removing the joiner must still leave exactly one closer.
	if n := strings.Count(strings.ReplaceAll(wrapped, zwj, ""), Close(TagMemory)); n != 1 {
		t.Fatalf("Wrap(%q) has %d closers, want 1: %q", closerBody, n, wrapped)
	}
}

// Regression for a control token split by a Mongolian free variation selector, found by fuzzing in UPM-15.5.
func TestRegression_mongolian_variation_selector(t *testing.T) {
	for _, sel := range []rune{'\u180B', '\u180C', '\u180D'} {
		in := "<" + string(sel) + "|"
		got := NeutralizeSlot(in)
		if got != "&lt;|" {
			t.Fatalf("NeutralizeSlot(%q) = %q, want %q", in, got, "&lt;|")
		}
		if strings.Contains(got, string(sel)) {
			t.Fatalf("NeutralizeSlot(%q) kept the selector: %q", in, got)
		}
	}
}
