//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestSpawnOpensAChildAndDoesNotAdmit(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")

	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, " File them ")
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != domain.Preparing || child.Goal != "File them" {
		t.Fatalf("child = %+v", child)
	}

	var parentID string
	if err := owner.QueryRow(ctx, `SELECT parent_id::text FROM tasks.tasks WHERE id = $1`, child.ID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	if parentID != parent.ID {
		t.Fatalf("parent = %s, want %s", parentID, parent.ID)
	}
	var runs int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.runs WHERE task_id = $1`, child.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("runs = %d, want 0", runs)
	}
}

func TestSpawnCeilings(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")

	first, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Read the folder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, ownerAccess, first.ID, "File them"); !errors.Is(err, domain.ErrDepth) {
		t.Fatalf("grandchild error = %v, want %v", err, domain.ErrDepth)
	}
	var grandchildren int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE parent_id = $1`, first.ID).Scan(&grandchildren); err != nil {
		t.Fatal(err)
	}
	if grandchildren != 0 {
		t.Fatalf("grandchildren = %d", grandchildren)
	}

	for i := 1; i < domain.Fanout; i++ {
		if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "One more"); !errors.Is(err, domain.ErrFanout) {
		t.Fatalf("sixth child error = %v, want %v", err, domain.ErrFanout)
	}

	other := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	otherParent, err := app.ApplyTransition(ctx, store, other, "", domain.Input{Kind: domain.Open, Goal: "Close the week"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, other, otherParent.ID, "Pull the figures"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, other, otherParent.ID, "One more"); !errors.Is(err, domain.ErrCeiling) {
		t.Fatalf("shared cap error = %v, want %v", err, domain.ErrCeiling)
	}

	if _, err := app.Cancel(ctx, store, ownerAccess, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "After a cancel"); !errors.Is(err, domain.ErrFanout) {
		t.Fatalf("fan-out after cancel error = %v, want %v", err, domain.ErrFanout)
	}
	if _, err := app.Spawn(ctx, store, other, otherParent.ID, "After a slot freed"); err != nil {
		t.Fatalf("shared cap after a finish: %v", err)
	}
}

func TestAwaitReportsOpenChildren(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")

	none, err := app.Await(ctx, store, ownerAccess, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if none == nil || len(none) != 0 {
		t.Fatalf("empty children = %#v", none)
	}

	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Await(ctx, store, ownerAccess, parent.ID); !errors.Is(err, domain.ErrWaiting) {
		t.Fatalf("open child error = %v, want %v", err, domain.ErrWaiting)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, child.ID); err != nil {
		t.Fatal(err)
	}
	got, err := app.Await(ctx, store, ownerAccess, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != child.ID || got[0].Status != domain.Failed {
		t.Fatalf("children = %+v", got)
	}
	if _, err := app.Await(ctx, store, otherAccess, parent.ID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("other person error = %v, want %v", err, app.ErrGone)
	}
}

func TestSendReachesAChildOnly(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them")
	if err != nil {
		t.Fatal(err)
	}

	if err := app.Send(ctx, store, ownerAccess, parent.ID, "hello"); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("send to parent error = %v, want %v", err, domain.ErrInput)
	}
	if err := app.Send(ctx, store, ownerAccess, child.ID, "  Read the folder  "); err != nil {
		t.Fatal(err)
	}

	var body, status string
	if err := owner.QueryRow(ctx, `
		SELECT chat.body, task.status
		FROM tasks.chat_events AS chat
		JOIN tasks.tasks AS task ON task.id = chat.task_id
		WHERE chat.task_id = $1`, child.ID).Scan(&body, &status); err != nil {
		t.Fatal(err)
	}
	if body != "Read the folder" || status != string(domain.Preparing) {
		t.Fatalf("body = %q status = %s", body, status)
	}
	var parentChats int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, parent.ID).Scan(&parentChats); err != nil {
		t.Fatal(err)
	}
	if parentChats != 0 {
		t.Fatalf("parent chat lines = %d", parentChats)
	}
}

func TestCancelDoesNotPropagate(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	first, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Read the folder")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them")
	if err != nil {
		t.Fatal(err)
	}

	cancelled, err := app.Cancel(ctx, store, ownerAccess, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != domain.Failed {
		t.Fatalf("cancelled status = %s", cancelled.Status)
	}
	if status := taskStatus(t, owner, parent.ID); status != string(domain.Preparing) {
		t.Fatalf("parent status = %s", status)
	}
	if status := taskStatus(t, owner, second.ID); status != string(domain.Preparing) {
		t.Fatalf("sibling status = %s", status)
	}

	if _, err := app.Cancel(ctx, store, ownerAccess, parent.ID); err != nil {
		t.Fatal(err)
	}
	if status := taskStatus(t, owner, second.ID); status != string(domain.Preparing) {
		t.Fatalf("child after parent cancel = %s", status)
	}
}

func TestDirectWriteKeepsTheCeilings(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET parent_id = NULL WHERE id = $1`, child.ID); err == nil || !containsTask(err, "tasks: writer") {
		t.Fatalf("parent rewrite error = %v", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id)
		VALUES ($1::uuid, 'private', 'Nested', 'Preparing', $2::uuid)`, ownerUser, child.ID); err == nil || !containsTask(err, "tasks: depth") {
		t.Fatalf("grandchild insert error = %v", err)
	}

	for i := 1; i < domain.Fanout; i++ {
		if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id)
		VALUES ($1::uuid, 'private', 'One more', 'Preparing', $2::uuid)`, ownerUser, parent.ID); err == nil || !containsTask(err, "tasks: fanout") {
		t.Fatalf("sixth insert error = %v", err)
	}
}

func openRoot(t *testing.T, store postgres.Store, goal string) app.Task {
	t.Helper()
	task, err := app.ApplyTransition(context.Background(), store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: goal})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func taskStatus(t *testing.T, owner *pgxpool.Pool, id string) string {
	t.Helper()
	var status string
	if err := owner.QueryRow(context.Background(), `SELECT status FROM tasks.tasks WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func containsTask(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}
