package example

import "testing"

func TestBracket(t *testing.T) {
	if got := Bracket("row"); got != "[row]" {
		t.Fatalf("Bracket(%q) = %q, want %q", "row", got, "[row]")
	}
}
