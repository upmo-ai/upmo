//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
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
	working, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance})
	if err != nil || working.Status != domain.Working {
		t.Fatalf("advance to Working = %+v %v", working, err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance}); !errors.Is(err, domain.ErrTurn) {
		t.Fatalf("advance without sentinel = %v", err)
	}
	var stored string
	var events int
	if err := owner.QueryRow(ctx, `SELECT status FROM tasks.tasks WHERE id = $1`, opened.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != string(domain.Working) {
		t.Fatalf("stored status after unfinished turn = %s", stored)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.task_events WHERE task_id = $1`, opened.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("task events after unfinished turn = %d", events)
	}
	checked, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance, Turn: "upmo:end"})
	if err != nil || checked.Status != domain.Checking {
		t.Fatalf("advance to Checking = %+v %v", checked, err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance}); !errors.Is(err, domain.ErrEvidence) {
		t.Fatalf("advance from Checking = %v", err)
	}
	noted, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Chat, Body: " I pass "})
	if err != nil || noted.Status != domain.Checking {
		t.Fatalf("chat = %+v %v", noted, err)
	}
	var passes bool
	if err := owner.QueryRow(ctx, `SELECT status, passes FROM tasks.tasks WHERE id = $1`, opened.ID).Scan(&stored, &passes); err != nil {
		t.Fatal(err)
	}
	if stored != string(domain.Checking) || passes {
		t.Fatalf("after chat status=%s passes=%t", stored, passes)
	}
	passed, err := app.RecordChecks(ctx, store, ownerAccess, opened.ID, []domain.Check{{Name: "go test", Code: 0}})
	if err != nil || !passed {
		t.Fatalf("record checks passed=%t err=%v", passed, err)
	}
	if err := owner.QueryRow(ctx, `SELECT status, passes FROM tasks.tasks WHERE id = $1`, opened.ID).Scan(&stored, &passes); err != nil {
		t.Fatal(err)
	}
	if stored != string(domain.Checking) || !passes {
		t.Fatalf("after checks status=%s passes=%t", stored, passes)
	}
	done, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Pass})
	if err != nil || done.Status != domain.Done {
		t.Fatalf("pass = %+v %v", done, err)
	}
	if err := owner.QueryRow(ctx, `SELECT status, passes FROM tasks.tasks WHERE id = $1`, opened.ID).Scan(&stored, &passes); err != nil {
		t.Fatal(err)
	}
	if stored != string(domain.Done) || !passes {
		t.Fatalf("after pass status=%s passes=%t", stored, passes)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.task_events WHERE task_id = $1`, opened.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 4 {
		t.Fatalf("task events = %d", events)
	}
	var chats int
	var body string
	if err := owner.QueryRow(ctx, `SELECT count(*), COALESCE(max(body), '') FROM tasks.chat_events WHERE task_id = $1`, opened.ID).Scan(&chats, &body); err != nil {
		t.Fatal(err)
	}
	if chats != 1 || body != "I pass" {
		t.Fatalf("chat events = %d %q", chats, body)
	}
	var checkName string
	var checkCode int
	if err := owner.QueryRow(ctx, `SELECT name, code FROM tasks.checks WHERE task_id = $1`, opened.ID).Scan(&checkName, &checkCode); err != nil {
		t.Fatal(err)
	}
	if checkName != "go test" || checkCode != 0 {
		t.Fatalf("check = %q %d", checkName, checkCode)
	}
}

func TestTransitionPlansFromPreparingAndRejectsAnAdvanceEdgeToPlanning(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	planned, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Plan})
	if err != nil || planned.Status != domain.Planning {
		t.Fatalf("plan = %+v %v", planned, err)
	}
	var status, input string
	if err := owner.QueryRow(ctx, `
		SELECT task.status, event.input
		FROM tasks.tasks AS task
		JOIN tasks.task_events AS event ON event.task_id = task.id
		WHERE task.id = $1 AND event.seq = 2`, opened.ID).Scan(&status, &input); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.Planning) || input != string(domain.Plan) {
		t.Fatalf("stored status=%s input=%s", status, input)
	}

	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		VALUES ($1, 3, 'Preparing', 'Planning', 'advance')`, opened.ID); err == nil {
		t.Fatal("an advance edge from Preparing to Planning was stored")
	}
}

func TestTransitionStoresTurnBodyWithoutTheSentinel(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	working, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance})
	if err != nil || working.Status != domain.Working {
		t.Fatalf("advance to Working = %+v %v", working, err)
	}
	got, err := app.ApplyTransition(ctx, store, ownerAccess, opened.ID, domain.Input{Kind: domain.Advance, Turn: "work complete\nupmo:end"})
	if err != nil || got.Status != domain.Checking {
		t.Fatalf("advance to Checking = %+v %v", got, err)
	}
	var body string
	if err := owner.QueryRow(ctx, `SELECT body FROM tasks.chat_events WHERE task_id = $1`, opened.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "work complete" {
		t.Fatalf("turn body = %q", body)
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

func TestFailedChecksAreStoredAndDoNotPass(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := openAtChecking(t, store, "File the receipts")
	if passed, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}); err != nil || !passed {
		t.Fatalf("successful checks passed=%t err=%v", passed, err)
	}
	passed, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 1}})
	if err != nil || passed {
		t.Fatalf("record checks passed=%t err=%v", passed, err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Pass}); !errors.Is(err, domain.ErrEvidence) {
		t.Fatalf("pass after failed check = %v", err)
	}
	var status string
	var storedPasses bool
	var code int
	var count, batch int
	if err := owner.QueryRow(ctx, `SELECT status, passes FROM tasks.tasks WHERE id = $1`, task.ID).Scan(&status, &storedPasses); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*), max(batch) FROM tasks.checks WHERE task_id = $1`, task.ID).Scan(&count, &batch); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT code FROM tasks.checks WHERE task_id = $1 AND batch = 2`, task.ID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.Checking) || storedPasses || count != 2 || batch != 2 || code != 1 {
		t.Fatalf("status=%s passes=%t count=%d batch=%d code=%d", status, storedPasses, count, batch, code)
	}
}

func TestChecksBeforeCheckingAreRefused(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.RecordChecks(ctx, store, ownerAccess, opened.ID, []domain.Check{{Name: "go test", Code: 0}}); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("checks before Checking = %v", err)
	}
	var checks int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.checks WHERE task_id = $1`, opened.ID).Scan(&checks); err != nil {
		t.Fatal(err)
	}
	if checks != 0 {
		t.Fatalf("checks stored = %d", checks)
	}
}

