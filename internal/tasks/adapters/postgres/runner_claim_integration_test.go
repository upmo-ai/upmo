//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func markTaskRunnerOwned(t *testing.T, owner *pgxpool.Pool, taskID string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `UPDATE tasks.tasks SET runner_owned = true WHERE id = $1::uuid`, taskID); err != nil {
		t.Fatal(err)
	}
}

func settleViaRunnerTx(ctx context.Context, store postgres.Store, taskID, runID, token string, actual int64) error {
	return store.NarrowedRunnerTx(ctx, agentloop.AccessScope{
		UserID:   ownerUser,
		Allow:    ownerAccess.Allow,
		ObjectID: taskID,
	}, func(tx pgx.Tx) error {
		return store.SettleTx(ctx, tx, taskID, runID, token, actual)
	})
}

func TestRunnerTimingOrder(t *testing.T) {
	if app.WorkerTimeout != 10*time.Minute {
		t.Fatalf("WorkerTimeout = %v, want 10m", app.WorkerTimeout)
	}
	if app.ClaimLease != 11*time.Minute {
		t.Fatalf("ClaimLease = %v, want 11m", app.ClaimLease)
	}
	if jobs.RescueStuckAfter != 15*time.Minute {
		t.Fatalf("RescueStuckAfter = %v, want 15m", jobs.RescueStuckAfter)
	}
	if !(app.WorkerTimeout < app.ClaimLease && app.ClaimLease < jobs.RescueStuckAfter) {
		t.Fatalf("timing order broken: WorkerTimeout=%v ClaimLease=%v RescueStuckAfter=%v",
			app.WorkerTimeout, app.ClaimLease, jobs.RescueStuckAfter)
	}
}

