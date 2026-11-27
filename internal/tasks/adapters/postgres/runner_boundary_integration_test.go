//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestRunnerOwnedColumnBoundary(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := openRoot(t, store, "Runner owned column check")

	var runnerOwned bool
	var reserved int64
	if err := owner.QueryRow(ctx, `SELECT runner_owned, reserved FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&runnerOwned, &reserved); err != nil {
		t.Fatal(err)
	}
	if runnerOwned || reserved != 0 {
		t.Fatalf("defaults = runner_owned:%t reserved:%d, want false/0", runnerOwned, reserved)
	}

	for _, role := range []string{"upmo_app", "upmo_runner"} {
		asRole(t, owner, role, ownerAccess, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET runner_owned = true WHERE id = $1::uuid`, task.ID); sqlState(err) != "42501" {
				t.Fatalf("%s UPDATE runner_owned = %v (code %s), want 42501", role, err, sqlState(err))
			}
		})
	}

	asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET reserved = 100 WHERE id = $1::uuid`, task.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_app UPDATE reserved = %v (code %s), want 42501", err, sqlState(err))
		}
	})

	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET reserved = 100 WHERE id = $1::uuid`, task.ID); err != nil {
			t.Fatalf("upmo_runner UPDATE reserved = %v", err)
		}
	})

	if got := taskBool(t, owner, `SELECT runner_owned FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got {
		t.Fatalf("runner_owned mutated to true")
	}
	if got := taskInt(t, owner, `SELECT reserved FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 100 {
		t.Fatalf("reserved = %d, want 100", got)
	}
}

