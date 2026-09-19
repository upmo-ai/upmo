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

func TestEscrowHoldsTheChildBudgetAndReleasesIt(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, ownerAccess, parent.ID, 50); err != nil {
		t.Fatal(err)
	}

	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 30, "task:edit,task:delete")
	if err != nil {
		t.Fatal(err)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 30 {
		t.Fatalf("held = %d, want 30", held)
	}
	var childAllow string
	var budget int
	if err := owner.QueryRow(ctx, `SELECT budget, allow FROM tasks.tasks WHERE id = $1`, child.ID).Scan(&budget, &childAllow); err != nil {
		t.Fatal(err)
	}
	if budget != 30 || childAllow != "task:edit" {
		t.Fatalf("child budget = %d allow = %q", budget, childAllow)
	}
	if err := app.Fund(ctx, store, ownerAccess, child.ID, 5); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("fund a child error = %v, want %v", err, domain.ErrBudget)
	}

	if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Another", 30, "task:view"); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("second reserve error = %v, want %v", err, domain.ErrBudget)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, child.ID); err != nil {
		t.Fatal(err)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 0 {
		t.Fatalf("held after cancel = %d, want 0", held)
	}
}

func TestSpendExhaustionPausesAndNotifiesOnce(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 10, "task:view")
	if err != nil {
		t.Fatal(err)
	}

	if err := app.Spend(ctx, store, ownerAccess, child.ID, 10); err != nil {
		t.Fatal(err)
	}
	if paused := taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, child.ID); !paused {
		t.Fatal("exact spend did not pause")
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, child.ID); spent != 10 {
		t.Fatalf("spent = %d, want 10", spent)
	}
	var notices int
	var body string
	if err := owner.QueryRow(ctx, `
		SELECT count(*), min(body)
		FROM tasks.chat_events
		WHERE task_id = $1`, parent.ID).Scan(&notices, &body); err != nil {
		t.Fatal(err)
	}
	if notices != 1 || body != domain.NoticeBudget {
		t.Fatalf("notices = %d body = %q", notices, body)
	}
	if status := taskStatus(t, owner, child.ID); status != string(domain.Preparing) {
		t.Fatalf("status = %s", status)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Advance}); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("advance while paused = %v", err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Plan}); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("plan while paused = %v", err)
	}

	if err := app.Spend(ctx, store, ownerAccess, child.ID, 1); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("second spend = %v", err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, parent.ID).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("notices after a second spend = %d", notices)
	}
}

func TestOverspendPausesWithoutCharging(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 10, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, child.ID, 11); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("overspend = %v", err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, child.ID); spent != 0 {
		t.Fatalf("spent = %d, want 0", spent)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, child.ID) {
		t.Fatal("overspend did not pause")
	}
	var body string
	if err := owner.QueryRow(ctx, `SELECT body FROM tasks.chat_events WHERE task_id = $1`, parent.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != domain.NoticeBudget {
		t.Fatalf("notice = %q", body)
	}
}

func TestTaintSpreadsAndAuthorityStaysNarrow(t *testing.T) {
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
	if err := app.Taint(ctx, store, ownerAccess, first.ID); err != nil {
		t.Fatal(err)
	}
	if !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, parent.ID) || !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, first.ID) || !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, second.ID) {
		t.Fatal("taint did not spread to the parent and the sibling")
	}
	third, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Send the note", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, third.ID) {
		t.Fatal("a new child of a tainted parent was clean")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET tainted = false WHERE id = $1`, parent.ID); err == nil || !containsTask(err, "tasks: taint") {
		t.Fatalf("clear taint error = %v", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id, budget, allow, tainted)
		VALUES ($1::uuid, 'private', 'Wide', 'Preparing', $2::uuid, 1, 'task:delete', false)`,
		ownerUser, parent.ID); err == nil || !containsTask(err, "tasks: authority") && !containsTask(err, "tasks: taint") {
		t.Fatalf("wide child error = %v", err)
	}
}

func taskInt(t *testing.T, owner *pgxpool.Pool, query, id string) int {
	t.Helper()
	var n int
	if err := owner.QueryRow(context.Background(), query, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func taskBool(t *testing.T, owner *pgxpool.Pool, query, id string) bool {
	t.Helper()
	var flag bool
	if err := owner.QueryRow(context.Background(), query, id).Scan(&flag); err != nil {
		t.Fatal(err)
	}
	return flag
}
