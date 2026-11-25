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

func TestAdmitQueuesTheThirdRunForOnePerson(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := openRunTask(t, store, ownerAccess, "File the receipts")

	for index, want := range []domain.RunState{
		domain.RunAdmitted,
		domain.RunAdmitted,
		domain.RunQueued,
	} {
		got, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
		if err != nil || got.State != want {
			t.Fatalf("admit %d = %+v %v, want %s", index+1, got, err, want)
		}
	}

	var admitted int
	if err := owner.QueryRow(
		ctx,
		`SELECT count(*) FROM tasks.runs WHERE state = 'admitted'`,
	).Scan(&admitted); err != nil {
		t.Fatal(err)
	}
	if admitted != domain.UserCap {
		t.Fatalf("admitted runs = %d, want %d", admitted, domain.UserCap)
	}
}

func TestEndThenSweepAdmitsTheQueuedRun(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := openRunTask(t, store, ownerAccess, "File the receipts")

	first, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Admit(ctx, store, ownerAccess, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	queued, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil || queued.State != domain.RunQueued {
		t.Fatalf("queued = %+v %v", queued, err)
	}

	ended, err := app.EndRun(ctx, store, ownerAccess, first.ID)
	if err != nil || ended.State != domain.RunEnded {
		t.Fatalf("ended = %+v %v", ended, err)
	}
	swept, err := app.Sweep(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if len(swept) != 1 || swept[0].ID != queued.ID || swept[0].State != domain.RunAdmitted {
		t.Fatalf("swept = %+v", swept)
	}

	var state string
	if err := owner.QueryRow(ctx, `SELECT state FROM tasks.runs WHERE id = $1`, queued.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunAdmitted) {
		t.Fatalf("queued run state = %s", state)
	}
}

func TestAdmissionCapIsPerPerson(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	ownerTask := openRunTask(t, store, ownerAccess, "File the receipts")

	for range domain.UserCap {
		got, err := app.Admit(ctx, store, ownerAccess, ownerTask.ID, "")
		if err != nil || got.State != domain.RunAdmitted {
			t.Fatalf("owner admit = %+v %v", got, err)
		}
	}

	secondAccess := app.Access{
		UserID: otherUser,
		Allow:  "task:create,task:view,task:edit",
	}
	secondTask := openRunTask(t, store, secondAccess, "Sort the mail")
	got, err := app.Admit(ctx, store, secondAccess, secondTask.ID, "")
	if err != nil || got.State != domain.RunAdmitted {
		t.Fatalf("second person admit = %+v %v", got, err)
	}
}

func TestAdmitRefusesADoneTaskWithoutStoringARun(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := openRunTask(t, store, ownerAccess, "File the receipts")

	task, _ = advanceRunTask(t, store, ownerAccess, task.ID, domain.Input{Kind: domain.Advance}, domain.Working)
	task, _ = advanceRunTask(
		t,
		store,
		ownerAccess,
		task.ID,
		domain.Input{Kind: domain.Advance, Turn: "upmo:end"},
		domain.Verifying,
	)
	passed, err := app.RecordChecks(
		ctx,
		store,
		ownerAccess,
		task.ID,
		[]domain.Check{{Name: "go test", Code: 0}},
		nil,
	)
	if err != nil || !passed {
		t.Fatalf("record checks passed=%t err=%v", passed, err)
	}
	task, err = app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Pass})
	if err != nil || task.Status != domain.Done {
		t.Fatalf("pass = %+v %v", task, err)
	}

	if _, err := app.Admit(ctx, store, ownerAccess, task.ID, ""); !errors.Is(err, domain.ErrTerminal) {
		t.Fatalf("admit Done task = %v", err)
	}
	var runs int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.runs WHERE task_id = $1`, task.ID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("stored runs = %d", runs)
	}
}

func TestDirectUpdateCannotAdmitAThirdRun(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := openRunTask(t, store, ownerAccess, "File the receipts")

	for range domain.UserCap {
		if _, err := app.Admit(ctx, store, ownerAccess, task.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil || queued.State != domain.RunQueued {
		t.Fatalf("queued = %+v %v", queued, err)
	}
	if _, err := owner.Exec(
		ctx,
		`UPDATE tasks.runs SET state = 'admitted' WHERE id = $1`,
		queued.ID,
	); err == nil {
		t.Fatal("a third run was admitted directly")
	}

	var state string
	if err := owner.QueryRow(ctx, `SELECT state FROM tasks.runs WHERE id = $1`, queued.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunQueued) {
		t.Fatalf("third run state = %s", state)
	}
}

func TestRunEventsCannotBeUpdatedOrDeleted(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := openRunTask(t, store, ownerAccess, "File the receipts")
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := owner.Exec(
		ctx,
		`UPDATE tasks.run_events SET input = 'end' WHERE run_id = $1`,
		run.ID,
	); err == nil {
		t.Fatal("a run event was updated")
	}
	if _, err := owner.Exec(ctx, `DELETE FROM tasks.run_events WHERE run_id = $1`, run.ID); err == nil {
		t.Fatal("a run event was deleted")
	}
}

func TestReadmittingAnAdmittedRunIsIdempotent(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Owner: owner}
	ctx := context.Background()
	task := openRunTask(t, store, ownerAccess, "File the receipts")
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := app.Admit(ctx, store, ownerAccess, task.ID, run.ID)
	if err != nil || got.State != domain.RunAdmitted {
		t.Fatalf("readmit = %+v %v", got, err)
	}
	var events int
	if err := owner.QueryRow(
		ctx,
		`SELECT count(*) FROM tasks.run_events WHERE run_id = $1`,
		run.ID,
	).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("run events = %d", events)
	}

	second, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil || second.State != domain.RunAdmitted {
		t.Fatalf("second run = %+v %v", second, err)
	}
}

func openRunTask(t *testing.T, store postgres.Store, access app.Access, goal string) app.Task {
	t.Helper()
	task, err := app.ApplyTransition(
		context.Background(),
		store,
		access,
		"",
		domain.Input{Kind: domain.Open, Goal: goal},
	)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func advanceRunTask(
	t *testing.T,
	store postgres.Store,
	access app.Access,
	taskID string,
	input domain.Input,
	want domain.Status,
) (app.Task, error) {
	t.Helper()
	task, err := app.ApplyTransition(context.Background(), store, access, taskID, input)
	if err != nil || task.Status != want {
		t.Fatalf("advance to %s = %+v %v", want, task, err)
	}
	return task, err
}