func TestRunnerOwnedManualTransitionsRefused(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	h := approvalServer(store, map[string]app.Access{"owner": ownerAccess})

	markRunnerOwned := func(taskID string) {
		t.Helper()
		if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET runner_owned = true WHERE id = $1::uuid`, taskID); err != nil {
			t.Fatal(err)
		}
	}

	countEvents := func(taskID string) (taskEvents, chatEvents int) {
		t.Helper()
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.task_events WHERE task_id = $1::uuid`, taskID).Scan(&taskEvents); err != nil {
			t.Fatal(err)
		}
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1::uuid`, taskID).Scan(&chatEvents); err != nil {
			t.Fatal(err)
		}
		return taskEvents, chatEvents
	}

	// Preparing task: Advance and Plan refused (both store and HTTP 409 for Advance); Fund and Fail allowed.
	prepOwned := reach(t, store, "Preparing runner task", domain.Preparing)
	markRunnerOwned(prepOwned.ID)
	evBefore, chatBefore := countEvents(prepOwned.ID)

	for _, kind := range []domain.Kind{domain.Advance, domain.Plan} {
		if _, err := app.ApplyTransition(ctx, store, ownerAccess, prepOwned.ID, domain.Input{Kind: kind}); !errors.Is(err, app.ErrRunnerOwned) {
			t.Fatalf("%s on runner_owned task = %v, want ErrRunnerOwned", kind, err)
		}
	}
	if code, body := postTaskStep(h, "owner", prepOwned.ID, `{"kind":"advance"}`); code != http.StatusConflict {
		t.Fatalf("HTTP advance on runner_owned task = %d %s, want 409", code, body)
	}
	evAfter, chatAfter := countEvents(prepOwned.ID)
	if evAfter != evBefore || chatAfter != chatBefore {
		t.Fatalf("refused transitions wrote events: task %d->%d chat %d->%d", evBefore, evAfter, chatBefore, chatAfter)
	}
	if got := taskText(t, owner, `SELECT status FROM tasks.tasks WHERE id = $1::uuid`, prepOwned.ID); got != string(domain.Preparing) {
		t.Fatalf("status after refused transitions = %q, want Preparing", got)
	}

	// Fund allowed on runner_owned task.
	if err := app.Fund(ctx, store, ownerAccess, prepOwned.ID, 3_000_000); err != nil {
		t.Fatalf("Fund on runner_owned task = %v", err)
	}
	if got := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1::uuid`, prepOwned.ID); got != 3_000_000 {
		t.Fatalf("budget after Fund = %d, want 3000000", got)
	}

	// Fail allowed on runner_owned task.
	failed, err := app.ApplyTransition(ctx, store, ownerAccess, prepOwned.ID, domain.Input{Kind: domain.Fail})
	if err != nil || failed.Status != domain.Failed {
		t.Fatalf("Fail on runner_owned task = %+v %v", failed, err)
	}

	// Working task: Chat refused and writes no chat_events.
	workOwned := reach(t, store, "Working runner task", domain.Working)
	markRunnerOwned(workOwned.ID)
	evBefore, chatBefore = countEvents(workOwned.ID)
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, workOwned.ID, domain.Input{Kind: domain.Chat, Body: "hello"}); !errors.Is(err, app.ErrRunnerOwned) {
		t.Fatalf("Chat on runner_owned task = %v, want ErrRunnerOwned", err)
	}
	evAfter, chatAfter = countEvents(workOwned.ID)
	if evAfter != evBefore || chatAfter != chatBefore {
		t.Fatalf("refused chat wrote events: task %d->%d chat %d->%d", evBefore, evAfter, chatBefore, chatAfter)
	}

	// Verifying task with passes=true: Pass refused.
	verifyOwned := reach(t, store, "Verifying runner task", domain.Verifying)
	if _, err := app.RecordChecks(ctx, store, ownerAccess, verifyOwned.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
		t.Fatal(err)
	}
	markRunnerOwned(verifyOwned.ID)
	evBefore, chatBefore = countEvents(verifyOwned.ID)
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, verifyOwned.ID, domain.Input{Kind: domain.Pass}); !errors.Is(err, app.ErrRunnerOwned) {
		t.Fatalf("Pass on runner_owned task = %v, want ErrRunnerOwned", err)
	}
	evAfter, chatAfter = countEvents(verifyOwned.ID)
	if evAfter != evBefore || chatAfter != chatBefore {
		t.Fatalf("refused pass wrote events: task %d->%d chat %d->%d", evBefore, evAfter, chatBefore, chatAfter)
	}
	if got := taskText(t, owner, `SELECT status FROM tasks.tasks WHERE id = $1::uuid`, verifyOwned.ID); got != string(domain.Verifying) {
		t.Fatalf("status after refused Pass = %q, want Verifying", got)
	}

	// Needs approval task with pending approval: Approve and Deny refused (store + DecideApproval + HTTP 409 for Approve) with no approval/task writes.
	approvalOwned := reach(t, store, "Approval runner task", domain.Working)
	pending := request(t, store, approvalOwned.ID)
	markRunnerOwned(approvalOwned.ID)
	evBefore, chatBefore = countEvents(approvalOwned.ID)

	for _, kind := range []domain.Kind{domain.Approve, domain.Deny} {
		if _, err := app.DecideApproval(ctx, store, ownerAccess, pending.ID, kind); !errors.Is(err, app.ErrRunnerOwned) {
			t.Fatalf("DecideApproval(%s) on runner_owned task = %v, want ErrRunnerOwned", kind, err)
		}
		if _, err := store.Transition(ctx, ownerAccess, approvalOwned.ID, domain.Input{Kind: kind}); !errors.Is(err, app.ErrRunnerOwned) {
			t.Fatalf("Transition(%s) on runner_owned task = %v, want ErrRunnerOwned", kind, err)
		}
	}
	if code, body := send(h, "owner", http.MethodPost, "/api/approvals/"+pending.ID+"/approve"); code != http.StatusConflict {
		t.Fatalf("HTTP approve on runner_owned task = %d %s, want 409", code, body)
	}
	evAfter, chatAfter = countEvents(approvalOwned.ID)
	if evAfter != evBefore || chatAfter != chatBefore {
		t.Fatalf("refused decide wrote events: task %d->%d chat %d->%d", evBefore, evAfter, chatBefore, chatAfter)
	}
	if got := approvalState(t, owner, pending.ID); got != string(domain.ApprovalPending) {
		t.Fatalf("approval state after refused decide = %q, want pending", got)
	}
	if got := taskText(t, owner, `SELECT status FROM tasks.tasks WHERE id = $1::uuid`, approvalOwned.ID); got != string(domain.NeedsApproval) {
		t.Fatalf("status after refused decide = %q, want Needs approval", got)
	}

	// Resume allowed on runner_owned task (when no pending approval blocks it).
	resumeOwned := reach(t, store, "Resume runner task", domain.NeedsApproval)
	markRunnerOwned(resumeOwned.ID)
	resumed, err := app.ApplyTransition(ctx, store, ownerAccess, resumeOwned.ID, domain.Input{Kind: domain.Resume})
	if err != nil || resumed.Status != domain.Working {
		t.Fatalf("Resume on runner_owned task = %+v %v", resumed, err)
	}

	// Control: identical tasks with runner_owned=false succeed via HTTP Advance and Approve.
	prepManual := reach(t, store, "Manual advance task", domain.Preparing)
	if code, body := postTaskStep(h, "owner", prepManual.ID, `{"kind":"advance"}`); code != http.StatusOK {
		t.Fatalf("HTTP advance on manual task = %d %s, want 200", code, body)
	}
	approvalManual := reach(t, store, "Manual approval task", domain.Working)
	manualPending := request(t, store, approvalManual.ID)
	if code, body := send(h, "owner", http.MethodPost, "/api/approvals/"+manualPending.ID+"/approve"); code != http.StatusOK {
		t.Fatalf("HTTP approve on manual task = %d %s, want 200", code, body)
	}
}

