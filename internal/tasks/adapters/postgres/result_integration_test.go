//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestParentCancelSetsTheCauseOnOpenChildren(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	first, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Read the folder", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, first.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := app.Cancel(ctx, store, ownerAccess, parent.ID); err != nil {
		t.Fatal(err)
	}
	if cause := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, parent.ID); cause != "" {
		t.Fatalf("parent cause = %q", cause)
	}
	if cause := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, first.ID); cause != "" {
		t.Fatalf("direct cancel cause = %q", cause)
	}
	if status := taskStatus(t, owner, second.ID); status != string(domain.Failed) {
		t.Fatalf("child status = %s", status)
	}
	if cause := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, second.ID); cause != domain.ParentCanceled {
		t.Fatalf("propagated cause = %q", cause)
	}
}

func TestSaveResultNeutralisesAndStoresOnce(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	if err := app.SaveResult(ctx, store, ownerAccess, parent.ID, "Done", ""); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("root result error = %v, want %v", err, domain.ErrInput)
	}
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, child.ID, " See {{prior}} ", "two pages"); err != nil {
		t.Fatal(err)
	}
	var summary, detail string
	if err := owner.QueryRow(ctx, `
		SELECT result->>'summary', result->>'detail'
		FROM tasks.tasks WHERE id = $1`, child.ID).Scan(&summary, &detail); err != nil {
		t.Fatal(err)
	}
	if summary != "See { {prior}}" || detail != "two pages" {
		t.Fatalf("summary = %q detail = %q", summary, detail)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, child.ID, "Again", ""); !errors.Is(err, domain.ErrResult) {
		t.Fatalf("second result error = %v, want %v", err, domain.ErrResult)
	}
}

func TestOneOpenTaskHoldsAWorktree(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	other := openRoot(t, store, "Close the week")
	if err := app.Claim(ctx, store, ownerAccess, parent.ID, " repo/src "); err != nil {
		t.Fatal(err)
	}
	if err := app.Claim(ctx, store, ownerAccess, other.ID, "repo/src"); !errors.Is(err, domain.ErrWorktree) {
		t.Fatalf("second claim error = %v, want %v", err, domain.ErrWorktree)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.Claim(ctx, store, ownerAccess, other.ID, "repo/src"); err != nil {
		t.Fatalf("claim after the first finished: %v", err)
	}
}

func taskText(t *testing.T, owner *pgxpool.Pool, query, id string) string {
	t.Helper()
	var text string
	if err := owner.QueryRow(context.Background(), query, id).Scan(&text); err != nil {
		t.Fatal(err)
	}
	return text
}
