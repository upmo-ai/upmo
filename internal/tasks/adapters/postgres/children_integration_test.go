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

	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, " File them ", 1, "task:view")
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

	first, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Read the folder", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, ownerAccess, first.ID, "File them", 1, "task:view"); !errors.Is(err, domain.ErrDepth) {
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
		if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "One more", 1, "task:view"); !errors.Is(err, domain.ErrFanout) {
		t.Fatalf("sixth child error = %v, want %v", err, domain.ErrFanout)
	}

	other := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	otherParent, err := app.ApplyTransition(ctx, store, other, "", domain.Input{Kind: domain.Open, Goal: "Close the week"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, other, otherParent.ID, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, other, otherParent.ID, "Pull the figures", 1, "task:view"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, other, otherParent.ID, "One more", 1, "task:view"); !errors.Is(err, domain.ErrCeiling) {
		t.Fatalf("shared cap error = %v, want %v", err, domain.ErrCeiling)
	}

	if _, err := app.Cancel(ctx, store, ownerAccess, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "After a cancel", 1, "task:view"); !errors.Is(err, domain.ErrFanout) {
		t.Fatalf("fan-out after cancel error = %v, want %v", err, domain.ErrFanout)
	}
	if _, err := app.Spawn(ctx, store, other, otherParent.ID, "After a slot freed", 1, "task:view"); err != nil {
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

	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
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
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
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
	first, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Read the folder", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}

	canceled, err := app.Cancel(ctx, store, ownerAccess, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != domain.Failed {
		t.Fatalf("canceled status = %s", canceled.Status)
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
	if status := taskStatus(t, owner, second.ID); status != string(domain.Failed) {
		t.Fatalf("child after parent cancel = %s", status)
	}
	var cause string
	if err := owner.QueryRow(ctx, `SELECT cause FROM tasks.tasks WHERE id = $1`, second.ID).Scan(&cause); err != nil {
		t.Fatal(err)
	}
	if cause != domain.ParentCanceled {
		t.Fatalf("cause = %q", cause)
	}
}

func TestDirectWriteKeepsTheCeilings(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET parent_id = NULL WHERE id = $1`, child.ID); err == nil || !containsTask(err, "tasks: writer") {
		t.Fatalf("parent rewrite error = %v", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id, budget, allow)
		VALUES ($1::uuid, 'private', 'Nested', 'Preparing', $2::uuid, 1, 'task:view')`, ownerUser, child.ID); err == nil || !containsTask(err, "tasks: depth") {
		t.Fatalf("grandchild insert error = %v", err)
	}

	for i := 1; i < domain.Fanout; i++ {
		if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id, budget, allow)
		VALUES ($1::uuid, 'private', 'One more', 'Preparing', $2::uuid, 1, 'task:view')`, ownerUser, parent.ID); err == nil || !containsTask(err, "tasks: fanout") {
		t.Fatalf("sixth insert error = %v", err)
	}
}

func openRoot(t *testing.T, store postgres.Store, goal string) app.Task {
	t.Helper()
	task, err := app.ApplyTransition(context.Background(), store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: goal})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(context.Background(), store, ownerAccess, task.ID, 1000); err != nil {
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

func TestOpenChildCount_UPM252_CountsEveryOpenChildForAnOwnerThatIsNeitherSuperuserNorBypassRLS(t *testing.T) {
	owner := up(t)
	pool := pgtest.App(t, owner)
	store := postgres.Store{Pool: pool}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	for _, goal := range []string{"Read the folder", "File them"} {
		if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, goal, 1, "task:view"); err != nil {
			t.Fatal(err)
		}
	}
	var want int
	if err := owner.QueryRow(ctx, `SELECT tasks.open_child_count()`).Scan(&want); err != nil || want != 2 {
		t.Fatalf("open children = %d: %v", want, err)
	}
	const role = "tasks_count_owner"
	if _, err := owner.Exec(ctx, `DROP ROLE IF EXISTS `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE ROLE `+role+` NOSUPERUSER NOBYPASSRLS NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{`REASSIGN OWNED BY ` + role + ` TO CURRENT_USER`, `DROP OWNED BY ` + role, `DROP ROLE ` + role} {
			_, _ = owner.Exec(context.Background(), stmt)
		}
	})
	for _, stmt := range []string{
		`ALTER TABLE tasks.tasks OWNER TO ` + role,
		`ALTER FUNCTION tasks.open_child_count() OWNER TO ` + role,
		`GRANT USAGE ON SCHEMA tasks, identity TO ` + role,
		`GRANT EXECUTE ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) TO ` + role,
	} {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// The request role sees no task of anyone else's, yet the ceiling counts them all.
	var got int
	if err := pool.QueryRow(ctx, `SELECT tasks.open_child_count()`).Scan(&got); err != nil || got != want {
		t.Fatalf("open children = %d, want %d: %v", got, want, err)
	}
}