func TestRunnerClaim(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Claimable runner task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET tainted = true, private_read = true WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	var dbNowBefore time.Time
	if err := owner.QueryRow(ctx, `SELECT now()`).Scan(&dbNowBefore); err != nil {
		t.Fatal(err)
	}

	claim1, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatalf("first Claim = %v", err)
	}
	if _, err := uuid.Parse(claim1.Token); err != nil {
		t.Fatalf("claim1 token %q invalid UUID: %v", claim1.Token, err)
	}
	if claim1.Status != domain.Working || !claim1.Tainted || !claim1.PrivateRead || claim1.TurnSeq != 0 {
		t.Fatalf("claim1 = %+v, want Working/tainted=true/private=true/turnSeq=0", claim1)
	}

	var dbToken string
	var leaseUntil, dbNowAfter time.Time
	if err := owner.QueryRow(ctx, `
		SELECT claim_token::text, lease_until, now()
		FROM tasks.tasks
		WHERE id = $1::uuid`, task.ID).Scan(&dbToken, &leaseUntil, &dbNowAfter); err != nil {
		t.Fatal(err)
	}
	if dbToken != claim1.Token {
		t.Fatalf("db claim_token = %q, want %q", dbToken, claim1.Token)
	}
	if leaseUntil.Before(dbNowBefore.Add(app.ClaimLease-5*time.Second)) || leaseUntil.After(dbNowAfter.Add(app.ClaimLease+5*time.Second)) {
		t.Fatalf("lease_until = %v, want about %v + %v", leaseUntil, dbNowBefore, app.ClaimLease)
	}

	// Second Claim while lease is live returns ErrBusy without mutating token.
	if _, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("second Claim while live = %v, want ErrBusy", err)
	}
	if got := taskText(t, owner, `SELECT claim_token::text FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != claim1.Token {
		t.Fatalf("claim_token after ErrBusy = %q, want %q", got, claim1.Token)
	}

	// Expire lease and plant reserved=500; takeover returns a fresh token and clears reserved=0.
	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET lease_until = now() - interval '1 second', reserved = 500
		WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	claim2, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatalf("takeover Claim = %v", err)
	}
	if claim2.Token == "" || claim2.Token == claim1.Token {
		t.Fatalf("takeover token = %q, want new token != %q", claim2.Token, claim1.Token)
	}
	if got := taskInt(t, owner, `SELECT reserved FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 0 {
		t.Fatalf("reserved after takeover = %d, want 0", got)
	}

	// Settle turn 1 -> turn_seq = 1, claim_token cleared.
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2.Token, 100); err != nil {
		t.Fatalf("settle turn 1 = %v", err)
	}

	// Replay: Claim(turn 1) -> ErrAlready with no lease taken; Claim(turn 2) succeeds; Claim(turn 3) -> ErrNotRunnable.
	if _, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1); !errors.Is(err, app.ErrAlready) {
		t.Fatalf("replay Claim(turn 1) = %v, want ErrAlready", err)
	}
	var nullCheck *string
	if err := owner.QueryRow(ctx, `SELECT claim_token::text FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&nullCheck); err != nil {
		t.Fatal(err)
	}
	if nullCheck != nil {
		t.Fatalf("replay took claim_token = %q, want NULL", *nullCheck)
	}
	if _, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 3); !errors.Is(err, app.ErrNotRunnable) {
		t.Fatalf("future Claim(turn 3) = %v, want ErrNotRunnable", err)
	}
	claimTurn2, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 2)
	if err != nil || claimTurn2.TurnSeq != 1 {
		t.Fatalf("Claim(turn 2) = %+v, %v", claimTurn2, err)
	}
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claimTurn2.Token, 50); err != nil {
		t.Fatalf("settle turn 2 = %v", err)
	}

	// Replay check MUST precede runnable checks: move task to Verifying with turn_seq=2;
	// Claim(turn 2) -> ErrAlready, Claim(turn 3) -> ErrNotRunnable.
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor)
		SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, 'Working', 'Verifying', 'advance', 'core'
		FROM tasks.task_events WHERE task_id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET status = 'Verifying' WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 2); !errors.Is(err, app.ErrAlready) {
		t.Fatalf("replay Claim(turn 2) on Verifying task = %v, want ErrAlready", err)
	}
	if _, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 3); !errors.Is(err, app.ErrNotRunnable) {
		t.Fatalf("Claim(turn 3) on Verifying task = %v, want ErrNotRunnable", err)
	}

	// Refusal cases: each returns the typed error and leaves claim_token NULL.
	assertNoClaim := func(name, taskID string, err error) {
		t.Helper()
		var tok *string
		if qErr := owner.QueryRow(ctx, `SELECT claim_token::text FROM tasks.tasks WHERE id = $1::uuid`, taskID).Scan(&tok); qErr != nil {
			t.Fatal(qErr)
		}
		if tok != nil {
			t.Fatalf("%s wrote claim_token = %q after error %v", name, *tok, err)
		}
	}

	// Non-runner_owned task.
	manualTask := reach(t, store, "Manual task", domain.Working)
	manualRun, err := app.Admit(ctx, store, ownerAccess, manualTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRun(ctx, ownerAccess, manualTask.ID, manualRun.ID, 1); !errors.Is(err, app.ErrNotRunnable) {
		t.Fatalf("Claim non-runner_owned = %v, want ErrNotRunnable", err)
	}
	assertNoClaim("non-runner_owned", manualTask.ID, err)

	// Paused task.
	pausedTask := reach(t, store, "Paused task", domain.Working)
	markTaskRunnerOwned(t, owner, pausedTask.ID)
	pausedRun, err := app.Admit(ctx, store, ownerAccess, pausedTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET paused = true WHERE id = $1::uuid`, pausedTask.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRun(ctx, ownerAccess, pausedTask.ID, pausedRun.ID, 1); !errors.Is(err, app.ErrNotRunnable) {
		t.Fatalf("Claim paused = %v, want ErrNotRunnable", err)
	}
	assertNoClaim("paused", pausedTask.ID, err)

	// Needs approval task with pending approval.
	approvalTask := reach(t, store, "Approval task", domain.Working)
	pending := request(t, store, approvalTask.ID)
	if pending.State != domain.ApprovalPending {
		t.Fatalf("pending approval state = %s", pending.State)
	}
	markTaskRunnerOwned(t, owner, approvalTask.ID)
	approvalRun, err := app.Admit(ctx, store, ownerAccess, approvalTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRun(ctx, ownerAccess, approvalTask.ID, approvalRun.ID, 1); !errors.Is(err, app.ErrNotRunnable) {
		t.Fatalf("Claim pending approval = %v, want ErrNotRunnable", err)
	}
	assertNoClaim("pending approval", approvalTask.ID, err)

	// Unadmitted run: end all open admitted runs, occupy both admission slots, then Admit yields queued.
	unadmittedTask := reach(t, store, "Unadmitted run task", domain.Planning)
	markTaskRunnerOwned(t, owner, unadmittedTask.ID)
	slotA := reach(t, store, "Slot A", domain.Working)
	slotB := reach(t, store, "Slot B", domain.Working)

	rows, err := owner.Query(ctx, `SELECT id::text FROM tasks.runs WHERE state = 'admitted'`)
	if err != nil {
		t.Fatal(err)
	}
	var openRunIDs []string
	for rows.Next() {
		var rid string
		if err := rows.Scan(&rid); err != nil {
			t.Fatal(err)
		}
		openRunIDs = append(openRunIDs, rid)
	}
	rows.Close()
	for _, rid := range openRunIDs {
		if _, err := app.EndRun(ctx, store, ownerAccess, rid); err != nil {
			t.Fatal(err)
		}
	}
	if r1, err := app.Admit(ctx, store, ownerAccess, slotA.ID, ""); err != nil || r1.State != domain.RunAdmitted {
		t.Fatalf("slotA admit = %+v %v", r1, err)
	}
	if r2, err := app.Admit(ctx, store, ownerAccess, slotB.ID, ""); err != nil || r2.State != domain.RunAdmitted {
		t.Fatalf("slotB admit = %+v %v", r2, err)
	}
	queuedRun, err := app.Admit(ctx, store, ownerAccess, unadmittedTask.ID, "")
	if err != nil || queuedRun.State != domain.RunQueued {
		t.Fatalf("queuedRun = %+v %v, want queued", queuedRun, err)
	}
	if _, err := store.ClaimRun(ctx, ownerAccess, unadmittedTask.ID, queuedRun.ID, 1); !errors.Is(err, app.ErrNotRunnable) {
		t.Fatalf("Claim queued run = %v, want ErrNotRunnable", err)
	}
	assertNoClaim("queued run", unadmittedTask.ID, err)

	// Non-owner (shared editor => ErrAccess, stranger => ErrGone) and owner without task:edit (=> ErrAccess).
	if _, err := app.EndRun(ctx, store, ownerAccess, queuedRun.ID); err != nil {
		t.Fatal(err)
	}
	// Make room for unadmittedTask run to be admitted cleanly.
	rows, err = owner.Query(ctx, `SELECT id::text FROM tasks.runs WHERE state = 'admitted'`)
	if err != nil {
		t.Fatal(err)
	}
	openRunIDs = nil
	for rows.Next() {
		var rid string
		if err := rows.Scan(&rid); err != nil {
			t.Fatal(err)
		}
		openRunIDs = append(openRunIDs, rid)
	}
	rows.Close()
	for _, rid := range openRunIDs {
		if _, err := app.EndRun(ctx, store, ownerAccess, rid); err != nil {
			t.Fatal(err)
		}
	}
	admittedPlanRun, err := app.Admit(ctx, store, ownerAccess, unadmittedTask.ID, "")
	if err != nil || admittedPlanRun.State != domain.RunAdmitted {
		t.Fatalf("admittedPlanRun = %+v %v", admittedPlanRun, err)
	}

	sharedEditor := app.Access{
		UserID:     otherUser,
		Allow:      "task:view,task:edit",
		SharedView: "{" + unadmittedTask.ID + "}",
		SharedEdit: "{" + unadmittedTask.ID + "}",
	}
	if _, err := store.ClaimRun(ctx, sharedEditor, unadmittedTask.ID, admittedPlanRun.ID, 1); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("Claim shared editor = %v, want ErrAccess", err)
	}
	assertNoClaim("shared editor", unadmittedTask.ID, err)

	stranger := app.Access{UserID: otherUser, Allow: "task:view,task:edit"}
	if _, err := store.ClaimRun(ctx, stranger, unadmittedTask.ID, admittedPlanRun.ID, 1); !errors.Is(err, app.ErrGone) {
		t.Fatalf("Claim stranger = %v, want ErrGone", err)
	}
	assertNoClaim("stranger", unadmittedTask.ID, err)

	ownerViewOnly := app.Access{UserID: ownerUser, Allow: "task:view"}
	if _, err := store.ClaimRun(ctx, ownerViewOnly, unadmittedTask.ID, admittedPlanRun.ID, 1); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("Claim owner without task:edit = %v, want ErrAccess", err)
	}
	assertNoClaim("owner without task:edit", unadmittedTask.ID, err)

	if _, err := store.ClaimRun(ctx, ownerAccess, uuid.NewString(), admittedPlanRun.ID, 1); !errors.Is(err, app.ErrGone) {
		t.Fatalf("Claim missing task = %v, want ErrGone", err)
	}
}