func TestTransitionHidesATaskFromAnotherPerson(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	task := openAtChecking(t, store, "Sort the mail")
	if _, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, otherAccess, task.ID, domain.Input{Kind: domain.Pass}); !errors.Is(err, app.ErrGone) {
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
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.checks`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Fatalf("other person sees %d checks", seen)
	}
}

func TestEvidenceAndEventsCannotBeRewritten(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	opened, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET passes = true WHERE id = $1`, opened.ID); err == nil {
		t.Fatal("passes became true without checks")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET status = 'Done' WHERE id = $1`, opened.ID); err == nil {
		t.Fatal("status moved to Done while passes was false")
	}

	task := openAtChecking(t, store, "Keep the evidence")
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Chat, Body: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.task_events SET input = 'pass' WHERE task_id = $1`, task.ID); err == nil {
		t.Fatal("a task event was updated")
	}
	if _, err := owner.Exec(ctx, `DELETE FROM tasks.task_events WHERE task_id = $1`, task.ID); err == nil {
		t.Fatal("a task event was deleted")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.chat_events SET body = 'changed' WHERE task_id = $1`, task.ID); err == nil {
		t.Fatal("a chat event was updated")
	}
	if _, err := owner.Exec(ctx, `DELETE FROM tasks.chat_events WHERE task_id = $1`, task.ID); err == nil {
		t.Fatal("a chat event was deleted")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.checks SET code = 1 WHERE task_id = $1`, task.ID); err == nil {
		t.Fatal("a check was updated")
	}
	if _, err := owner.Exec(ctx, `DELETE FROM tasks.checks WHERE task_id = $1`, task.ID); err == nil {
		t.Fatal("a check was deleted")
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
	if _, err := appPool.Exec(ctx, `UPDATE tasks.checks SET code = code`); err == nil {
		t.Fatal("the app role updated a check")
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

func openAtChecking(t *testing.T, store postgres.Store, goal string) app.Task {
	t.Helper()
	ctx := context.Background()
	task, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: goal})
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range []domain.Status{domain.Working, domain.Checking} {
		input := domain.Input{Kind: domain.Advance}
		if next == domain.Checking {
			input.Turn = domain.Sentinel
		}
		task, err = app.ApplyTransition(ctx, store, ownerAccess, task.ID, input)
		if err != nil || task.Status != next {
			t.Fatalf("advance to %s = %+v %v", next, task, err)
		}
	}
	return task
}

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	// The stores record audit events, so the audit schema comes up first.
	if err := audit.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}