func postTaskStep(h http.Handler, as, taskID, body string) (int, []byte) {
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID, strings.NewReader(body))
	if as != "" {
		req.Header.Set("X-As", as)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func approvalState(t *testing.T, owner *pgxpool.Pool, approvalID string) string {
	t.Helper()
	var state string
	if err := owner.QueryRow(context.Background(), `SELECT state FROM tasks.approvals WHERE id = $1::uuid`, approvalID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestTurnsAndFindingsEvidenceBoundary(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Owner evidence task", domain.Working)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	otherCreateAccess := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	otherTask, err := app.ApplyTransition(ctx, store, otherCreateAccess, "", domain.Input{Kind: domain.Open, Goal: "Other owner task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, otherCreateAccess, otherTask.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	otherRun, err := app.Admit(ctx, store, otherCreateAccess, otherTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	var turnsInsertPriv, findingsInsertPriv bool
	if err := owner.QueryRow(ctx, `
		SELECT has_table_privilege('upmo_app', 'tasks.turns', 'INSERT'),
		       has_table_privilege('upmo_app', 'tasks.findings', 'INSERT')`).Scan(&turnsInsertPriv, &findingsInsertPriv); err != nil {
		t.Fatal(err)
	}
	if turnsInsertPriv || findingsInsertPriv {
		t.Fatalf("upmo_app INSERT privileges: turns=%t findings=%t, want false/false", turnsInsertPriv, findingsInsertPriv)
	}

	roleProfiles := []struct {
		name   string
		access app.Access
	}{
		{
			name: "editor",
			access: app.Access{
				UserID: otherUser, Allow: "task:view,task:edit",
				SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
			},
		},
		{
			name: "member",
			access: app.Access{
				UserID: otherUser, Allow: "task:create,task:view,task:edit,task:share,task:delete",
				SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
			},
		},
		{
			name: "manager",
			access: app.Access{
				UserID: otherUser, Allow: "members:manage,settings:manage,task:view,task:edit",
				SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
			},
		},
		{
			name: "admin",
			access: app.Access{
				UserID: otherUser, Allow: "task:create,task:view,task:edit,task:share,task:delete,members:manage,teams:manage,roles:manage,settings:manage,connectors:manage,audit:view,usage:view",
				SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
			},
		},
		{
			name:   "owner",
			access: ownerAccess,
		},
	}

	for _, rp := range roleProfiles {
		asRole(t, owner, "upmo_app", rp.access, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body)
				VALUES ($1::uuid, $2::uuid, 1, 'assistant', 'worker', 'forged')`, task.ID, run.ID); sqlState(err) != "42501" {
				t.Fatalf("upmo_app (%s) INSERT turns = %v (code %s), want 42501", rp.name, err, sqlState(err))
			}
		})
		asRole(t, owner, "upmo_app", rp.access, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
				VALUES ($1::uuid, $2::uuid, 'output', 'forged', 'body')`, task.ID, run.ID); sqlState(err) != "42501" {
				t.Fatalf("upmo_app (%s) INSERT findings = %v (code %s), want 42501", rp.name, err, sqlState(err))
			}
		})
	}

	// Runner succeeds for owned task on turns and findings (output, tool_output).
	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body)
			VALUES ($1::uuid, $2::uuid, 1, 'assistant', 'worker', 'runner turn')`, task.ID, run.ID); err != nil {
			t.Fatalf("upmo_runner INSERT owned turn = %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
			VALUES ($1::uuid, $2::uuid, 'output', 'out', 'runner output')`, task.ID, run.ID); err != nil {
			t.Fatalf("upmo_runner INSERT owned output = %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
			VALUES ($1::uuid, $2::uuid, 'tool_output', 'tool', 'runner tool output')`, task.ID, run.ID); err != nil {
			t.Fatalf("upmo_runner INSERT owned tool_output = %v", err)
		}
	})

	// Runner denied for findings kind 'finding' and for another owner's task.
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
			VALUES ($1::uuid, $2::uuid, 'finding', 'forged', 'bad kind')`, task.ID, run.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_runner INSERT kind=finding = %v (code %s), want 42501", err, sqlState(err))
		}
	})
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body)
			VALUES ($1::uuid, $2::uuid, 1, 'assistant', 'worker', 'cross-owner')`, otherTask.ID, otherRun.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_runner INSERT other owner turn = %v (code %s), want 42501", err, sqlState(err))
		}
	})
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
			VALUES ($1::uuid, $2::uuid, 'output', 'cross-owner', 'body')`, otherTask.ID, otherRun.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_runner INSERT other owner finding = %v (code %s), want 42501", err, sqlState(err))
		}
	})

	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.turns WHERE task_id = $1::uuid`, task.ID); got != 1 {
		t.Fatalf("turns count = %d, want 1", got)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.findings WHERE task_id = $1::uuid`, task.ID); got != 2 {
		t.Fatalf("findings count = %d, want 2", got)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.turns WHERE task_id = $1::uuid`, otherTask.ID); got != 0 {
		t.Fatalf("otherTask turns count = %d, want 0", got)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.findings WHERE task_id = $1::uuid`, otherTask.ID); got != 0 {
		t.Fatalf("otherTask findings count = %d, want 0", got)
	}
}

