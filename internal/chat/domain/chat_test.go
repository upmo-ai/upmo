package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestProfileFitsThePrefix(t *testing.T) {
	got, err := Profile("  Ada  ")
	if err != nil || got != "Ada" {
		t.Fatalf("profile = %q %v", got, err)
	}
	if Block(got) != "Profile:\nAda" {
		t.Fatalf("block = %q", Block(got))
	}
	if Block("") != "" {
		t.Fatal("empty profile still produced a block")
	}
	if _, err := Profile(strings.Repeat("a", ProfileBytes+1)); !errors.Is(err, ErrProfile) {
		t.Fatalf("long profile error = %v", err)
	}
}

func TestLineRejectsAnEmptyBody(t *testing.T) {
	got, err := Line("  hello  ")
	if err != nil || got != "hello" {
		t.Fatalf("line = %q %v", got, err)
	}
	if _, err := Line("  "); !errors.Is(err, ErrLine) {
		t.Fatalf("blank line error = %v", err)
	}
}