func TestRunnerSettleTx(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Settle task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	claim1, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET reserved = 400000 WHERE id = $1::uuid`, task.ID); err != nil {
			t.Fatal(err)
		}
	})

	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim1.Token, 250_000); err != nil {
		t.Fatalf("SettleTx(250000) = %v", err)
	}

	var spent, reserved int64
	var paused bool
	var dbToken, leaseUntil *string
	if err := owner.QueryRow(ctx, `
		SELECT spent, reserved, paused, claim_token::text, lease_until::text
		FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&spent, &reserved, &paused, &dbToken, &leaseUntil); err != nil {
		t.Fatal(err)
	}
	if spent != 250_000 || reserved != 0 || paused || dbToken != nil || leaseUntil != nil {
		t.Fatalf("after settle: spent=%d reserved=%d paused=%t token=%v lease=%v", spent, reserved, paused, dbToken, leaseUntil)
	}
	if got := taskInt(t, owner, `SELECT turn_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 1 {
		t.Fatalf("turn_seq = %d, want 1", got)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.settlements WHERE task_id = $1::uuid`, task.ID); got != 1 {
		t.Fatalf("settlements count = %d, want 1", got)
	}

	// Settling again with the cleared token returns ErrFenced.
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim1.Token, 100); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("repeat SettleTx = %v, want ErrFenced", err)
	}

	// Settlement row replay (claim_token matches, settlements row already exists) returns ErrAlready with no extra charge.
	claim2, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.settlements (task_id, claim_token, amount)
		VALUES ($1::uuid, $2::uuid, 123)`, task.ID, claim2.Token); err != nil {
		t.Fatal(err)
	}
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2.Token, 50_000); !errors.Is(err, app.ErrAlready) {
		t.Fatalf("settlement row replay = %v, want ErrAlready", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 250_000 {
		t.Fatalf("spent changed on replay = %d, want 250000", got)
	}
	if got := taskInt(t, owner, `SELECT turn_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 1 {
		t.Fatalf("turn_seq changed on replay = %d, want 1", got)
	}

	// Take over expired claim2 for turn 2, then pause the task and verify SettleTx still adds actual and clears claim/reserved.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET lease_until = now() - interval '1 second', reserved = 100 WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	claim2Live, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET paused = true WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2Live.Token, 100_000); err != nil {
		t.Fatalf("SettleTx on paused task = %v", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 350_000 {
		t.Fatalf("spent after paused settle = %d, want 350000", got)
	}
	if got := taskInt(t, owner, `SELECT turn_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 2 {
		t.Fatalf("turn_seq after paused settle = %d, want 2", got)
	}

	// Overflow saturation: actual = MaxInt64 - spent + 10 saturates at MaxInt64 without error.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET paused = false, spent = $2 WHERE id = $1::uuid`, task.ID, int64(math.MaxInt64-50)); err != nil {
		t.Fatal(err)
	}
	claim3, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim3.Token, 60); err != nil {
		t.Fatalf("SettleTx overflow saturate = %v", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != math.MaxInt64 {
		t.Fatalf("spent after overflow = %d, want MaxInt64", got)
	}
	if got := taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID); !got {
		t.Fatalf("paused after overflow = false, want true")
	}
}

