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
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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

func TestRunnerOwnedControlsAreOwnerOnly(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	working := reach(t, store, "Runner controls", domain.Working)
	markTaskRunnerOwned(t, owner, working.ID)
	runs := make([]app.Run, 0, 3)
	for range 3 {
		run, err := app.Admit(ctx, store, ownerAccess, working.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	if runs[2].State != domain.RunQueued {
		t.Fatalf("third run = %s, want queued", runs[2].State)
	}

	resume := reach(t, store, "Runner resume", domain.NeedsApproval)
	markTaskRunnerOwned(t, owner, resume.ID)
	newRun := reach(t, store, "Runner new admission", domain.Working)
	markTaskRunnerOwned(t, owner, newRun.ID)

	editor := app.Access{UserID: otherUser, Allow: otherAccess.Allow,
		SharedView: "{" + working.ID + "," + resume.ID + "," + newRun.ID + "}",
		SharedEdit: "{" + working.ID + "," + resume.ID + "," + newRun.ID + "}"}
	adminEditor := app.Access{UserID: adminUser, Allow: adminAccess.Allow,
		SharedView: editor.SharedView, SharedEdit: editor.SharedEdit}
	stranger := otherAccess
	initialBudget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, working.ID)
	for _, person := range []struct {
		name   string
		access app.Access
		want   error
	}{{"shared editor", editor, app.ErrAccess}, {"admin", adminEditor, app.ErrAccess}, {"unshared admin", adminAccess, app.ErrGone}, {"stranger", stranger, app.ErrGone}} {
		t.Run(person.name, func(t *testing.T) {
			budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, working.ID)
			for _, amount := range []int64{budget, budget + 1} {
				if err := app.Fund(ctx, store, person.access, working.ID, amount); !errors.Is(err, person.want) {
					t.Errorf("Fund(%d) = %v, want %v", amount, err, person.want)
				}
			}
			if person.name == "shared editor" {
				if code, _ := postFund(t, store, person.access, working.ID, `{"kind":"fund","amount":3000000}`); code != 403 {
					t.Errorf("HTTP Fund = %d, want 403", code)
				}
				h := approvalServer(store, map[string]app.Access{"editor": person.access})
				if code, _ := postTaskStep(h, "editor", resume.ID, `{"kind":"resume"}`); code != 400 {
					t.Errorf("HTTP Resume = %d, want 400", code)
				}
			}
			if _, err := app.ApplyTransition(ctx, store, person.access, resume.ID, domain.Input{Kind: domain.Resume}); !errors.Is(err, person.want) {
				t.Errorf("Resume = %v, want %v", err, person.want)
			}
			if _, err := app.EndRun(ctx, store, person.access, runs[0].ID); !errors.Is(err, person.want) {
				t.Errorf("End = %v, want %v", err, person.want)
			}
			if _, err := app.Admit(ctx, store, person.access, newRun.ID, ""); !errors.Is(err, person.want) {
				t.Errorf("Admit new = %v, want %v", err, person.want)
			}
			if _, err := app.Admit(ctx, store, person.access, working.ID, runs[0].ID); !errors.Is(err, person.want) {
				t.Errorf("Admit existing admitted = %v, want %v", err, person.want)
			}
			if _, err := app.Admit(ctx, store, person.access, working.ID, runs[2].ID); !errors.Is(err, person.want) {
				t.Errorf("Admit existing queued = %v, want %v", err, person.want)
			}
		})
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, working.ID); budget != initialBudget {
		t.Fatalf("denied Fund changed budget from %d to %d", initialBudget, budget)
	}
	var state string
	var newRunCount int
	if err := owner.QueryRow(ctx, `SELECT state FROM tasks.runs WHERE id = $1::uuid`, runs[0].ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.runs WHERE task_id = $1::uuid`, newRun.ID).Scan(&newRunCount); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunAdmitted) || newRunCount != 0 {
		t.Fatalf("denied controls changed run state=%s new-task runs=%d", state, newRunCount)
	}
	if got := taskStatus(t, owner, resume.ID); got != string(domain.NeedsApproval) {
		t.Fatalf("denied Resume changed status to %s", got)
	}
	if err := app.Fund(ctx, store, ownerAccess, working.ID, initialBudget); err != nil {
		t.Fatalf("member owner equal Fund = %v", err)
	}
	if err := app.Fund(ctx, store, ownerAccess, working.ID, initialBudget+123); err != nil {
		t.Fatalf("member owner Fund raise = %v", err)
	}
	if got := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, working.ID); got != initialBudget+123 {
		t.Fatalf("member owner Fund budget = %d, want %d", got, initialBudget+123)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, resume.ID, domain.Input{Kind: domain.Resume}); err != nil {
		t.Fatalf("member owner Resume = %v", err)
	}
	if got := taskStatus(t, owner, resume.ID); got != string(domain.Working) {
		t.Fatalf("member owner Resume status = %s, want Working", got)
	}
	if err := owner.QueryRow(ctx, `SELECT state FROM tasks.runs WHERE id = $1::uuid`, runs[2].ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.RunQueued) {
		t.Fatalf("denied Admit changed queued run state to %s", state)
	}
	adminTask, err := app.ApplyTransition(ctx, store, adminAccess, "", domain.Input{Kind: domain.Open, Goal: "Admin owner controls"})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.Status{domain.Working} {
		adminTask, err = app.ApplyTransition(ctx, store, adminAccess, adminTask.ID, domain.Input{Kind: domain.Advance})
		if err != nil || adminTask.Status != status {
			t.Fatalf("admin owner advance = %+v %v, want %s", adminTask, err, status)
		}
	}
	markTaskRunnerOwned(t, owner, adminTask.ID)
	adminBudget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, adminTask.ID)
	if err := app.Fund(ctx, store, adminAccess, adminTask.ID, adminBudget); err != nil {
		t.Fatalf("admin owner equal Fund = %v", err)
	}
	if err := app.Fund(ctx, store, adminAccess, adminTask.ID, adminBudget+456); err != nil {
		t.Fatalf("admin owner Fund raise = %v", err)
	}
	if got := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, adminTask.ID); got != adminBudget+456 {
		t.Fatalf("admin owner Fund budget = %d, want %d", got, adminBudget+456)
	}
	adminResume, err := app.ApplyTransition(ctx, store, adminAccess, "", domain.Input{Kind: domain.Open, Goal: "Admin owner resume"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, adminAccess, adminResume.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, adminAccess, adminResume.ID, domain.Input{Kind: domain.Pause}); err != nil {
		t.Fatal(err)
	}
	markTaskRunnerOwned(t, owner, adminResume.ID)
	if _, err := app.ApplyTransition(ctx, store, adminAccess, adminResume.ID, domain.Input{Kind: domain.Resume}); err != nil {
		t.Fatalf("admin owner Resume = %v", err)
	}
	if got := taskStatus(t, owner, adminResume.ID); got != string(domain.Working) {
		t.Fatalf("admin owner Resume status = %s, want Working", got)
	}
	adminRuns := make([]app.Run, 0, 3)
	for range 3 {
		admitted, err := app.Admit(ctx, store, adminAccess, adminTask.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		adminRuns = append(adminRuns, admitted)
	}
	if adminRuns[2].State != domain.RunQueued {
		t.Fatalf("admin owner queued run = %s, want queued", adminRuns[2].State)
	}
	if _, err := app.EndRun(ctx, store, adminAccess, adminRuns[0].ID); err != nil {
		t.Fatalf("admin owner End = %v", err)
	}
	adminSwept, err := app.Sweep(ctx, store, adminAccess)
	if err != nil || len(adminSwept) != 1 || adminSwept[0].ID != adminRuns[2].ID || adminSwept[0].State != domain.RunAdmitted {
		t.Fatalf("admin owner Sweep = %+v %v, want queued run admitted", adminSwept, err)
	}
	if swept, err := app.Sweep(ctx, store, editor); err != nil || len(swept) != 0 {
		t.Fatalf("editor Sweep = %+v %v, want no visible owned queue", swept, err)
	}
	if _, err := app.EndRun(ctx, store, ownerAccess, runs[0].ID); err != nil {
		t.Fatalf("owner End runner-owned run = %v", err)
	}
	if swept, err := app.Sweep(ctx, store, ownerAccess); err != nil || len(swept) != 1 || swept[0].ID != runs[2].ID || swept[0].State != domain.RunAdmitted {
		t.Fatalf("owner Sweep = %+v %v, want queued runner-owned run admitted", swept, err)
	}
	manual := reach(t, store, "Shared manual controls", domain.NeedsApproval)
	manualEditor := app.Access{UserID: otherUser, Allow: otherAccess.Allow,
		SharedView: "{" + manual.ID + "}", SharedEdit: "{" + manual.ID + "}"}
	manualBudget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, manual.ID)
	if err := app.Fund(ctx, store, manualEditor, manual.ID, manualBudget+1); err != nil {
		t.Fatalf("shared manual Fund = %v", err)
	}
	if _, err := app.ApplyTransition(ctx, store, manualEditor, manual.ID, domain.Input{Kind: domain.Resume}); err != nil {
		t.Fatalf("shared manual Resume = %v", err)
	}
	manualRun, err := app.Admit(ctx, store, manualEditor, manual.ID, "")
	if err != nil {
		t.Fatalf("shared manual Admit = %v", err)
	}
	if _, err := app.EndRun(ctx, store, manualEditor, manualRun.ID); err != nil {
		t.Fatalf("shared manual End = %v", err)
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
