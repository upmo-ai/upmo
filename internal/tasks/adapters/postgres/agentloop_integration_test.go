//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestSpendPreservesFullActualSpendAndPauses(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Micro USD run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, task.ID, 1_600_000); err != nil {
		t.Fatal(err)
	}
	spent, paused := taskMoney(t, owner, task.ID)
	if spent != 1_600_000 || paused {
		t.Fatalf("first spend = spent:%d paused:%v", spent, paused)
	}
	if err := app.Spend(ctx, store, ownerAccess, task.ID, 650_000); err != nil {
		t.Fatal(err)
	}
	spent, paused = taskMoney(t, owner, task.ID)
	if spent != 2_250_000 || !paused {
		t.Fatalf("crossing spend = spent:%d paused:%v", spent, paused)
	}
	if err := app.Spend(ctx, store, ownerAccess, task.ID, 1); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("spend while paused = %v, want %v", err, domain.ErrExhausted)
	}
	spent, paused = taskMoney(t, owner, task.ID)
	if spent != 2_250_000 || !paused {
		t.Fatalf("after refusal = spent:%d paused:%v", spent, paused)
	}
}

func TestTasksSingleTeamRLSNarrowingAndBatchHoldAndActionLog(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	teamA := "018f3b2c-7a10-7c4a-8e11-aaaa00000001"
	teamB := "018f3b2c-7a10-7c4a-8e11-aaaa00000002"

	taskA, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Team A task"})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Team B task"})
	if err != nil {
		t.Fatal(err)
	}
	otherPrivate, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Shared private task"})
	if err != nil {
		t.Fatal(err)
	}
	// Set visibility='team' for taskA (teamA) and taskB (teamB), owned by ownerUser.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, taskA.ID, teamA); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, taskB.ID, teamB); err != nil {
		t.Fatal(err)
	}

	// Non-owner member of [teamA, teamB] with SharedView for otherPrivate, narrowed onto taskA:
	// MUST see taskA, MUST NOT see taskB or otherPrivate!
	scopeMember := agentloop.AccessScope{
		UserID:           otherUser,
		Allow:            "task:view,task:edit",
		UserTeams:        []string{teamA, teamB},
		ObjectID:         taskA.ID,
		ObjectVisibility: agentloop.VisibilityTeam,
		VisibilityTeamID: teamA,
		SharedView:       "{" + otherPrivate.ID + "}",
		SharedEdit:       "{" + taskA.ID + "," + otherPrivate.ID + "}",
	}
	if err := store.NarrowedTx(ctx, scopeMember, func(tx pgx.Tx) error {
		var countA, countB, countOther int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE id = $1::uuid`, taskA.ID).Scan(&countA); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE id = $1::uuid`, taskB.ID).Scan(&countB); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE id = $1::uuid`, otherPrivate.ID).Scan(&countOther); err != nil {
			return err
		}
		if countA != 1 || countB != 0 || countOther != 0 {
			t.Fatalf("narrowed member visibility: A=%d B=%d other=%d", countA, countB, countOther)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Shared editor who is ONLY in teamB (NOT teamA), running on taskA:
	// MUST NOT gain teamA membership! Cannot see another teamA-only task.
	taskA2, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Team A secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, taskA2.ID, teamA); err != nil {
		t.Fatal(err)
	}
	scopeNonMemberEditor := agentloop.AccessScope{
		UserID:           otherUser,
		Allow:            "task:view,task:edit",
		UserTeams:        []string{teamB},
		ObjectID:         taskA.ID,
		ObjectVisibility: agentloop.VisibilityTeam,
		VisibilityTeamID: teamA,
		SharedEdit:       "{" + taskA.ID + "}",
	}
	if err := store.NarrowedTx(ctx, scopeNonMemberEditor, func(tx pgx.Tx) error {
		var countA2 int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE id = $1::uuid`, taskA2.ID).Scan(&countA2); err != nil {
			return err
		}
		if countA2 != 0 {
			t.Fatalf("shared editor non-member gained teamA visibility: countA2=%d", countA2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Move taskA to Working and test batch hold + ActionLog + turns.
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, taskA.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	scopeOwner := agentloop.AccessScope{
		UserID:           ownerUser,
		Allow:            ownerAccess.Allow,
		UserTeams:        []string{teamA, teamB},
		ObjectID:         taskA.ID,
		ObjectVisibility: agentloop.VisibilityTeam,
		VisibilityTeamID: teamA,
	}

	batch := agentloop.BuildBatchHold("ask", []agentloop.HeldCall{
		agentloop.BuildHeldCall(agentloop.ToolCall{ID: "c1", Name: "send_payment", Arguments: json.RawMessage(`{"iban":"DE89"}`)}, agentloop.ToolDef{Op: agentloop.OpChange}, "acct", true),
		agentloop.BuildHeldCall(agentloop.ToolCall{ID: "c2", Name: "post_webhook", Arguments: json.RawMessage(`{"target_url":"https://example.com"}`)}, agentloop.ToolDef{Op: agentloop.OpChange}, "hook", true),
	})
	approval, err := store.RequestBatchApproval(ctx, ownerAccess, taskA.ID, "send_payment", batch)
	if err != nil || approval.ID == "" {
		t.Fatalf("RequestBatchApproval = %+v %v", approval, err)
	}
	var callCount int
	var untrustedOrigin bool
	if err := owner.QueryRow(ctx, `SELECT cardinality(calls), untrusted_origin FROM tasks.approvals WHERE id = $1::uuid`, approval.ID).Scan(&callCount, &untrustedOrigin); err != nil {
		t.Fatal(err)
	}
	if callCount != 2 || !untrustedOrigin {
		t.Fatalf("stored batch approval calls=%d untrusted_origin=%v", callCount, untrustedOrigin)
	}

	// ActionLog at-most-once test against Postgres.
	alog := postgres.TaskActionLog{Store: store, Scope: scopeOwner}
	rec := agentloop.ActionRecord{
		CallID:   "pg-call-1",
		ObjectID: taskA.ID,
		UserID:   ownerUser,
		ToolName: "send_payment",
		Action:   "change send_payment",
		Preview:  "change send_payment()",
	}
	if err := alog.BeginDial(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := alog.BeginDial(ctx, rec); !errors.Is(err, agentloop.ErrDuplicateAction) {
		t.Fatalf("replay while dialing = %v, want ErrDuplicateAction", err)
	}
	if err := alog.FinishDial(ctx, taskA.ID, rec.CallID, agentloop.ActionStateDone, "sha256:abc", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := alog.BeginDial(ctx, rec); !errors.Is(err, agentloop.ErrDuplicateAction) {
		t.Fatalf("replay after done = %v, want ErrDuplicateAction", err)
	}

	// Failed state retry under upmo_app must succeed without touching executed_at.
	failedRec := agentloop.ActionRecord{
		CallID:   "pg-call-failed-1",
		ObjectID: taskA.ID,
		UserID:   ownerUser,
		ToolName: "send_payment",
		Action:   "change send_payment",
		Preview:  "change send_payment()",
	}
	if err := alog.BeginDial(ctx, failedRec); err != nil {
		t.Fatal(err)
	}
	if err := alog.FinishDial(ctx, taskA.ID, failedRec.CallID, agentloop.ActionStateFailed, "", "timeout", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := alog.BeginDial(ctx, failedRec); err != nil {
		t.Fatalf("retry after failed = %v", err)
	}
	if err := alog.FinishDial(ctx, taskA.ID, failedRec.CallID, agentloop.ActionStateDone, "sha256:retried", "", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestTasksActionLogScopedByRunAndTaskAllowsSameProviderCallID(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task1, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Task 1"})
	if err != nil {
		t.Fatal(err)
	}
	task2, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Task 2"})
	if err != nil {
		t.Fatal(err)
	}
	run1, err := app.Admit(ctx, store, ownerAccess, task1.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	run2, err := app.Admit(ctx, store, ownerAccess, task2.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	alog1 := postgres.TaskActionLog{Store: store, Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task1.ID}}
	alog2 := postgres.TaskActionLog{Store: store, Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task2.ID}}

	rec1 := agentloop.ActionRecord{CallID: "call_0", RunID: run1.ID, ObjectID: task1.ID, UserID: ownerUser, ToolName: "webhook", Action: "change webhook"}
	rec2 := agentloop.ActionRecord{CallID: "call_0", RunID: run2.ID, ObjectID: task2.ID, UserID: ownerUser, ToolName: "webhook", Action: "change webhook"}

	if err := alog1.BeginDial(ctx, rec1); err != nil {
		t.Fatalf("run1 call_0 BeginDial: %v", err)
	}
	if err := alog2.BeginDial(ctx, rec2); err != nil {
		t.Fatalf("run2 call_0 BeginDial collided with run1: %v", err)
	}
	if err := alog1.FinishDial(ctx, run1.ID, "call_0", agentloop.ActionStateDone, "sha256:r1", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := alog2.FinishDial(ctx, run2.ID, "call_0", agentloop.ActionStateDone, "sha256:r2", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := alog1.BeginDial(ctx, rec1); !errors.Is(err, agentloop.ErrDuplicateAction) {
		t.Fatalf("run1 replay = %v, want ErrDuplicateAction", err)
	}
	if err := alog2.BeginDial(ctx, rec2); !errors.Is(err, agentloop.ErrDuplicateAction) {
		t.Fatalf("run2 replay = %v, want ErrDuplicateAction", err)
	}
}

func TestTasksActionLogCrossTenantRLSIsolationAndUniqueViolation(t *testing.T) {
	owner := up(t)
	appPool := pgtest.App(t, owner)
	store := postgres.Store{Pool: appPool}
	ctx := context.Background()

	otherCreateAccess := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	taskA, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "User A task"})
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := app.ApplyTransition(ctx, store, otherCreateAccess, "", domain.Input{Kind: domain.Open, Goal: "User B task"})
	if err != nil {
		t.Fatal(err)
	}
	runA, err := app.Admit(ctx, store, ownerAccess, taskA.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	runB, err := app.Admit(ctx, store, otherCreateAccess, taskB.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	callID := agentloop.ScopedCallID(0, "call_0")
	alogA := postgres.TaskActionLog{Store: store, Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: taskA.ID}}
	alogB := postgres.TaskActionLog{Store: store, Scope: agentloop.AccessScope{UserID: otherUser, Allow: otherCreateAccess.Allow, ObjectID: taskB.ID}}

	recA := agentloop.ActionRecord{CallID: callID, RunID: runA.ID, ObjectID: taskA.ID, UserID: ownerUser, ToolName: "webhook", Action: "change webhook"}
	recB := agentloop.ActionRecord{CallID: callID, RunID: runB.ID, ObjectID: taskB.ID, UserID: otherUser, ToolName: "webhook", Action: "change webhook"}

	if err := alogA.BeginDial(ctx, recA); err != nil {
		t.Fatalf("user A BeginDial: %v", err)
	}
	if err := alogB.BeginDial(ctx, recB); err != nil {
		t.Fatalf("user B BeginDial collided with user A: %v", err)
	}
	if err := alogA.FinishDial(ctx, runA.ID, callID, agentloop.ActionStateDone, "sha256:user-a", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	// User B cannot observe User A's row via SELECT, cannot BeginDial into taskA, and cannot FinishDial User A's row.
	forgedB := postgres.TaskActionLog{Store: store, Scope: agentloop.AccessScope{UserID: otherUser, Allow: otherCreateAccess.Allow, ObjectID: taskA.ID}}
	if err := store.NarrowedTx(ctx, forgedB.Scope, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.action_logs WHERE scope_id = $1::uuid AND call_id = $2`, runA.ID, callID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("user B observed user A action_log row: count=%d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := forgedB.BeginDial(ctx, agentloop.ActionRecord{
		CallID: callID, RunID: runA.ID, ObjectID: taskA.ID, UserID: otherUser, ToolName: "webhook", Action: "change webhook",
	}); err == nil {
		t.Fatal("user B BeginDial on user A task succeeded; expected RLS refusal")
	}
	if err := forgedB.FinishDial(ctx, runA.ID, callID, agentloop.ActionStateFailed, "", "tamper", time.Now()); !errors.Is(err, app.ErrGone) {
		t.Fatalf("user B FinishDial on user A row = %v, want app.ErrGone", err)
	}

	// User A's row remains intact, and User B's own row finishes cleanly.
	var stateA, digestA string
	if err := owner.QueryRow(ctx, `SELECT state, response_digest FROM tasks.action_logs WHERE scope_id = $1::uuid AND call_id = $2`, runA.ID, callID).Scan(&stateA, &digestA); err != nil {
		t.Fatal(err)
	}
	if stateA != string(agentloop.ActionStateDone) || digestA != "sha256:user-a" {
		t.Fatalf("user A row mutated: state=%q digest=%q", stateA, digestA)
	}
	if err := alogB.FinishDial(ctx, runB.ID, callID, agentloop.ActionStateDone, "sha256:user-b", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	// Concurrent INSERT hitting SQLSTATE 23505 maps to agentloop.ErrDuplicateAction.
	assertTaskBeginDial23505(t, ctx, owner, appPool, alogA, taskA.ID, runA.ID)
}

func assertTaskBeginDial23505(t *testing.T, ctx context.Context, owner, appPool *pgxpool.Pool, alog postgres.TaskActionLog, taskID, runID string) {
	t.Helper()
	concurrentCallID := agentloop.ScopedCallID(2, "call_concurrent")
	tx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentloop.ApplyRLSTx(ctx, tx, agentloop.NarrowRLS(alog.Scope)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.action_logs (
			scope_id, call_id, task_id, run_id, user_id, tool_name, action, preview, state
		) VALUES ($1::uuid, $2, $3::uuid, $1::uuid, $4::uuid, 'webhook', 'change webhook', '', 'dialing')`,
		runID, concurrentCallID, taskID, ownerUser); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- alog.BeginDial(ctx, agentloop.ActionRecord{
			CallID:   concurrentCallID,
			RunID:    runID,
			ObjectID: taskID,
			UserID:   ownerUser,
			ToolName: "webhook",
			Action:   "change webhook",
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if qerr := owner.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO tasks.action_logs%'`).Scan(&waiting); qerr == nil && waiting > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-errCh; !errors.Is(got, agentloop.ErrDuplicateAction) {
		t.Fatalf("concurrent BeginDial 23505 = %v, want ErrDuplicateAction", got)
	}
}

func TestTasksCommitTurnAndEnqueue(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	// CommitTurnAndEnqueue persists turns, state, hold/approval, and enqueue callback atomically.
	task, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Turn commit task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, ownerAccess, task.ID, 5_000_000); err != nil {
		t.Fatal(err)
	}
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	var enqueuedCount int
	turnStore := postgres.TaskTurnStore{
		Store: store,
		Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
		EnqueueNext: func(_ context.Context, _ pgx.Tx, commit agentloop.TurnCommit) error {
			enqueuedCount++
			if commit.RunID != run.ID {
				t.Fatalf("enqueue commit RunID = %q, want %q", commit.RunID, run.ID)
			}
			return nil
		},
	}

	// Turn 1: continue with EnqueueNext=true.
	if err := turnStore.CommitTurnAndEnqueue(ctx, agentloop.TurnCommit{
		RunID:         run.ID,
		ObjectID:      task.ID,
		UserID:        ownerUser,
		SeqStart:      1,
		Messages:      []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "step 1"}, {Role: agentloop.RoleTool, ToolName: "search", ToolID: "c1", Text: "ok"}},
		Model:         "m1",
		InputTokens:   100,
		OutputTokens:  50,
		CostMicroUSD:  250_000,
		SpentMicroUSD: 250_000,
		Exposure:      agentloop.ExposureState{Tainted: true, PrivateRead: true},
		EnqueueNext:   true,
	}); err != nil {
		t.Fatal(err)
	}
	if enqueuedCount != 1 {
		t.Fatalf("enqueuedCount = %d, want 1", enqueuedCount)
	}
	var turnCount int
	var turnActor string
	var spent int64
	var tainted bool
	if err := owner.QueryRow(ctx, `SELECT count(*), COALESCE(min(actor), '') FROM tasks.turns WHERE run_id = $1::uuid`, run.ID).Scan(&turnCount, &turnActor); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT spent, tainted FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent, &tainted); err != nil {
		t.Fatal(err)
	}
	if turnCount != 2 || turnActor != "worker" || spent != 250_000 || !tainted {
		t.Fatalf("after turn 1: turns=%d actor=%q spent=%d tainted=%v", turnCount, turnActor, spent, tainted)
	}

	// Turn 2: hold persists batch approval and pauses task.
	hold := agentloop.BuildBatchHold("ask", []agentloop.HeldCall{
		agentloop.BuildHeldCall(agentloop.ToolCall{ID: "c2", Name: "send_payment", Arguments: json.RawMessage(`{"iban":"DE89"}`)}, agentloop.ToolDef{Op: agentloop.OpChange}, "acct", true),
	})
	if err := turnStore.CommitTurnAndEnqueue(ctx, agentloop.TurnCommit{
		RunID:         run.ID,
		ObjectID:      task.ID,
		UserID:        ownerUser,
		SeqStart:      3,
		Messages:      []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "waiting"}, {Role: agentloop.RoleTool, ToolName: "send_payment", ToolID: "c2", Text: "awaiting approval"}},
		Model:         "m1",
		CostMicroUSD:  50_000,
		SpentMicroUSD: 300_000,
		Exposure:      agentloop.ExposureState{Tainted: true},
		Hold:          &hold,
		EnqueueNext:   false,
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var approvalCount int
	if err := owner.QueryRow(ctx, `SELECT status FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.approvals WHERE task_id = $1::uuid AND state = 'pending'`, task.ID).Scan(&approvalCount); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.NeedsApproval) || approvalCount != 1 || enqueuedCount != 1 || spent != 300_000 {
		t.Fatalf("after hold turn: status=%s approvals=%d enqueued=%d spent=%d", status, approvalCount, enqueuedCount, spent)
	}
}

func TestPausedCommitTurnAndEnqueueReturnsErrExhaustedWithoutCharge(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Paused turn"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, task.ID, domain.DefaultBudget); err != nil {
		t.Fatal(err)
	}
	turnStore := postgres.TaskTurnStore{
		Store: store,
		Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
	}
	err = turnStore.CommitTurnAndEnqueue(ctx, agentloop.TurnCommit{
		RunID:         run.ID,
		ObjectID:      task.ID,
		UserID:        ownerUser,
		SeqStart:      1,
		Messages:      []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "late"}},
		CostMicroUSD:  1,
		SpentMicroUSD: 2_000_001,
		Exposure:      agentloop.ExposureState{Tainted: true},
	})
	if !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("paused commit = %v, want %v", err, domain.ErrExhausted)
	}
	var spent int64
	var paused, tainted bool
	var turns int
	if err := owner.QueryRow(ctx, `SELECT spent, paused, tainted FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent, &paused, &tainted); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.turns WHERE run_id = $1::uuid`, run.ID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if spent != 2_000_000 || !paused || tainted || turns != 0 {
		t.Fatalf("after refused commit: spent=%d paused=%v tainted=%v turns=%d", spent, paused, tainted, turns)
	}
}

func TestCommitRunnerTurnContinueAndEnqueue(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	teamID := "018f3b2c-7a10-7c4a-8e11-aaaa00000003"
	if _, err := owner.Exec(ctx, `INSERT INTO identity.teams (id, name) VALUES ($1::uuid, 'Live runner team')`, teamID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, teamID, ownerUser); err != nil {
		t.Fatal(err)
	}

	for _, status := range []domain.Status{domain.Planning, domain.Working} {
		t.Run(string(status), func(t *testing.T) {
			task := reach(t, store, "Runner continues", status)
			markTaskRunnerOwned(t, owner, task.ID)
			run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			called := 0
			turnStore := postgres.TaskTurnStore{
				Store: store,
				Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
				EnqueueNext: func(ctx context.Context, tx pgx.Tx, _ agentloop.TurnCommit) error {
					called++
					var teams string
					if err := tx.QueryRow(ctx, `SELECT current_setting('upmo.teams')`).Scan(&teams); err != nil {
						return err
					}
					if teams != "{"+teamID+"}" {
						t.Fatalf("live teams in runner tx = %q, want {%s}", teams, teamID)
					}
					return nil
				},
			}
			result, err := turnStore.CommitRunnerTurn(ctx, agentloop.TurnCommit{
				RunID: run.ID, ObjectID: task.ID, UserID: ownerUser, SeqStart: 1,
				Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "continue"}}, CostMicroUSD: 1,
			})
			if err != nil || !result.Continue || called != 1 {
				t.Fatalf("CommitRunnerTurn = %+v calls=%d err=%v, want continue once", result, called, err)
			}
		})
	}
	validationTask, validationRun := runnerCommitTask(t, store, owner, domain.Working, true)
	for _, test := range []struct {
		name  string
		scope agentloop.AccessScope
		user  string
	}{
		{"empty allow", agentloop.AccessScope{UserID: ownerUser, ObjectID: validationTask.ID}, ownerUser},
		{"view only", agentloop.AccessScope{UserID: ownerUser, Allow: "task:view", ObjectID: validationTask.ID}, ownerUser},
		{"mismatched user", agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: validationTask.ID}, otherUser},
		{"invalid user", agentloop.AccessScope{UserID: "invalid", Allow: ownerAccess.Allow, ObjectID: validationTask.ID}, "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			turnStore := postgres.TaskTurnStore{
				Store: store, Scope: test.scope,
				EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error {
					t.Fatal("invalid scope enqueued")
					return nil
				},
			}
			if _, err := turnStore.CommitRunnerTurn(ctx, agentloop.TurnCommit{RunID: validationRun.ID, ObjectID: validationTask.ID, UserID: test.user}); !errors.Is(err, app.ErrAccess) {
				t.Fatalf("CommitRunnerTurn = %v, want ErrAccess", err)
			}
		})
	}

	missingHook := postgres.TaskTurnStore{Store: store, Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: validationTask.ID}}
	if _, err := missingHook.CommitRunnerTurn(ctx, agentloop.TurnCommit{ObjectID: validationTask.ID}); !errors.Is(err, app.ErrEnqueueHook) {
		t.Fatalf("nil enqueue hook for continuing task = %v, want ErrEnqueueHook", err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET paused = true WHERE id = $1::uuid`, validationTask.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := missingHook.CommitRunnerTurn(ctx, agentloop.TurnCommit{ObjectID: validationTask.ID}); !errors.Is(err, app.ErrEnqueueHook) {
		t.Fatalf("nil enqueue hook for paused task = %v, want ErrEnqueueHook", err)
	}

	for _, test := range []struct {
		name        string
		runnerOwned bool
		cost        int64
		hold        bool
	}{
		{name: "hold", runnerOwned: true, cost: 1, hold: true},
		{name: "paused", runnerOwned: true, cost: domain.DefaultBudget},
		{name: "failed", runnerOwned: true, cost: 1},
		{name: "verifying", runnerOwned: true, cost: 1},
		{name: "done", runnerOwned: true, cost: 1},
		{name: "non-runner-owned", cost: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			task := reach(t, store, "Runner stops", domain.Working)
			run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			commit := agentloop.TurnCommit{
				RunID: run.ID, ObjectID: task.ID, UserID: ownerUser, SeqStart: 1,
				Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "stop"}}, CostMicroUSD: test.cost,
			}
			if test.hold {
				hold := agentloop.BuildBatchHold("ask", []agentloop.HeldCall{
					agentloop.BuildHeldCall(agentloop.ToolCall{ID: "hold-1", Name: "send_payment", Arguments: json.RawMessage(`{"iban":"DE89"}`)}, agentloop.ToolDef{Op: agentloop.OpChange}, "acct", true),
				})
				commit.Hold = &hold
			}
			switch test.name {
			case "failed":
				if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Fail}); err != nil {
					t.Fatal(err)
				}
			case "verifying", "done":
				if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Advance, Turn: domain.Sentinel}); err != nil {
					t.Fatal(err)
				}
				if test.name == "done" {
					if _, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
						t.Fatal(err)
					}
					if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Pass}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if test.runnerOwned {
				markTaskRunnerOwned(t, owner, task.ID)
			}
			called := 0
			turnStore := postgres.TaskTurnStore{
				Store:       store,
				Scope:       agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
				EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error { called++; return nil },
			}
			result, err := turnStore.CommitRunnerTurn(ctx, commit)
			if err != nil || result.Continue || called != 0 {
				t.Fatalf("CommitRunnerTurn = %+v calls=%d err=%v, want stop without enqueue", result, called, err)
			}
		})
	}
}

func TestCommitRunnerTurnRejectsLiveRevocationWithoutWrites(t *testing.T) {
	for _, revoke := range []struct {
		name  string
		query string
	}{
		{"demoted", `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'viewer') WHERE id = $1::uuid`},
		{"disabled", `UPDATE identity.users SET disabled_at = now() WHERE id = $1::uuid`},
		{"unjoined", `UPDATE identity.users SET joined_at = NULL WHERE id = $1::uuid`},
	} {
		t.Run(revoke.name, func(t *testing.T) {
			owner := up(t)
			store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
			ctx := context.Background()
			task := reach(t, store, "Revoked commit", domain.Working)
			markTaskRunnerOwned(t, owner, task.ID)
			run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(ctx, revoke.query, ownerUser); err != nil {
				t.Fatal(err)
			}
			called := 0
			turnStore := postgres.TaskTurnStore{
				Store:       store,
				Scope:       agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
				EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error { called++; return nil },
			}
			_, err = turnStore.CommitRunnerTurn(ctx, agentloop.TurnCommit{
				RunID: run.ID, ObjectID: task.ID, UserID: ownerUser, SeqStart: 1,
				Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "denied"}}, CostMicroUSD: 500, Exposure: agentloop.ExposureState{Tainted: true},
			})
			if !errors.Is(err, app.ErrAccess) || called != 0 {
				t.Fatalf("revoked CommitRunnerTurn err=%v calls=%d, want ErrAccess/no enqueue", err, called)
			}
			var turns, approvals int
			var spent int64
			var tainted bool
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.turns WHERE run_id = $1::uuid`, run.ID).Scan(&turns); err != nil {
				t.Fatal(err)
			}
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.approvals WHERE task_id = $1::uuid`, task.ID).Scan(&approvals); err != nil {
				t.Fatal(err)
			}
			if err := owner.QueryRow(ctx, `SELECT spent, tainted FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent, &tainted); err != nil {
				t.Fatal(err)
			}
			if turns != 0 || approvals != 0 || spent != 0 || tainted {
				t.Fatalf("revoked commit wrote turns=%d approvals=%d spent=%d tainted=%t", turns, approvals, spent, tainted)
			}
		})
	}
	owner := up(t)
	store := postgres.Store{Runner: pgtest.Runner(t, owner)}
	missingID := "018f3b2c-7a10-7c4a-8e11-666666666666"
	called := false
	turnStore := postgres.TaskTurnStore{
		Store:       store,
		Scope:       agentloop.AccessScope{UserID: missingID, Allow: ownerAccess.Allow},
		EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error { called = true; return nil },
	}
	if _, err := turnStore.CommitRunnerTurn(context.Background(), agentloop.TurnCommit{UserID: missingID}); !errors.Is(err, app.ErrAccess) || called {
		t.Fatalf("missing-user CommitRunnerTurn = %v called=%t, want ErrAccess/no enqueue", err, called)
	}
}