func TestRunnerClaimGrants(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Claim grant boundary task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	otherCreate := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	otherTask, err := app.ApplyTransition(ctx, store, otherCreate, "", domain.Input{Kind: domain.Open, Goal: "Other owner grant task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, otherCreate, otherTask.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	otherRun, err := app.Admit(ctx, store, otherCreate, otherTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// upmo_app is denied 42501 on claim_token, lease_until, reserved, tasks.settlements, and tasks.runs.turn_seq.
	roleProfiles := []app.Access{
		ownerAccess,
		{UserID: otherUser, Allow: "task:view,task:edit", SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}"},
		{UserID: otherUser, Allow: "task:create,task:view,task:edit,task:share,task:delete", SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}"},
		{UserID: otherUser, Allow: "members:manage,settings:manage,task:view,task:edit", SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}"},
		{UserID: otherUser, Allow: "task:create,task:view,task:edit,task:share,task:delete,members:manage,teams:manage,roles:manage,settings:manage,connectors:manage,audit:view,usage:view", SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}"},
	}
	for _, access := range roleProfiles {
		for _, stmt := range []string{
			`UPDATE tasks.tasks SET claim_token = uuidv7() WHERE id = $1::uuid`,
			`UPDATE tasks.tasks SET lease_until = now() WHERE id = $1::uuid`,
			`UPDATE tasks.tasks SET reserved = 1 WHERE id = $1::uuid`,
		} {
			asRole(t, owner, "upmo_app", access, false, func(tx pgx.Tx) {
				if _, err := tx.Exec(ctx, stmt, task.ID); sqlState(err) != "42501" {
					t.Fatalf("upmo_app %s = %v (code %s), want 42501", stmt, err, sqlState(err))
				}
			})
		}
		asRole(t, owner, "upmo_app", access, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `UPDATE tasks.runs SET turn_seq = 1 WHERE id = $1::uuid`, run.ID); sqlState(err) != "42501" {
				t.Fatalf("upmo_app UPDATE runs.turn_seq = %v (code %s), want 42501", err, sqlState(err))
			}
		})
		asRole(t, owner, "upmo_app", access, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `SELECT count(*) FROM tasks.settlements`); sqlState(err) != "42501" {
				t.Fatalf("upmo_app SELECT settlements = %v (code %s), want 42501", err, sqlState(err))
			}
		})
		asRole(t, owner, "upmo_app", access, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, `INSERT INTO tasks.settlements (task_id, claim_token, amount) VALUES ($1::uuid, uuidv7(), 1)`, task.ID); sqlState(err) != "42501" {
				t.Fatalf("upmo_app INSERT settlements = %v (code %s), want 42501", err, sqlState(err))
			}
		})
	}

	// upmo_runner updates own task claim_token/lease_until, own run turn_seq, and inserts/selects own settlements;
	// cross-owner updates/inserts/selects are denied.
	tok := uuid.NewString()
	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET claim_token = $2::uuid, lease_until = now() WHERE id = $1::uuid`, task.ID, tok); err != nil {
			t.Fatalf("runner UPDATE own task claim = %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks.runs SET turn_seq = 1 WHERE id = $1::uuid`, run.ID); err != nil {
			t.Fatalf("runner UPDATE own run turn_seq = %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tasks.settlements (task_id, claim_token, amount) VALUES ($1::uuid, $2::uuid, 42)`, task.ID, tok); err != nil {
			t.Fatalf("runner INSERT own settlement = %v", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.settlements WHERE task_id = $1::uuid`, task.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("runner SELECT own settlement = %d, %v", n, err)
		}
	})

	asRole(t, owner, "upmo_runner", ownerAccess, false, func(tx pgx.Tx) {
		tag, err := tx.Exec(ctx, `UPDATE tasks.tasks SET claim_token = uuidv7() WHERE id = $1::uuid`, otherTask.ID)
		if err != nil {
			t.Fatalf("runner cross-owner task update err = %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("runner updated %d rows on other owner's task", tag.RowsAffected())
		}
		tag, err = tx.Exec(ctx, `UPDATE tasks.runs SET turn_seq = 1 WHERE id = $1::uuid`, otherRun.ID)
		if err != nil {
			t.Fatalf("runner cross-owner run update err = %v", err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("runner updated %d rows on other owner's run", tag.RowsAffected())
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tasks.settlements (task_id, claim_token, amount) VALUES ($1::uuid, uuidv7(), 1)`, otherTask.ID); sqlState(err) != "42501" {
			t.Fatalf("runner cross-owner INSERT settlement = %v (code %s), want 42501", err, sqlState(err))
		}
	})

	// Another runner (otherUser) cannot see owner's settlements row.
	asRole(t, owner, "upmo_runner", otherCreate, false, func(tx pgx.Tx) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.settlements WHERE task_id = $1::uuid`, task.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("other runner SELECT owner settlements = %d, %v, want 0", n, err)
		}
	})
}

func TestRunnerReserve(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	parent := openRoot(t, store, "Reserve parent")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Reserve task", 1_000_000, "task:view,task:edit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET held = 100000 WHERE id = $1::uuid`, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, child.ID, 600_000); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	task := child
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Missing claim -> ErrFenced without pause.
	if err := store.Reserve(ctx, ownerAccess, task.ID, uuid.NewString(), 100); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("Reserve without claim = %v, want ErrFenced", err)
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("Reserve without claim paused task")
	}

	claim1, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Non-owner access checks: shared editor => ErrAccess, stranger => ErrGone.
	sharedEditor := app.Access{
		UserID:     otherUser,
		Allow:      "task:view,task:edit",
		SharedView: "{" + task.ID + "}",
		SharedEdit: "{" + task.ID + "}",
	}
	if err := store.Reserve(ctx, sharedEditor, task.ID, claim1.Token, 100); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("Reserve shared editor = %v, want ErrAccess", err)
	}
	stranger := app.Access{UserID: otherUser, Allow: "task:view,task:edit"}
	if err := store.Reserve(ctx, stranger, task.ID, claim1.Token, 100); !errors.Is(err, app.ErrGone) {
		t.Fatalf("Reserve stranger = %v, want ErrGone", err)
	}

	// Wrong token -> ErrFenced without pause.
	if err := store.Reserve(ctx, ownerAccess, task.ID, uuid.NewString(), 100); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("Reserve wrong token = %v, want ErrFenced", err)
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("Reserve wrong token paused task")
	}

	// x = 0 and x < 0 -> ErrNotRunnable without pause.
	for _, badX := range []int64{0, -5} {
		if err := store.Reserve(ctx, ownerAccess, task.ID, claim1.Token, badX); !errors.Is(err, app.ErrNotRunnable) {
			t.Fatalf("Reserve(%d) = %v, want ErrNotRunnable", badX, err)
		}
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("Reserve non-positive paused task")
	}

	// Exact boundary: budget 1_000_000, spent 600_000, held 100_000 -> remaining 300_000 succeeds.
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim1.Token, 300_000); err != nil {
		t.Fatalf("Reserve exact boundary (300000) = %v", err)
	}
	if got := taskInt(t, owner, `SELECT reserved FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 300_000 {
		t.Fatalf("reserved = %d, want 300000", got)
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("Reserve exact boundary paused task")
	}

	// Second Reserve with live reservation -> ErrReserved without pause.
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim1.Token, 1); !errors.Is(err, app.ErrReserved) {
		t.Fatalf("second Reserve = %v, want ErrReserved", err)
	}
	if got := taskInt(t, owner, `SELECT reserved FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 300_000 {
		t.Fatalf("reserved after ErrReserved = %d, want 300000", got)
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("ErrReserved paused task")
	}

	// Expired-lease token -> ErrFenced without pause.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET lease_until = now() - interval '1 second', reserved = 0 WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim1.Token, 100); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("Reserve expired lease = %v, want ErrFenced", err)
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("Reserve expired lease paused task")
	}

	// Take over expired claim, then x = 300_001 -> ErrReserve and paused = true.
	claim2, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim2.Token, 300_001); !errors.Is(err, app.ErrReserve) {
		t.Fatalf("Reserve(300001) = %v, want ErrReserve", err)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("refused Reserve did not pause task")
	}
	if got := taskInt(t, owner, `SELECT reserved FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 0 {
		t.Fatalf("reserved after refused Reserve = %d, want 0", got)
	}

	// Already paused task -> ErrNotRunnable.
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim2.Token, 1); !errors.Is(err, app.ErrNotRunnable) {
		t.Fatalf("Reserve while paused = %v, want ErrNotRunnable", err)
	}

	// Overspent (spent = 2_000_000 > budget 1_000_000) -> ErrReserve with paused = true.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET paused = false, held = 0, spent = 2000000 WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim2.Token, 1); !errors.Is(err, app.ErrReserve) {
		t.Fatalf("Reserve overspent = %v, want ErrReserve", err)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("overspent Reserve did not pause task")
	}

	// spent = MaxInt64 -> ErrReserve (no 22003 numeric overflow).
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET paused = false, spent = $2 WHERE id = $1::uuid`, task.ID, int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim2.Token, 1); !errors.Is(err, app.ErrReserve) {
		t.Fatalf("Reserve at MaxInt64 = %v, want ErrReserve", err)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, task.ID) {
		t.Fatal("MaxInt64 Reserve did not pause task")
	}
}

