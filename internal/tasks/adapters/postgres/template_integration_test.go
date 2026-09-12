//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestSaveAndOpenTemplate(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	tmpl := domain.Template{
		Goal:       "File the {{kind}} for {{client}}",
		AgentID:    "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Outline:    []string{"Read the folder", "File them"},
		Deliveries: []string{"mail", "chat"},
		Budget:     100,
	}

	id, err := app.SaveTemplate(ctx, store, ownerAccess, "", tmpl)
	if err != nil {
		t.Fatalf("SaveTemplate() error = %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty template id")
	}

	got, err := app.OpenTemplate(ctx, store, ownerAccess, id)
	if err != nil {
		t.Fatalf("OpenTemplate() error = %v", err)
	}

	if got.Goal != tmpl.Goal {
		t.Errorf("goal = %q, want %q", got.Goal, tmpl.Goal)
	}
	if got.AgentID != tmpl.AgentID {
		t.Errorf("agent id = %q, want %q", got.AgentID, tmpl.AgentID)
	}
	if len(got.Outline) != 2 || got.Outline[0] != "Read the folder" || got.Outline[1] != "File them" {
		t.Errorf("outline = %#v, want %#v", got.Outline, tmpl.Outline)
	}
	if len(got.Deliveries) != 2 || got.Deliveries[0] != "mail" || got.Deliveries[1] != "chat" {
		t.Errorf("deliveries = %#v, want %#v", got.Deliveries, tmpl.Deliveries)
	}
	if got.Budget != tmpl.Budget {
		t.Errorf("budget = %d, want %d", got.Budget, tmpl.Budget)
	}

	_, slots, err := domain.Normalize(got)
	if err != nil {
		t.Fatalf("Normalize() on stored goal error = %v", err)
	}
	if len(slots) != 2 || slots[0] != "kind" || slots[1] != "client" {
		t.Fatalf("stored goal slots = %#v, want [kind client]", slots)
	}
}

func TestSaveAgainReplacesOutlineTitles(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	tmpl := domain.Template{
		Goal:       "File the {{kind}} for {{client}}",
		AgentID:    "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Outline:    []string{"Read the folder", "File them"},
		Deliveries: []string{"mail", "chat"},
		Budget:     100,
	}

	id, err := app.SaveTemplate(ctx, store, ownerAccess, "", tmpl)
	if err != nil {
		t.Fatalf("initial SaveTemplate() error = %v", err)
	}

	updated := tmpl
	updated.Outline = []string{"Only step"}
	savedID, err := app.SaveTemplate(ctx, store, ownerAccess, id, updated)
	if err != nil {
		t.Fatalf("SaveTemplate() again error = %v", err)
	}
	if savedID != id {
		t.Fatalf("saved id = %q, want %q", savedID, id)
	}

	got, err := app.OpenTemplate(ctx, store, ownerAccess, id)
	if err != nil {
		t.Fatalf("OpenTemplate() after update error = %v", err)
	}
	if len(got.Outline) != 1 || got.Outline[0] != "Only step" {
		t.Fatalf("outline = %#v, want [Only step]", got.Outline)
	}

	var stepCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.template_steps WHERE template_id = $1::uuid`, id).Scan(&stepCount); err != nil {
		t.Fatal(err)
	}
	if stepCount != 1 {
		t.Fatalf("step rows count = %d, want 1", stepCount)
	}

	var storedTitle string
	if err := owner.QueryRow(ctx, `SELECT title FROM tasks.template_steps WHERE template_id = $1::uuid`, id).Scan(&storedTitle); err != nil {
		t.Fatal(err)
	}
	if storedTitle != "Only step" {
		t.Fatalf("stored step title = %q, want %q", storedTitle, "Only step")
	}
}

func TestOpenByAnotherPersonReturnsErrGone(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	tmpl := domain.Template{
		Goal:       "File the {{kind}} for {{client}}",
		AgentID:    "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Outline:    []string{"Read the folder"},
		Deliveries: []string{"mail"},
		Budget:     100,
	}

	id, err := app.SaveTemplate(ctx, store, ownerAccess, "", tmpl)
	if err != nil {
		t.Fatalf("SaveTemplate() error = %v", err)
	}

	viewAccess := app.Access{
		UserID: otherUser,
		Allow:  "task:view",
	}
	_, err = app.OpenTemplate(ctx, store, viewAccess, id)
	if !errors.Is(err, app.ErrGone) {
		t.Fatalf("open by other person error = %v, want %v", err, app.ErrGone)
	}
}

func TestRemoveTemplate(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	tmpl := domain.Template{
		Goal:       "File the {{kind}} for {{client}}",
		AgentID:    "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Outline:    []string{"Read the folder", "File them"},
		Deliveries: []string{"mail", "chat"},
		Budget:     100,
	}

	id, err := app.SaveTemplate(ctx, store, ownerAccess, "", tmpl)
	if err != nil {
		t.Fatalf("SaveTemplate() error = %v", err)
	}

	removeAccess := app.Access{
		UserID: ownerUser,
		Allow:  ownerAccess.Allow + ",task:delete",
	}

	if err := app.RemoveTemplate(ctx, store, removeAccess, id); err != nil {
		t.Fatalf("RemoveTemplate() error = %v", err)
	}

	_, err = app.OpenTemplate(ctx, store, ownerAccess, id)
	if !errors.Is(err, app.ErrGone) {
		t.Fatalf("OpenTemplate() after remove error = %v, want %v", err, app.ErrGone)
	}

	var stepCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.template_steps WHERE template_id = $1::uuid`, id).Scan(&stepCount); err != nil {
		t.Fatal(err)
	}
	if stepCount != 0 {
		t.Fatalf("step rows after remove = %d, want 0", stepCount)
	}

	var deliveryCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.template_deliveries WHERE template_id = $1::uuid`, id).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 0 {
		t.Fatalf("delivery rows after remove = %d, want 0", deliveryCount)
	}
}

func TestMalformedGoalStoresNothing(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	tmpl := domain.Template{
		Goal:       "{{",
		AgentID:    "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Outline:    []string{"Read the folder"},
		Deliveries: []string{"mail"},
		Budget:     100,
	}

	_, err := app.SaveTemplate(ctx, store, ownerAccess, "", tmpl)
	if err == nil {
		t.Fatal("expected error for malformed goal")
	}

	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.templates`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("templates count = %d, want 0", count)
	}
}
