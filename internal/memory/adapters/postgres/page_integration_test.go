//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestPageSavesProfileAndUserFacts(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)

	if err := app.SaveProfile(ctx, store, person, "Prefers tea.", at); err != nil {
		t.Fatal(err)
	}
	id, err := app.AddFact(ctx, store, person, "Ada prefers tea", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.AddFact(ctx, store, person, "Ada prefers tea.", at.Add(time.Minute)); !errors.Is(err, app.ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}

	page, err := app.LoadPage(ctx, store, person)
	if err != nil {
		t.Fatal(err)
	}
	if page.Profile != "Prefers tea." || len(page.Facts) != 1 || page.Facts[0].ID != id || page.Facts[0].Trust != "user" {
		t.Fatalf("page=%+v", page)
	}

	next, err := app.EditFact(ctx, store, person, id, "Ada prefers coffee", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if next == id {
		t.Fatal("edit kept the row")
	}
	same, err := app.EditFact(ctx, store, person, next, "Ada prefers coffee.", at.Add(3*time.Minute))
	if err != nil || same != next {
		t.Fatalf("same=%s err=%v", same, err)
	}

	page, err = app.LoadPage(ctx, store, person)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].ID != next || page.Facts[0].Claim != "Ada prefers coffee" {
		t.Fatalf("after edit %+v", page.Facts)
	}

	var oldTrust string
	var superseded *string
	err = owner.QueryRow(ctx, `
		SELECT trust, superseded_by::text FROM memory.facts WHERE id = $1::uuid`, id).
		Scan(&oldTrust, &superseded)
	if err != nil {
		t.Fatal(err)
	}
	if oldTrust != "user" || superseded == nil || *superseded != next {
		t.Fatalf("old trust=%s superseded=%v", oldTrust, superseded)
	}

	if err := app.Forget(ctx, store, person, next, at.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := app.Forget(ctx, store, person, next, at); !errors.Is(err, app.ErrGone) {
		t.Fatalf("second forget: %v", err)
	}
	page, err = app.LoadPage(ctx, store, person)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 0 {
		t.Fatalf("forgotten still listed: %+v", page.Facts)
	}
}
