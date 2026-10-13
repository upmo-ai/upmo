package domain

import "testing"

func TestResolveAllowBundleIsTheSurface(t *testing.T) {
	got, err := ResolveAllow([]string{"search", "write"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "search" || got[1] != "write" {
		t.Fatalf("got = %v", got)
	}
}

func TestResolveAllowIntersectsAndKeepsBundleOrder(t *testing.T) {
	got, err := ResolveAllow([]string{"search", "write", "read"}, []string{"write", "search"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "search" || got[1] != "write" {
		t.Fatalf("got = %v", got)
	}
}

func TestResolveAllowWithoutBundleKeepsAllow(t *testing.T) {
	got, err := ResolveAllow(nil, []string{"search"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "search" {
		t.Fatalf("got = %v", got)
	}
}

func TestResolveAllowRejectsEmptyOrRepeat(t *testing.T) {
	if _, err := ResolveAllow([]string{""}, nil, true); err == nil {
		t.Fatal("empty name")
	}
	if _, err := ResolveAllow([]string{"a", "a"}, nil, true); err == nil {
		t.Fatal("repeat")
	}
}

func TestResolveAllowEmptyBundleExposesNoTools(t *testing.T) {
	got, err := ResolveAllow(nil, []string{"search"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got = %v", got)
	}
}
