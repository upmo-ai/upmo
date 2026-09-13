package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestAcceptResultTrimsAndRejectsAnEmptySummary(t *testing.T) {
	got, err := AcceptResult("  Filed the receipts  ", "  two pages  ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "Filed the receipts" || got.Detail != "two pages" {
		t.Fatalf("result = %+v", got)
	}
	if _, err := AcceptResult("  ", "note"); !errors.Is(err, ErrResult) {
		t.Fatalf("empty summary error = %v, want %v", err, ErrResult)
	}
	if _, err := AcceptResult("ok", strings.Repeat("a", ResultDetailRunes+1)); !errors.Is(err, ErrResult) {
		t.Fatalf("long detail error = %v, want %v", err, ErrResult)
	}
}

func TestWorktreeAllowsAPathAndRejectsASpace(t *testing.T) {
	got, err := Worktree("  repo/src  ")
	if err != nil || got != "repo/src" {
		t.Fatalf("worktree = %q %v", got, err)
	}
	if _, err := Worktree("repo src"); !errors.Is(err, ErrWorktree) {
		t.Fatalf("space error = %v, want %v", err, ErrWorktree)
	}
}
