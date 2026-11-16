package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanNameAndSlug(t *testing.T) {
	name, err := CleanName("  Purchase Invoices  ")
	if err != nil || name != "Purchase Invoices" {
		t.Fatalf("name = %q %v", name, err)
	}
	if _, err := CleanName("   "); err == nil {
		t.Fatal("empty name succeeded")
	}
	if _, err := CleanName(strings.Repeat("a", NameRunes+1)); err == nil {
		t.Fatal("overlong name succeeded")
	}
	slug, err := CleanSlug("", name)
	if err != nil || slug != "purchase-invoices" {
		t.Fatalf("derived slug = %q %v", slug, err)
	}
	if got := DeriveSlug("🤖"); got != "agent" {
		t.Fatalf("fallback slug = %q", got)
	}
	if _, err := CleanSlug("Bad Slug", "Name"); err == nil {
		t.Fatal("uppercase slug succeeded")
	}
}

func TestCleanModelAndPlanApproval(t *testing.T) {
	p, m, err := CleanModel(" OpenAI ", " gpt-5 ")
	if err != nil || p != "OpenAI" || m != "gpt-5" {
		t.Fatalf("model = %q %q %v", p, m, err)
	}
	if _, _, err := CleanModel("OpenAI", ""); err == nil {
		t.Fatal("half model succeeded")
	}
	mode, err := CleanPlanApproval("")
	if err != nil || mode != PlanApprovalRisky {
		t.Fatalf("default plan_approval = %q %v", mode, err)
	}
	if _, err := CleanPlanApproval("sometimes"); err == nil {
		t.Fatal("invalid plan_approval succeeded")
	}
}

func TestCleanArraysAndCheckCriteria(t *testing.T) {
	classes, err := CleanToolClasses([]string{"shell", "connector", "shell"})
	if err != nil || len(classes) != 2 {
		t.Fatalf("classes = %v %v", classes, err)
	}
	folders, err := CleanKnowledgeFolderIDs([]string{"018F3B2C-7A10-7C4A-8E11-111111111111"})
	if err != nil || len(folders) != 1 || folders[0] != "018f3b2c-7a10-7c4a-8e11-111111111111" {
		t.Fatalf("folders = %v %v", folders, err)
	}
	if _, err := CleanKnowledgeFolderIDs([]string{"not-a-uuid"}); err == nil {
		t.Fatal("invalid folder UUID succeeded")
	}
	criteria, err := CleanCheckCriteria([]json.RawMessage{json.RawMessage(` {"rule":"totals match"} `)})
	if err != nil || len(criteria) != 1 {
		t.Fatalf("criteria = %v %v", criteria, err)
	}
	if _, err := CleanCheckCriteria([]json.RawMessage{json.RawMessage(`{bad`)}); err == nil {
		t.Fatal("invalid check criterion JSON succeeded")
	}
}

func TestCleanVisibility(t *testing.T) {
	const team = "018f3b2c-7a10-7c4a-8e11-111111111111"
	v, ids, err := CleanVisibility("", nil)
	if err != nil || v != VisibilityPrivate || len(ids) != 0 {
		t.Fatalf("private = %q %v %v", v, ids, err)
	}
	v, ids, err = CleanVisibility(VisibilityTeam, []string{team, team})
	if err != nil || v != VisibilityTeam || len(ids) != 1 || ids[0] != team {
		t.Fatalf("team = %q %v %v", v, ids, err)
	}
	if _, _, err := CleanVisibility(VisibilityTeam, nil); err == nil {
		t.Fatal("team without teams succeeded")
	}
	if _, _, err := CleanVisibility(VisibilityPrivate, []string{team}); err == nil {
		t.Fatal("private with teams succeeded")
	}
}