func TestTaskEventsAndTurnsActorBoundary(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Actor boundary task", domain.Working)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// DB rejects NULL (23502) and unknown actor (23514) on both tables.
	for _, stmt := range []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "task_events NULL actor",
			sql:  `INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor) VALUES ($1::uuid, 99, 'Working', 'Failed', 'fail', NULL)`,
			want: "23502",
		},
		{
			name: "task_events unknown actor",
			sql:  `INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor) VALUES ($1::uuid, 99, 'Working', 'Failed', 'fail', 'bot')`,
			want: "23514",
		},
	} {
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, execErr := tx.Exec(ctx, stmt.sql, task.ID)
		_ = tx.Rollback(ctx)
		if sqlState(execErr) != stmt.want {
			t.Fatalf("%s = %v (code %s), want %s", stmt.name, execErr, sqlState(execErr), stmt.want)
		}
	}

	for _, stmt := range []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "turns NULL actor",
			sql:  `INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body) VALUES ($1::uuid, $2::uuid, 1, 'assistant', NULL, 'hi')`,
			want: "23502",
		},
		{
			name: "turns unknown actor",
			sql:  `INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body) VALUES ($1::uuid, $2::uuid, 1, 'assistant', 'bot', 'hi')`,
			want: "23514",
		},
	} {
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, execErr := tx.Exec(ctx, stmt.sql, task.ID, run.ID)
		_ = tx.Rollback(ctx)
		if sqlState(execErr) != stmt.want {
			t.Fatalf("%s = %v (code %s), want %s", stmt.name, execErr, sqlState(execErr), stmt.want)
		}
	}

	// upmo_app inserting task_events with actor='core' is denied (42501); 'person' passes.
	asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor)
			SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, 'Working', 'Failed', 'fail', 'core'
			FROM tasks.task_events WHERE task_id = $1::uuid`, task.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_app INSERT task_events actor=core = %v (code %s), want 42501", err, sqlState(err))
		}
	})
	asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor)
			SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, 'Working', 'Failed', 'fail', 'person'
			FROM tasks.task_events WHERE task_id = $1::uuid`, task.ID); err != nil {
			t.Fatalf("upmo_app INSERT task_events actor=person = %v", err)
		}
	})

	// Open + Step events read back actor='person'.
	var openActor, stepActor string
	if err := owner.QueryRow(ctx, `SELECT actor FROM tasks.task_events WHERE task_id = $1::uuid AND seq = 1`, task.ID).Scan(&openActor); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT actor FROM tasks.task_events WHERE task_id = $1::uuid AND seq = 2`, task.ID).Scan(&stepActor); err != nil {
		t.Fatal(err)
	}
	if openActor != "person" || stepActor != "person" {
		t.Fatalf("open/step actors = %q/%q, want person/person", openActor, stepActor)
	}

	// Spawn event reads back actor='person'.
	child, err := app.Spawn(ctx, store, ownerAccess, task.ID, "Child actor check", 100_000, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if got := taskText(t, owner, `SELECT actor FROM tasks.task_events WHERE task_id = $1::uuid AND seq = 1`, child.ID); got != "person" {
		t.Fatalf("spawn actor = %q, want person", got)
	}

	// Fire event reads back actor='person'.
	templateID := saveAutomationTemplate(t, store, "Auto {{kind}}", 40)
	automationID, err := app.SaveAutomation(ctx, store, ownerAccess, templateID, domain.KindEvent, "actor.check")
	if err != nil {
		t.Fatal(err)
	}
	firedID, _, err := app.Fire(ctx, store, ownerAccess, automationID, "k1", map[string]string{"kind": "test"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := taskText(t, owner, `SELECT actor FROM tasks.task_events WHERE task_id = $1::uuid AND seq = 1`, firedID); got != "person" {
		t.Fatalf("fire actor = %q, want person", got)
	}
}

func TestTurnsAndFindingsPrivateReadVisibility(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Private read task", domain.Working)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body, private_read)
			VALUES ($1::uuid, $2::uuid, 1, 'assistant', 'worker', 'public turn', false),
			       ($1::uuid, $2::uuid, 2, 'assistant', 'worker', 'secret turn', true)`, task.ID, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body, private_read)
			VALUES ($1::uuid, $2::uuid, 'output', 'pub', 'public finding', false),
			       ($1::uuid, $2::uuid, 'output', 'priv', 'secret finding', true)`, task.ID, run.ID); err != nil {
			t.Fatal(err)
		}
	})

	viewer := app.Access{
		UserID:     otherUser,
		Allow:      "task:view",
		SharedView: "{" + task.ID + "}",
	}

	asRole(t, owner, "upmo_app", viewer, false, func(tx pgx.Tx) {
		var turnCount, privTurnCount int
		var turnBody string
		if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(min(body), '') FROM tasks.turns WHERE task_id = $1::uuid`, task.ID).Scan(&turnCount, &turnBody); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.turns WHERE task_id = $1::uuid AND private_read`, task.ID).Scan(&privTurnCount); err != nil {
			t.Fatal(err)
		}
		if turnCount != 1 || privTurnCount != 0 || turnBody != "public turn" {
			t.Fatalf("viewer turns: count=%d priv=%d body=%q, want 1/0/public turn", turnCount, privTurnCount, turnBody)
		}

		var findCount, privFindCount int
		var findBody string
		if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(min(body), '') FROM tasks.findings WHERE task_id = $1::uuid`, task.ID).Scan(&findCount, &findBody); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.findings WHERE task_id = $1::uuid AND private_read`, task.ID).Scan(&privFindCount); err != nil {
			t.Fatal(err)
		}
		if findCount != 1 || privFindCount != 0 || findBody != "public finding" {
			t.Fatalf("viewer findings: count=%d priv=%d body=%q, want 1/0/public finding", findCount, privFindCount, findBody)
		}
	})

	asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
		var turnCount, findCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.turns WHERE task_id = $1::uuid`, task.ID).Scan(&turnCount); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.findings WHERE task_id = $1::uuid`, task.ID).Scan(&findCount); err != nil {
			t.Fatal(err)
		}
		if turnCount != 2 || findCount != 2 {
			t.Fatalf("owner visibility: turns=%d findings=%d, want 2/2", turnCount, findCount)
		}
	})
}

func TestTurnsAndFindingsBodyCap(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Body cap task", domain.Working)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Oversize inserts raise 23514 (check_violation) from the runner only; no HTTP route accepts turn/finding bodies in this card.
	exactBody := strings.Repeat("a", 262144)
	overBody := strings.Repeat("a", 262145)

	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body)
			VALUES ($1::uuid, $2::uuid, 1, 'assistant', 'worker', $3)`, task.ID, run.ID, exactBody); err != nil {
			t.Fatalf("turns 262144 bytes = %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
			VALUES ($1::uuid, $2::uuid, 'output', 'exact', $3)`, task.ID, run.ID, exactBody); err != nil {
			t.Fatalf("findings 262144 bytes = %v", err)
		}
	})

	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body)
			VALUES ($1::uuid, $2::uuid, 2, 'assistant', 'worker', $3)`, task.ID, run.ID, overBody); sqlState(err) != "23514" {
			t.Fatalf("turns 262145 bytes = %v (code %s), want 23514", err, sqlState(err))
		}
	})
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
			VALUES ($1::uuid, $2::uuid, 'output', 'over', $3)`, task.ID, run.ID, overBody); sqlState(err) != "23514" {
			t.Fatalf("findings 262145 bytes = %v (code %s), want 23514", err, sqlState(err))
		}
	})

	// The cap counts bytes, not characters: 65537 four-byte characters are 262148 bytes.
	wide := strings.Repeat("\U0001D11E", 65537)
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body)
			VALUES ($1::uuid, $2::uuid, 3, 'assistant', 'worker', $3)`, task.ID, run.ID, wide); sqlState(err) != "23514" {
			t.Fatalf("turns 262148 wide bytes = %v (code %s), want 23514", err, sqlState(err))
		}
	})
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.findings (task_id, run_id, kind, title, body)
			VALUES ($1::uuid, $2::uuid, 'output', 'wide', $3)`, task.ID, run.ID, wide); sqlState(err) != "23514" {
			t.Fatalf("findings 262148 wide bytes = %v (code %s), want 23514", err, sqlState(err))
		}
	})

	// Every actor in the list is accepted from the runner.
	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		for i, actor := range []string{"person", "planner", "verifier", "core"} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks.turns (task_id, run_id, seq, role, actor, body)
				VALUES ($1::uuid, $2::uuid, $3, 'assistant', $4, 'actor')`, task.ID, run.ID, 10+i, actor); err != nil {
				t.Fatalf("turns actor %s = %v", actor, err)
			}
		}
	})
}

