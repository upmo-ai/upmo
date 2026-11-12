package domain

import "testing"

func TestCleanVisibilityForAgents(t *testing.T) {
	visibility, ids, err := CleanVisibility(VisibilityEveryone, nil)
	if err != nil || visibility != VisibilityEveryone || len(ids) != 0 {
		t.Fatalf("everyone = %q %v %v", visibility, ids, err)
	}
	visibility, ids, err = CleanVisibility("", nil)
	if err != nil || visibility != VisibilityPrivate || len(ids) != 0 {
		t.Fatalf("default = %q", visibility)
	}
	team := "10000000-0000-7000-8000-000000000007"
	visibility, ids, err = CleanVisibility(VisibilityTeam, []string{team})
	if err != nil || visibility != VisibilityTeam || len(ids) != 1 || ids[0] != team {
		t.Fatalf("team = %q %v %v", visibility, ids, err)
	}
	if _, _, err := CleanVisibility(VisibilityPrivate, []string{team}); err == nil {
		t.Fatal("private with teams was accepted")
	}
}
