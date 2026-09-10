//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestTransitionWalksToCheckingAndAppendsEvents(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil || opened.Status != domain.Preparing {
		t.Fatalf("open = %+v %v", opened, err)
	}
	status := opened.Status
	for _, next := range []domain.Status{domain.Planning, domain.Working, domain.Checking} {
		got, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance})
		if err != nil || got.Status != next {
			t.Fatalf("advance to %s = %+v %v", next, got, err)
		}
		status = got.Status
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance}); !errors.Is(err, domain.ErrEvidence) {
		t.Fatalf("advance from Checking = %v", err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Pass}); !errors.Is(err, domain.ErrEvidence) {
		t.Fatalf("pass = %v", err)
	}
	noted, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Chat, Body: " the figures "})
	if err != nil || noted.Status != status {
		t.Fatalf("chat = %+v %v", noted, err)
	}
	var events, chats int
	var stored, body string
	if err := owner.QueryRow(ctx, `SELECT status FROM tasks.tasks WHERE id = $1`, opened.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != string(domain.Checking) {
		t.Fatalf("stored status = %s", stored)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.task_events WHERE task_id = $1`, opened.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 4 {
		t.Fatalf("task events = %d", events)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*), COALESCE(max(body), '') FROM tasks.chat_events WHERE task_id = $1`, opened.ID).Scan(&chats, &body); err != nil {
		t.Fatal(err)
	}
	if chats != 1 || body != "the figures" {
		t.Fatalf("chat events = %d %q", chats, body)
	}
}

func TestTransitionFailThenChatStops(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Close the week"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Fail})
	if err != nil || failed.Status != domain.Failed {
		t.Fatalf("fail = %+v %v", failed, err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Chat, Body: "why"}); !errors.Is(err, domain.ErrTerminal) {
		t.Fatalf("chat after fail = %v", err)
	}
	var chats int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, opened.ID).Scan(&chats); err != nil {
		t.Fatal(err)
	}
	if chats != 0 {
		t.Fatalf("chat events = %d", chats)
	}
}

func TestTransitionHidesATaskFromAnotherPerson(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Sort the mail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, otherAccess, opened.ID, domain.Input{Kind: domain.Advance}); !errors.Is(err, app.ErrGone) {
		t.Fatalf("other person = %v", err)
	}
	var seen int
	appPool := pgtest.App(t, owner)
	tx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', 'task:view', true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', '{}', true),
		       set_config('upmo.shared_edit', '{}', true)`, otherUser); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Fatalf("other person sees %d tasks", seen)
	}
}

func TestEventsAreAppendOnlyAndDoneCannotBeWritten(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Chat, Body: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET status = 'Done' WHERE id = $1`, opened.ID); err == nil {
		t.Fatal("status moved to Done without an event")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.task_events SET input = 'pass'`); err == nil {
		t.Fatal("a task event was updated")
	}
	if _, err := owner.Exec(ctx, `DELETE FROM tasks.task_events`); err == nil {
		t.Fatal("a task event was deleted")
	}
	if _, err := owner.Exec(ctx, `DELETE FROM tasks.chat_events`); err == nil {
		t.Fatal("a chat event was deleted")
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status)
		VALUES ($1::uuid, 'private', 'skip', 'Done')`, ownerUser); err == nil {
		t.Fatal("a task was born Done")
	}
	appPool := pgtest.App(t, owner)
	if _, err := appPool.Exec(ctx, `UPDATE tasks.task_events SET input = input`); err == nil {
		t.Fatal("the app role updated a task event")
	}
}

const (
	ownerUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser = "018f3b2c-7a10-7c4a-8e11-222222222222"
)

var (
	ownerAccess = app.Access{UserID: ownerUser, Allow: "task:create,task:view,task:edit"}
	otherAccess = app.Access{UserID: otherUser, Allow: "task:view,task:edit"}
)

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}