func TestReservedBudgetCheck(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	// Create parent (default budget 2_000_000), spawn child of 200_000 (held = 200_000), spend 500_000.
	// Remaining for reservation = 2_000_000 - 200_000 - 500_000 = 1_300_000.
	parent := openRoot(t, store, "Reserved check parent")
	if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Child hold", 200_000, "task:view"); err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, parent.ID, 500_000); err != nil {
		t.Fatal(err)
	}

	// Exact fit passes as upmo_runner.
	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET reserved = 1300000 WHERE id = $1::uuid`, parent.ID); err != nil {
			t.Fatalf("exact reserved = %v", err)
		}
	})
	if got := taskInt(t, owner, `SELECT reserved FROM tasks.tasks WHERE id = $1::uuid`, parent.ID); got != 1_300_000 {
		t.Fatalf("reserved = %d, want 1300000", got)
	}

	// Exceeding budget (by 1) and negative reserved both fail with 23514.
	for _, bad := range []int64{1_300_001, -1} {
		asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET reserved = $2 WHERE id = $1::uuid`, parent.ID, bad); sqlState(err) != "23514" {
				t.Fatalf("reserved = %d: err=%v (code %s), want 23514", bad, err, sqlState(err))
			}
		})
	}

	// Reset reserved = 0, then verify overspent (spent > budget) and spent = MaxInt64 pass with reserved = 0,
	// while reserved > 0 on overspent/MaxInt64 fails.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET reserved = 0, spent = 2500000 WHERE id = $1::uuid`, parent.ID); err != nil {
		t.Fatalf("overspent reserved=0 = %v", err)
	}
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET reserved = 1 WHERE id = $1::uuid`, parent.ID); sqlState(err) != "23514" {
			t.Fatalf("overspent reserved=1 = %v (code %s), want 23514", err, sqlState(err))
		}
	})

	maxTask := openRoot(t, store, "MaxInt64 spent task")
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET reserved = 0, spent = $2 WHERE id = $1::uuid`, maxTask.ID, int64(math.MaxInt64)); err != nil {
		t.Fatalf("MaxInt64 spent with reserved=0 = %v", err)
	}
	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET reserved = 1 WHERE id = $1::uuid`, maxTask.ID); err == nil {
			t.Fatalf("MaxInt64 spent with reserved=1 unexpectedly succeeded")
		} else if code := sqlState(err); code != "22003" && code != "23514" {
			t.Fatalf("MaxInt64 spent with reserved=1 code = %s (%v), want 22003 or 23514", code, err)
		}
	})
}