func TestCommitRunnerTurnFailsClosedWithoutRunnerOrOnLookupError(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task, run := runnerCommitTask(t, store, owner, domain.Working, true)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	noRunner := store
	noRunner.Runner = nil
	for _, test := range []struct {
		name  string
		store postgres.Store
		ctx   context.Context
	}{
		{"nil runner", noRunner, ctx},
		{"canceled lookup", store, canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			turnStore := postgres.TaskTurnStore{
				Store:       test.store,
				Scope:       agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
				EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error { calls++; return nil },
			}
			_, err := turnStore.CommitRunnerTurn(test.ctx, agentloop.TurnCommit{
				RunID: run.ID, ObjectID: task.ID, UserID: ownerUser, SeqStart: 1,
				Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "lookup failure"}}, CostMicroUSD: 500,
			})
			if err == nil || calls != 0 {
				t.Fatalf("CommitRunnerTurn err=%v calls=%d, want lookup error/no enqueue", err, calls)
			}
			var turns int
			var spent int64
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.turns WHERE run_id = $1::uuid`, run.ID).Scan(&turns); err != nil {
				t.Fatal(err)
			}
			if err := owner.QueryRow(ctx, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent); err != nil {
				t.Fatal(err)
			}
			if turns != 0 || spent != 0 {
				t.Fatalf("lookup failure wrote turns=%d spent=%d", turns, spent)
			}
		})
	}
}

func TestCommitRunnerTurnOwnerAccessMatrix(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task, run := runnerCommitTask(t, store, owner, domain.Working, true)
	for _, person := range []struct {
		name   string
		user   string
		allow  string
		shared bool
		want   error
	}{
		{"shared editor", otherUser, otherAccess.Allow, true, app.ErrAccess},
		{"shared admin", adminUser, adminAccess.Allow, true, app.ErrAccess},
		{"stranger", otherUser, otherAccess.Allow, false, app.ErrGone},
	} {
		t.Run(person.name, func(t *testing.T) {
			scope := agentloop.AccessScope{UserID: person.user, Allow: person.allow, ObjectID: task.ID}
			if person.shared {
				scope.SharedView, scope.SharedEdit = "{"+task.ID+"}", "{"+task.ID+"}"
			}
			turnStore := postgres.TaskTurnStore{
				Store: store, Scope: scope,
				EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error { t.Fatal("non-owner enqueued"); return nil },
			}
			_, err := turnStore.CommitRunnerTurn(ctx, agentloop.TurnCommit{
				RunID: run.ID, ObjectID: task.ID, UserID: person.user, SeqStart: 1,
				Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "denied"}}, CostMicroUSD: 1,
			})
			if !errors.Is(err, person.want) {
				t.Fatalf("CommitRunnerTurn = %v, want %v", err, person.want)
			}
		})
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.turns WHERE run_id = $1::uuid`, run.ID); got != 0 {
		t.Fatalf("non-owner commit inserted %d turns", got)
	}
}

