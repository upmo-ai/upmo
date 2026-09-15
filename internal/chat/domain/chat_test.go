package domain

import (
	"errors"
	"testing"
)

func TestLineRejectsAnEmptyBody(t *testing.T) {
	got, err := Line("  hello  ")
	if err != nil || got != "hello" {
		t.Fatalf("line = %q %v", got, err)
	}
	if _, err := Line("  "); !errors.Is(err, ErrLine) {
		t.Fatalf("blank line error = %v", err)
	}
}