func TestRunnerNotice(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	// Refused Reserve leaves exactly one "Budget exhausted." chat_events row.
	task := reach(t, store, "Notice reserve task", domain.Working)
	if _, err := app.Spawn(ctx, store, ownerAccess, task.ID, "Notice hold", 100_000, "task:view"); err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, task.ID, 1_600_000); err != nil {
		t.Fatal(err)
	}
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim.Token, 300_001); !errors.Is(err, app.ErrReserve) {
		t.Fatalf("Reserve(300001) = %v, want ErrReserve", err)
	}

	var count int
	var body string
	if err := owner.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(body), '')
		FROM tasks.chat_events
		WHERE task_id = $1::uuid`, task.ID).Scan(&count, &body); err != nil {
		t.Fatal(err)
	}
	if count != 1 || body != domain.NoticeBudget {
		t.Fatalf("reserve notice count=%d body=%q, want 1 / %q", count, body, domain.NoticeBudget)
	}

	// Exhausting app.Spend still writes exactly one "Budget exhausted." chat_events row.
	spendTask := reach(t, store, "Notice spend task", domain.Working)
	if err := app.Spend(ctx, store, ownerAccess, spendTask.ID, 2_000_000); err != nil {
		t.Fatalf("exhausting Spend = %v", err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(body), '')
		FROM tasks.chat_events
		WHERE task_id = $1::uuid`, spendTask.ID).Scan(&count, &body); err != nil {
		t.Fatal(err)
	}
	if count != 1 || body != domain.NoticeBudget {
		t.Fatalf("spend notice count=%d body=%q, want 1 / %q", count, body, domain.NoticeBudget)
	}
}