func TestCommitRunnerTurnRevocationAllowsClaimCleanup(t *testing.T) {
	for _, revoke := range []struct {
		name  string
		query string
	}{
		{"demoted", `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'viewer') WHERE id = $1::uuid`},
		{"disabled", `UPDATE identity.users SET disabled_at = now() WHERE id = $1::uuid`},
	} {
		t.Run(revoke.name, func(t *testing.T) {
			owner := up(t)
			store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
			ctx := context.Background()
			task := reach(t, store, "Revocation cleanup", domain.Working)
			markTaskRunnerOwned(t, owner, task.ID)
			run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			claim, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Reserve(ctx, ownerAccess, task.ID, claim.Token, 1_000); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Exec(ctx, revoke.query, ownerUser); err != nil {
				t.Fatal(err)
			}
			turnStore := postgres.TaskTurnStore{
				Store: store,
				Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
				EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error {
					t.Fatal("revoked turn enqueued")
					return nil
				},
			}
			if _, err := turnStore.CommitRunnerTurn(ctx, agentloop.TurnCommit{RunID: run.ID, ObjectID: task.ID, UserID: ownerUser}); !errors.Is(err, app.ErrAccess) {
				t.Fatalf("revoked commit = %v, want ErrAccess", err)
			}
			var turns int
			var spent, reserved int64
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.turns WHERE run_id = $1::uuid`, run.ID).Scan(&turns); err != nil {
				t.Fatal(err)
			}
			if err := owner.QueryRow(ctx, `SELECT spent, reserved FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent, &reserved); err != nil {
				t.Fatal(err)
			}
			if turns != 0 || spent != 0 || reserved != 1_000 {
				t.Fatalf("revoked commit wrote turns=%d spent=%d reserved=%d", turns, spent, reserved)
			}
			if err := store.SettleOrphan(ctx, ownerAccess, task.ID, claim.Token, 500); err != nil {
				t.Fatalf("revoked-owner SettleOrphan = %v", err)
			}
			if err := store.SettleOrphan(ctx, ownerAccess, task.ID, claim.Token, 500); !errors.Is(err, app.ErrAlready) {
				t.Fatalf("duplicate SettleOrphan = %v, want ErrAlready", err)
			}
			if err := store.Release(ctx, ownerAccess, task.ID, claim.Token); err != nil {
				t.Fatalf("revoked-owner Release = %v", err)
			}
			var settlementCount int
			var token *string
			if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.settlements WHERE task_id = $1::uuid AND claim_token = $2::uuid`, task.ID, claim.Token).Scan(&settlementCount); err != nil {
				t.Fatal(err)
			}
			if err := owner.QueryRow(ctx, `SELECT spent, reserved, claim_token::text FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent, &reserved, &token); err != nil {
				t.Fatal(err)
			}
			if settlementCount != 1 || spent != 500 || reserved != 0 || token != nil {
				t.Fatalf("cleanup settlements=%d spent=%d reserved=%d token=%v", settlementCount, spent, reserved, token)
			}
		})
	}
}

