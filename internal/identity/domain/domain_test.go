package domain

import "testing"

func TestParseRejectsUnknown(t *testing.T) {
	if _, err := ParseRole("boss"); err == nil {
		t.Fatal("ParseRole(boss) succeeded")
	}
	if _, err := ParseLevel("owner"); err == nil {
		t.Fatal("ParseLevel(owner) succeeded")
	}
	if _, err := ParseLevel("author"); err == nil {
		t.Fatal("ParseLevel(author) succeeded")
	}
	if _, err := ParseVisibility("public"); err == nil {
		t.Fatal("ParseVisibility(public) succeeded")
	}
	if _, err := ParseKind("service"); err == nil {
		t.Fatal("ParseKind(service) succeeded")
	}
}

func TestParseAcceptsKnown(t *testing.T) {
	role, err := ParseRole("owner")
	if err != nil || role != RoleOwner {
		t.Fatalf("role = %q, %v", role, err)
	}
	level, err := ParseLevel("editor")
	if err != nil || level != LevelEditor {
		t.Fatalf("level = %q, %v", level, err)
	}
	visibility, err := ParseVisibility("team")
	if err != nil || visibility != VisibilityTeam {
		t.Fatalf("visibility = %q, %v", visibility, err)
	}
	kind, err := ParseKind("run")
	if err != nil || kind != KindRun {
		t.Fatalf("kind = %q, %v", kind, err)
	}
}