func TestRunnerRelease(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Release task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	claimA, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claimA.Token, 200_000); err != nil {
		t.Fatal(err)
	}

	// Expire A and let B claim and reserve.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET lease_until = now() - interval '1 second' WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	claimB, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claimB.Token, 350_000); err != nil {
		t.Fatal(err)
	}

	// Non-owner cannot release.
	sharedEditor := app.Access{
		UserID:     otherUser,
		Allow:      "task:view,task:edit",
		SharedView: "{" + task.ID + "}",
		SharedEdit: "{" + task.ID + "}",
	}
	if err := store.Release(ctx, sharedEditor, task.ID, claimB.Token); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("Release shared editor = %v, want ErrAccess", err)
	}
	stranger := app.Access{UserID: otherUser, Allow: "task:view,task:edit"}
	if err := store.Release(ctx, stranger, task.ID, claimB.Token); !errors.Is(err, app.ErrGone) {
		t.Fatalf("Release stranger = %v, want ErrGone", err)
	}

	// Stale token A is a no-op: B's claim, lease, and reservation stay intact.
	if err := store.Release(ctx, ownerAccess, task.ID, claimA.Token); err != nil {
		t.Fatalf("Release stale token = %v", err)
	}
	var dbToken, dbLease *string
	var reserved int64
	if err := owner.QueryRow(ctx, `
		SELECT claim_token::text, lease_until::text, reserved
		FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&dbToken, &dbLease, &reserved); err != nil {
		t.Fatal(err)
	}
	if dbToken == nil || *dbToken != claimB.Token || dbLease == nil || reserved != 350_000 {
		t.Fatalf("stale Release mutated claim: token=%v lease=%v reserved=%d, want token=%s reserved=350000", dbToken, dbLease, reserved, claimB.Token)
	}

	// Right token B clears reserved, claim_token, and lease_until.
	if err := store.Release(ctx, ownerAccess, task.ID, claimB.Token); err != nil {
		t.Fatalf("Release matching token = %v", err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT claim_token::text, lease_until::text, reserved
		FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&dbToken, &dbLease, &reserved); err != nil {
		t.Fatal(err)
	}
	if dbToken != nil || dbLease != nil || reserved != 0 {
		t.Fatalf("after Release: token=%v lease=%v reserved=%d, want NULL/NULL/0", dbToken, dbLease, reserved)
	}
}

func TestRunnerSettleOrphan(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	// Order 1: A claims & reserves -> lease expires -> B claims & reserves -> A SettleOrphan -> B SettleTx.
	task1 := reach(t, store, "Orphan order 1", domain.Working)
	markTaskRunnerOwned(t, owner, task1.ID)
	run1, err := app.Admit(ctx, store, ownerAccess, task1.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	claimA1, err := store.ClaimRun(ctx, ownerAccess, task1.ID, run1.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task1.ID, claimA1.Token, 200_000); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET lease_until = now() - interval '1 second' WHERE id = $1::uuid`, task1.ID); err != nil {
		t.Fatal(err)
	}
	claimB1, err := store.ClaimRun(ctx, ownerAccess, task1.ID, run1.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task1.ID, claimB1.Token, 250_000); err != nil {
		t.Fatal(err)
	}

	// Non-owner cannot SettleOrphan.
	sharedEditor := app.Access{
		UserID:     otherUser,
		Allow:      "task:view,task:edit",
		SharedView: "{" + task1.ID + "}",
		SharedEdit: "{" + task1.ID + "}",
	}
	if err := store.SettleOrphan(ctx, sharedEditor, task1.ID, claimA1.Token, 100_000); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("SettleOrphan shared editor = %v, want ErrAccess", err)
	}

	if err := store.SettleOrphan(ctx, ownerAccess, task1.ID, claimA1.Token, 100_000); err != nil {
		t.Fatalf("order 1 SettleOrphan(A) = %v", err)
	}
	var dbToken1 *string
	var spent1, reserved1 int64
	if err := owner.QueryRow(ctx, `
		SELECT spent, reserved, claim_token::text
		FROM tasks.tasks WHERE id = $1::uuid`, task1.ID).Scan(&spent1, &reserved1, &dbToken1); err != nil {
		t.Fatal(err)
	}
	if spent1 != 100_000 || reserved1 != 250_000 || dbToken1 == nil || *dbToken1 != claimB1.Token {
		t.Fatalf("order 1 after A orphan: spent=%d reserved=%d token=%v, want 100000/250000/%s", spent1, reserved1, dbToken1, claimB1.Token)
	}
	if got := taskInt(t, owner, `SELECT turn_seq FROM tasks.runs WHERE id = $1::uuid`, run1.ID); got != 0 {
		t.Fatalf("order 1 turn_seq after A orphan = %d, want 0", got)
	}

	if err := settleViaRunnerTx(ctx, store, task1.ID, run1.ID, claimB1.Token, 200_000); err != nil {
		t.Fatalf("order 1 SettleTx(B) = %v", err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT spent, reserved, claim_token::text
		FROM tasks.tasks WHERE id = $1::uuid`, task1.ID).Scan(&spent1, &reserved1, &dbToken1); err != nil {
		t.Fatal(err)
	}
	if spent1 != 300_000 || reserved1 != 0 || dbToken1 != nil {
		t.Fatalf("order 1 after B settle: spent=%d reserved=%d token=%v, want 300000/0/NULL", spent1, reserved1, dbToken1)
	}
	if got := taskInt(t, owner, `SELECT turn_seq FROM tasks.runs WHERE id = $1::uuid`, run1.ID); got != 1 {
		t.Fatalf("order 1 turn_seq after B settle = %d, want 1", got)
	}

	// Replaying A's SettleOrphan returns ErrAlready and changes nothing.
	if err := store.SettleOrphan(ctx, ownerAccess, task1.ID, claimA1.Token, 100_000); !errors.Is(err, app.ErrAlready) {
		t.Fatalf("replay SettleOrphan = %v, want ErrAlready", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task1.ID); got != 300_000 {
		t.Fatalf("spent after orphan replay = %d, want 300000", got)
	}

	// Order 2: A claims & reserves -> lease expires -> B claims & reserves -> B SettleTx first -> A SettleOrphan second.
	task2 := reach(t, store, "Orphan order 2", domain.Working)
	markTaskRunnerOwned(t, owner, task2.ID)
	run2, err := app.Admit(ctx, store, ownerAccess, task2.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	claimA2, err := store.ClaimRun(ctx, ownerAccess, task2.ID, run2.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task2.ID, claimA2.Token, 200_000); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET lease_until = now() - interval '1 second' WHERE id = $1::uuid`, task2.ID); err != nil {
		t.Fatal(err)
	}
	claimB2, err := store.ClaimRun(ctx, ownerAccess, task2.ID, run2.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task2.ID, claimB2.Token, 250_000); err != nil {
		t.Fatal(err)
	}

	if err := settleViaRunnerTx(ctx, store, task2.ID, run2.ID, claimB2.Token, 200_000); err != nil {
		t.Fatalf("order 2 SettleTx(B) = %v", err)
	}
	if err := store.SettleOrphan(ctx, ownerAccess, task2.ID, claimA2.Token, 100_000); err != nil {
		t.Fatalf("order 2 SettleOrphan(A) = %v", err)
	}
	var dbToken2 *string
	var spent2, reserved2 int64
	if err := owner.QueryRow(ctx, `
		SELECT spent, reserved, claim_token::text
		FROM tasks.tasks WHERE id = $1::uuid`, task2.ID).Scan(&spent2, &reserved2, &dbToken2); err != nil {
		t.Fatal(err)
	}
	if spent2 != 300_000 || reserved2 != 0 || dbToken2 != nil {
		t.Fatalf("order 2 end state: spent=%d reserved=%d token=%v, want 300000/0/NULL", spent2, reserved2, dbToken2)
	}
	if got := taskInt(t, owner, `SELECT turn_seq FROM tasks.runs WHERE id = $1::uuid`, run2.ID); got != 1 {
		t.Fatalf("order 2 turn_seq = %d, want 1", got)
	}

	// SettleOrphan works on a Failed task and saturates at MaxInt64 / pauses at budget - held.
	if _, err := app.EndRun(ctx, store, ownerAccess, run1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.EndRun(ctx, store, ownerAccess, run2.ID); err != nil {
		t.Fatal(err)
	}
	failedTask := reach(t, store, "Orphan failed task", domain.Working)
	if _, err := app.Spawn(ctx, store, ownerAccess, failedTask.ID, "Orphan hold", 200_000, "task:view"); err != nil {
		t.Fatal(err)
	}
	markTaskRunnerOwned(t, owner, failedTask.ID)
	failedRun, err := app.Admit(ctx, store, ownerAccess, failedTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	claimFail, err := store.ClaimRun(ctx, ownerAccess, failedTask.ID, failedRun.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, failedTask.ID, domain.Input{Kind: domain.Fail}); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleOrphan(ctx, ownerAccess, failedTask.ID, claimFail.Token, 1_800_000); err != nil {
		t.Fatalf("SettleOrphan on Failed task = %v", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, failedTask.ID); got != 1_800_000 {
		t.Fatalf("Failed task spent = %d, want 1800000", got)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1::uuid`, failedTask.ID) {
		t.Fatal("SettleOrphan hitting budget - held did not pause task")
	}

	// Overflow saturation on paused Failed task.
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET spent = $2 WHERE id = $1::uuid`, failedTask.ID, int64(math.MaxInt64-50)); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleOrphan(ctx, ownerAccess, failedTask.ID, uuid.NewString(), 60); err != nil {
		t.Fatalf("SettleOrphan overflow saturate = %v", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, failedTask.ID); got != math.MaxInt64 {
		t.Fatalf("spent after orphan overflow = %d, want MaxInt64", got)
	}
}

func TestRunnerTerminalClear(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Terminal fail clear", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(ctx, ownerAccess, task.ID, claim.Token, 300_000); err != nil {
		t.Fatal(err)
	}

	failed, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Fail})
	if err != nil || failed.Status != domain.Failed {
		t.Fatalf("Fail = %+v, %v", failed, err)
	}

	var dbToken, dbLease *string
	var reserved int64
	if err := owner.QueryRow(ctx, `
		SELECT claim_token::text, lease_until::text, reserved
		FROM tasks.tasks WHERE id = $1::uuid`, task.ID).Scan(&dbToken, &dbLease, &reserved); err != nil {
		t.Fatal(err)
	}
	if dbToken != nil || dbLease != nil || reserved != 0 {
		t.Fatalf("after Fail: token=%v lease=%v reserved=%d, want NULL/NULL/0", dbToken, dbLease, reserved)
	}

	// A later SettleOrphan still charges the failed task.
	if err := store.SettleOrphan(ctx, ownerAccess, task.ID, claim.Token, 120_000); err != nil {
		t.Fatalf("SettleOrphan after Fail = %v", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 120_000 {
		t.Fatalf("spent after SettleOrphan = %d, want 120000", got)
	}

	// Done transition on a runner_owned task also clears claim and reservation.
	doneTask := reach(t, store, "Terminal done clear", domain.Verifying)
	if _, err := app.RecordChecks(ctx, store, ownerAccess, doneTask.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
		t.Fatal(err)
	}
	markTaskRunnerOwned(t, owner, doneTask.ID)
	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET claim_token = uuidv7(), lease_until = now() + interval '5 minutes', reserved = 150000
		WHERE id = $1::uuid`, doneTask.ID); err != nil {
		t.Fatal(err)
	}
	asRole(t, owner, "upmo_runner", ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor)
			SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, 'Verifying', 'Done', 'pass', 'verifier'
			FROM tasks.task_events WHERE task_id = $1::uuid`, doneTask.ID); err != nil {
			t.Fatalf("runner Done event = %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks.tasks SET status = 'Done' WHERE id = $1::uuid`, doneTask.ID); err != nil {
			t.Fatalf("runner Done update = %v", err)
		}
	})
	if err := owner.QueryRow(ctx, `
		SELECT claim_token::text, lease_until::text, reserved
		FROM tasks.tasks WHERE id = $1::uuid`, doneTask.ID).Scan(&dbToken, &dbLease, &reserved); err != nil {
		t.Fatal(err)
	}
	if dbToken != nil || dbLease != nil || reserved != 0 {
		t.Fatalf("after Done: token=%v lease=%v reserved=%d, want NULL/NULL/0", dbToken, dbLease, reserved)
	}
}