func TestCommitRunnerTurnHookFailureRollsBack(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "Runner rollback", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("enqueue failed")
	turnStore := postgres.TaskTurnStore{
		Store:       store,
		Scope:       agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: task.ID},
		EnqueueNext: func(context.Context, pgx.Tx, agentloop.TurnCommit) error { return wantErr },
	}
	_, err = turnStore.CommitRunnerTurn(ctx, agentloop.TurnCommit{
		RunID: run.ID, ObjectID: task.ID, UserID: ownerUser, SeqStart: 1,
		Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "rollback"}}, CostMicroUSD: 500,
		Exposure: agentloop.ExposureState{Tainted: true},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("hook error = %v, want %v", err, wantErr)
	}
	var turns int
	var spent int64
	var tainted bool
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.turns WHERE run_id = $1::uuid`, run.ID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT spent, tainted FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent, &tainted); err != nil {
		t.Fatal(err)
	}
	if turns != 0 || spent != 0 || tainted {
		t.Fatalf("hook failure persisted turns=%d spent=%d tainted=%t", turns, spent, tainted)
	}
}

func runnerCommitTask(t *testing.T, store postgres.Store, owner *pgxpool.Pool, status domain.Status, runnerOwned bool) (app.Task, app.Run) {
	t.Helper()
	task := reach(t, store, "Runner commit", status)
	if runnerOwned {
		markTaskRunnerOwned(t, owner, task.ID)
	}
	run, err := app.Admit(context.Background(), store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return task, run
}

func taskMoney(t *testing.T, owner *pgxpool.Pool, id string) (int64, bool) {
	t.Helper()
	var spent int64
	var paused bool
	if err := owner.QueryRow(context.Background(), `SELECT spent, paused FROM tasks.tasks WHERE id = $1::uuid`, id).Scan(&spent, &paused); err != nil {
		t.Fatal(err)
	}
	return spent, paused
}
