//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestOpenPermitsPreparingPlanningAndWorkingAndRefusesVerifyingOrDone(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	for _, to := range []domain.Status{domain.Preparing, domain.Planning, domain.Working} {
		task, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Open " + string(to), To: to})
		if err != nil || task.Status != to {
			t.Fatalf("open %s = %+v %v", to, task, err)
		}
		if got := taskStatus(t, owner, task.ID); got != string(to) {
			t.Fatalf("stored %s = %s", to, got)
		}
		var eventTo string
		if err := owner.QueryRow(ctx, `
			SELECT to_status FROM tasks.task_events
			WHERE task_id = $1::uuid AND seq = 1`, task.ID).Scan(&eventTo); err != nil || eventTo != string(to) {
			t.Fatalf("event to %s = %s %v", to, eventTo, err)
		}
	}
	for _, status := range []string{"Verifying", "Done"} {
		id := "018f3b2c-7a10-7c4a-8e11-33333333333" + map[string]string{"Verifying": "1", "Done": "2"}[status]
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.tasks (id, owner_user_id, goal, status)
			VALUES ($1::uuid, $2::uuid, $3, $4)`, id, ownerUser, "Bad "+status, status); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		// The edge check rejects an open event to Verifying or Done, so the
		// matching event never reaches the deferred born() check.
		_, eventErr := tx.Exec(ctx, `
			INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
			VALUES ($1::uuid, 1, '', $2, 'open')`, id, status)
		_ = tx.Rollback(ctx)
		if eventErr == nil {
			t.Fatalf("open event to %s was stored", status)
		}
		tx, err = owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.tasks (id, owner_user_id, goal, status)
			VALUES ($1::uuid, $2::uuid, $3, $4)`, id, ownerUser, "Bad "+status, status); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		err = tx.Commit(ctx)
		_ = tx.Rollback(ctx)
		if err == nil || !strings.Contains(err.Error(), "tasks: writer") {
			t.Fatalf("open %s = %v", status, err)
		}
	}
}

func TestFollowTriggerRequiresEveryPlannedCriterionToPassOrHaveAcceptedFinding(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "Check the books", domain.Verifying)
	plan, digest, err := domain.AcceptPlan(domain.FlowStandard, []domain.Unit{
		{Title: "One", Criteria: []domain.Criterion{
			{Text: "First file", Kind: domain.CheckCommand, Target: "one"},
			{Text: "Second file", Kind: domain.CheckCommand, Target: "two"},
		}},
		{Title: "Two", Criteria: []domain.Criterion{
			{Text: "Third file", Kind: domain.CheckCommand, Target: "three"},
			{Text: "Fourth file", Kind: domain.CheckCommand, Target: "four"},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePlan(ctx, ownerAccess, task.ID, plan, digest); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]int{{1, 1}, {1, 2}, {2, 1}} {
		passed, err := store.Record(ctx, ownerAccess, task.ID, []domain.Check{plannedCheck(pair[0], pair[1], 0, domain.VerdictPass, "")}, nil)
		if err != nil || passed {
			t.Fatalf("record %v passed=%t %v", pair, passed, err)
		}
	}
	if taskBool(t, owner, `SELECT passes FROM tasks.tasks WHERE id = $1`, task.ID) {
		t.Fatal("passes became true with a criterion missing")
	}
	expectEvidence(t, owner, task.ID)

	if _, err := store.Record(ctx, ownerAccess, task.ID, []domain.Check{plannedCheck(2, 2, 1, domain.VerdictFail, "")}, nil); err != nil {
		t.Fatal(err)
	}
	expectEvidence(t, owner, task.ID)

	if _, err := owner.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true)`, ownerUser); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.review_findings (task_id, seq, unit, criterion, title)
		VALUES ($1::uuid, 1, 2, 2, 'Fourth file is open')`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(ctx, ownerAccess, task.ID, []domain.Check{plannedCheck(2, 2, 0, domain.VerdictPass, "")}, nil); err != nil {
		t.Fatal(err)
	}
	if taskBool(t, owner, `SELECT passes FROM tasks.tasks WHERE id = $1`, task.ID) {
		t.Fatal("an open blocking finding set passes")
	}
	expectEvidence(t, owner, task.ID)

	if _, err := owner.Exec(ctx, `
		UPDATE tasks.review_findings
		SET state = 'accepted', settled_by = $2::uuid
		WHERE task_id = $1::uuid AND seq = 1`, task.ID, ownerUser); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET passes = true WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	done, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Pass})
	if err != nil || done.Status != domain.Done {
		t.Fatalf("pass = %+v %v", done, err)
	}
}

func TestFollowRejectsEmptyUnitsAndAMismatchedDigest(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	open := reach(t, store, "No plan", domain.Verifying)
	passed, err := store.Record(ctx, ownerAccess, open.ID, []domain.Check{{Name: "go test", Code: 0, Verdict: domain.VerdictPass}}, nil)
	if err != nil || !passed {
		t.Fatalf("null plan record = %t %v", passed, err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET passes = true WHERE id = $1::uuid`, open.ID); err != nil {
		t.Fatal(err)
	}

	empty := reach(t, store, "Empty units", domain.Verifying)
	passed, err = store.Record(ctx, ownerAccess, empty.ID, []domain.Check{{Name: "go test", Code: 0, Verdict: domain.VerdictPass}}, nil)
	if err != nil || !passed {
		t.Fatalf("empty units record = %t %v", passed, err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET plan = '{"units":[]}'::jsonb, passes = true WHERE id = $1::uuid`, empty.ID); !strings.Contains(err.Error(), "tasks: evidence") {
		t.Fatalf("empty units = %v", err)
	}

	stale := reach(t, store, "Stale digest", domain.Verifying)
	plan, digest, err := domain.AcceptPlan(domain.FlowLight, []domain.Unit{{
		Title: "One", Criteria: []domain.Criterion{{Text: "Ledger matches", Kind: domain.CheckRule}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePlan(ctx, ownerAccess, stale.ID, plan, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(ctx, ownerAccess, stale.ID, []domain.Check{
		plannedCheck(1, 1, 0, domain.VerdictPass, strings.Repeat("0", 64)),
	}, nil); err != nil {
		t.Fatal(err)
	}
	expectEvidence(t, owner, stale.ID)
	passed, err = store.Record(ctx, ownerAccess, stale.ID, []domain.Check{plannedCheck(1, 1, 0, domain.VerdictPass, "")}, nil)
	if err != nil || !passed {
		t.Fatalf("fresh digest = %t %v", passed, err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET passes = true WHERE id = $1::uuid`, stale.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAppRoleColumnLockAndMonotonicExposureAndInsertTaintSpread(t *testing.T) {
	owner := up(t)
	appPool := pgtest.App(t, owner)
	store := postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "Lock the columns", domain.Verifying)
	for _, set := range []string{
		"tainted = true", "private_read = true", "held = held", "spent = spent", "passes = passes",
		"flow = flow", "plan_approval = plan_approval", "plan = plan", "plan_digest = plan_digest",
		"stalls = stalls", "failures = failures", "replanned = replanned", "last_gap = last_gap",
	} {
		_, err := appPool.Exec(ctx, `UPDATE tasks.tasks SET `+set+` WHERE id = $1::uuid`, task.ID)
		if sqlState(err) != "42501" {
			t.Fatalf("app update %s = %v", set, err)
		}
	}
	_, err := appPool.Exec(ctx, `
		INSERT INTO tasks.checks (task_id, batch, seq, name, code)
		VALUES ($1::uuid, 1, 1, 'forged', 0)`, task.ID)
	if sqlState(err) != "42501" {
		t.Fatalf("app insert check = %v", err)
	}
	_, err = appPool.Exec(ctx, `
		INSERT INTO tasks.review_findings (task_id, seq, unit, criterion, title)
		VALUES ($1::uuid, 1, 1, 1, 'Forged')`, task.ID)
	if sqlState(err) != "42501" {
		t.Fatalf("app insert finding = %v", err)
	}
	if err := store.Spend(ctx, ownerAccess, task.ID, 1); err != nil {
		t.Fatalf("owner spend = %v", err)
	}
	var micro int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'tasks' AND table_name = 'tasks' AND column_name LIKE '%micro_usd'`).Scan(&micro); err != nil || micro != 0 {
		t.Fatalf("micro columns = %d %v", micro, err)
	}

	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks SET tainted = true, private_read = true WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	for _, set := range []string{"tainted = false", "private_read = false"} {
		_, err := owner.Exec(ctx, `UPDATE tasks.tasks SET `+set+` WHERE id = $1::uuid`, task.ID)
		if !strings.Contains(err.Error(), "tasks: taint") {
			t.Fatalf("clear %s = %v", set, err)
		}
	}

	parent := openRoot(t, store, "Parent of a tainted child")
	sibling, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Already here", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	childID := "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.tasks (id, owner_user_id, goal, status, parent_id, budget, tainted)
		VALUES ($1::uuid, $2::uuid, 'Tainted child', 'Preparing', $3::uuid, 1, true)`,
		childID, ownerUser, parent.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		VALUES ($1::uuid, 1, '', 'Preparing', 'open')`, childID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, parent.ID) ||
		!taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, sibling.ID) {
		t.Fatal("insert taint did not spread to the parent and the sibling")
	}
}

func TestSharedEditorCannotForgeAPassWaiverOrSpend(t *testing.T) {
	owner := up(t)
	appPool := pgtest.App(t, owner)
	ctx := context.Background()
	plain := postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)}
	task := reach(t, plain, "Shared books", domain.Verifying)
	editor := app.Access{
		UserID: otherUser, Allow: "task:view,task:edit",
		SharedView: "{" + task.ID + "}", SharedEdit: "{" + task.ID + "}",
	}
	stranger := app.Access{UserID: otherUser, Allow: "task:view,task:edit"}
	for _, set := range []string{"passes = true", "spent = spent + 1", "held = held + 1", "tainted = false"} {
		_, err := appPool.Exec(ctx, `UPDATE tasks.tasks SET `+set+` WHERE id = $1::uuid`, task.ID)
		if sqlState(err) != "42501" {
			t.Fatalf("app %s = %v", set, err)
		}
	}
	if _, err := appPool.Exec(ctx, `
		INSERT INTO tasks.checks (task_id, batch, seq, name, code)
		VALUES ($1::uuid, 1, 1, 'forged', 0)`, task.ID); sqlState(err) != "42501" {
		t.Fatalf("app check = %v", err)
	}
	var checkInsert, findingUpdate bool
	if err := owner.QueryRow(ctx, `
		SELECT has_table_privilege('upmo_app', 'tasks.checks', 'INSERT'),
		       has_column_privilege('upmo_app', 'tasks.review_findings', 'state', 'UPDATE')`).Scan(&checkInsert, &findingUpdate); err != nil || checkInsert || findingUpdate {
		t.Fatalf("privileges insert=%t update=%t %v", checkInsert, findingUpdate, err)
	}

	role, _ := tableOwner(t, owner, []string{"tasks.tasks", "tasks.checks", "tasks.review_findings"}, nil, true)
	store := postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)}
	spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, task.ID)
	if err := store.Spend(ctx, editor, task.ID, 1); err == nil {
		t.Fatalf("editor spend = nil, spent = %d", taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, task.ID))
	} else if !errors.Is(err, app.ErrAccess) {
		t.Fatalf("editor spend = %v", err)
	}
	if _, err := store.Record(ctx, editor, task.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("editor record = %v", err)
	}
	var checks int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.checks WHERE task_id = $1`, task.ID).Scan(&checks); err != nil || checks != 0 {
		t.Fatalf("checks = %d %v", checks, err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, task.ID); got != spent {
		t.Fatalf("spent = %d, want %d", got, spent)
	}
	if _, err := store.Record(ctx, stranger, task.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); !errors.Is(err, app.ErrGone) {
		t.Fatalf("stranger record = %v", err)
	}
	if err := store.Spend(ctx, stranger, task.ID, 1); !errors.Is(err, app.ErrGone) {
		t.Fatalf("stranger spend = %v", err)
	}

	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.review_findings (task_id, seq, unit, criterion, title)
		VALUES ($1::uuid, 1, 1, 1, 'Open gap')`, task.ID); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO tasks.checks (task_id, batch, seq, name, code) VALUES ($1::uuid, 2, 1, 'forged', 0)`,
		`INSERT INTO tasks.review_findings (task_id, seq, unit, criterion, title) VALUES ($1::uuid, 2, 1, 1, 'Forged')`,
	} {
		asRole(t, owner, role, editor, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(context.Background(), sql, task.ID); sqlState(err) != "42501" {
				t.Fatalf("owner role %s = %v", sql, err)
			}
		})
	}
	asRole(t, owner, role, editor, false, func(tx pgx.Tx) {
		tag, err := tx.Exec(context.Background(), `
			UPDATE tasks.review_findings
			SET state = 'accepted', settled_by = $2::uuid
			WHERE task_id = $1::uuid AND seq = 1`, task.ID, otherUser)
		if err != nil || tag.RowsAffected() != 0 {
			t.Fatalf("owner role update finding = %d %v", tag.RowsAffected(), err)
		}
	})
	asRole(t, owner, role, ownerAccess, true, func(tx pgx.Tx) {
		tag, err := tx.Exec(context.Background(), `
			UPDATE tasks.review_findings
			SET state = 'accepted', settled_by = $2::uuid
			WHERE task_id = $1::uuid AND seq = 1`, task.ID, ownerUser)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("owner settle = %d %v", tag.RowsAffected(), err)
		}
		tag, err = tx.Exec(context.Background(), `
			UPDATE tasks.review_findings
			SET state = 'open', settled_by = NULL
			WHERE task_id = $1::uuid AND seq = 1`, task.ID)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("owner reopen = %d %v", tag.RowsAffected(), err)
		}
	})
	asRole(t, owner, role, editor, true, func(tx pgx.Tx) {
		tag, err := tx.Exec(context.Background(), postgres.SpendUpdateSQL(), task.ID, spent+1, false)
		if err != nil || tag.RowsAffected() != 0 {
			t.Fatalf("spend sql rows = %d %v", tag.RowsAffected(), err)
		}
		tag, err = tx.Exec(context.Background(), postgres.RecordUpdateSQL(), task.ID, true, nil)
		if err != nil || tag.RowsAffected() != 0 {
			t.Fatalf("record sql rows = %d %v", tag.RowsAffected(), err)
		}
	})
	if got := taskText(t, owner, `SELECT state FROM tasks.review_findings WHERE task_id = $1 AND seq = 1`, task.ID); got != "open" {
		t.Fatalf("finding = %s", got)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, task.ID); got != spent {
		t.Fatalf("spent after sql = %d", got)
	}
	if taskBool(t, owner, `SELECT passes FROM tasks.tasks WHERE id = $1`, task.ID) {
		t.Fatal("passes changed")
	}
	if _, err := appPool.Exec(ctx, `
		UPDATE tasks.review_findings
		SET state = 'accepted', settled_by = $2::uuid
		WHERE task_id = $1::uuid AND seq = 1`, task.ID, otherUser); sqlState(err) != "42501" {
		t.Fatalf("app waiver = %v", err)
	}
	if got := taskText(t, owner, `SELECT state FROM tasks.review_findings WHERE task_id = $1 AND seq = 1`, task.ID); got != "open" {
		t.Fatalf("finding after app = %s", got)
	}

	asRole(t, owner, role, ownerAccess, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO tasks.checks (task_id, batch, seq, name, code)
			VALUES ($1::uuid, 3, 1, 'owner check', 0)`, task.ID); err != nil {
			t.Fatalf("owner insert check = %v", err)
		}
	})
}

func TestKeepResultAllowsRootAndSubtaskUpdateInWorkingAndVerifyingAndReworkResetsPasses(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "File the receipts", domain.Verifying)
	if err := app.SaveResult(ctx, store, ownerAccess, task.ID, "Filed", "two pages"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.RecordChecks(ctx, store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
		t.Fatal(err)
	}
	if !taskBool(t, owner, `SELECT passes FROM tasks.tasks WHERE id = $1`, task.ID) {
		t.Fatal("passes is false")
	}
	rework, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Rework})
	if err != nil || rework.Status != domain.Working {
		t.Fatalf("rework = %+v %v", rework, err)
	}
	if taskBool(t, owner, `SELECT passes FROM tasks.tasks WHERE id = $1`, task.ID) {
		t.Fatal("rework left passes true")
	}
	if err := app.SaveResult(ctx, store, ownerAccess, task.ID, "Again", "two pages"); err != nil {
		t.Fatal(err)
	}
}

func TestViewAndTailAfterAskPlanReworkReplanAndHold(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	cases := []struct {
		status  domain.Status
		input   domain.Input
		message string
	}{
		{domain.Planning, domain.Input{Kind: domain.AskPlan}, "Waiting for approval"},
		{domain.Verifying, domain.Input{Kind: domain.Rework}, "Moved on to Working"},
		{domain.Working, domain.Input{Kind: domain.Replan}, "Moved on to Planning"},
		{domain.Working, domain.Input{Kind: domain.Hold, Reason: domain.ReasonAsk}, "Waiting for approval"},
	}
	for _, test := range cases {
		task := reach(t, store, "Edge "+string(test.input.Kind), test.status)
		if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, test.input); err != nil {
			t.Fatalf("%s = %v", test.input.Kind, err)
		}
		view, err := app.ViewTask(ctx, store, ownerAccess, task.ID)
		if err != nil {
			t.Fatalf("view %s = %v", test.input.Kind, err)
		}
		tail, err := store.Tail(ctx, ownerAccess, task.ID, "", 20)
		if err != nil {
			t.Fatalf("tail %s = %v", test.input.Kind, err)
		}
		if view.Events[len(view.Events)-1].Message != test.message || tail.Events[len(tail.Events)-1].Message != test.message {
			t.Fatalf("%s view %q tail %q", test.input.Kind, view.Events[len(view.Events)-1].Message, tail.Events[len(tail.Events)-1].Message)
		}
	}
}

func TestResumeWhilePendingApprovalIsRefused(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "Wait here", domain.Verifying)
	if err := store.SaveBrake(ctx, ownerAccess, task.ID, domain.BrakeState{Stalls: 2, Failures: 2, LastGap: "g1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.RequestApproval(ctx, store, ownerAccess, task.ID, "review", domain.ReasonStalled, "Stop and wait"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, task.ID, domain.Input{Kind: domain.Resume}); !errors.Is(err, domain.ErrPending) {
		t.Fatalf("resume = %v", err)
	}
	if got := taskStatus(t, owner, task.ID); got != string(domain.NeedsApproval) {
		t.Fatalf("status = %s", got)
	}
	if _, pending := approvalRows(t, owner, task.ID); pending != 1 {
		t.Fatalf("pending = %d", pending)
	}
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		SELECT $1::uuid, COALESCE(MAX(event.seq), 0) + 1, 'Needs approval', 'Working', 'resume'
		FROM tasks.task_events AS event
		WHERE event.task_id = $1::uuid`, task.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE tasks.tasks SET status = 'Working' WHERE id = $1::uuid`, task.ID)
	_ = tx.Rollback(ctx)
	if !strings.Contains(err.Error(), "tasks: approval") {
		t.Fatalf("direct resume = %v", err)
	}
	approvalID := approvalID(t, owner, task.ID)
	if _, err := store.Decide(ctx, ownerAccess, approvalID, domain.Approve); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, task.ID); got != string(domain.Verifying) {
		t.Fatalf("approved status = %s", got)
	}
	var stalls, failures int
	var gap string
	if err := owner.QueryRow(ctx, `SELECT stalls, failures, last_gap FROM tasks.tasks WHERE id = $1`, task.ID).Scan(&stalls, &failures, &gap); err != nil {
		t.Fatal(err)
	}
	if stalls != 0 || failures != 0 || gap != "" {
		t.Fatalf("brakes = %d %d %q", stalls, failures, gap)
	}
}

func TestApprovalReasonRoutingSharedViewerSelectAndEditorDecide(t *testing.T) {
	owner := up(t)
	appPool := pgtest.App(t, owner)
	store := postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	planTask := reach(t, store, "Plan the books", domain.Planning)
	approval, err := app.RequestApproval(ctx, store, ownerAccess, planTask.ID, "plan", domain.ReasonPlan, "Accept the plan")
	if err != nil {
		t.Fatal(err)
	}
	viewer := app.Access{UserID: otherUser, Allow: "task:view", SharedView: "{" + planTask.ID + "}"}
	editorBlind := app.Access{UserID: otherUser, Allow: "task:view,task:edit", SharedEdit: "{" + planTask.ID + "}"}
	editor := app.Access{
		UserID: otherUser, Allow: "task:view,task:edit",
		SharedView: "{" + planTask.ID + "}", SharedEdit: "{" + planTask.ID + "}",
	}
	var seen int
	if err := asAccess(t, appPool, viewer, `
		SELECT count(*) FROM tasks.approvals WHERE id = $1::uuid`, approval.ID).Scan(&seen); err != nil || seen != 1 {
		t.Fatalf("viewer select = %d %v", seen, err)
	}
	if _, err := store.DecideWithInput(ctx, viewer, approval.ID, domain.Deny, "Split step 1"); !errors.Is(err, app.ErrNoApproval) {
		t.Fatalf("viewer decide = %v", err)
	}
	if _, err := store.DecideWithInput(ctx, editorBlind, approval.ID, domain.Deny, "Split step 1"); !errors.Is(err, app.ErrNoApproval) {
		t.Fatalf("editor without view = %v", err)
	}
	if _, err := store.DecideWithInput(ctx, editor, approval.ID, domain.Deny, "Split step 1"); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, planTask.ID); got != string(domain.Planning) {
		t.Fatalf("plan status = %s", got)
	}
	var notes int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM tasks.chat_events WHERE task_id = $1 AND body = 'Split step 1'`, planTask.ID).Scan(&notes); err != nil || notes != 1 {
		t.Fatalf("note = %d %v", notes, err)
	}

	question := reach(t, store, "Ask the books", domain.Working)
	asked, err := app.RequestApproval(ctx, store, ownerAccess, question.ID, "ask", domain.ReasonQuestion, "Which ledger")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideWithInput(ctx, ownerAccess, asked.ID, domain.Approve, "Use Q4 ledger"); err != nil {
		t.Fatal(err)
	}
	var answer string
	if err := owner.QueryRow(ctx, `SELECT answer FROM tasks.approvals WHERE id = $1`, asked.ID).Scan(&answer); err != nil || answer != "Use Q4 ledger" {
		t.Fatalf("answer = %q %v", answer, err)
	}
	if got := taskStatus(t, owner, question.ID); got != string(domain.Working) {
		t.Fatalf("question status = %s", got)
	}
	refused := reach(t, store, "Refuse the question", domain.Working)
	denied, err := app.RequestApproval(ctx, store, ownerAccess, refused.ID, "ask", domain.ReasonQuestion, "Which ledger")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DecideWithInput(ctx, ownerAccess, denied.ID, domain.Deny, ""); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, refused.ID); got != string(domain.Failed) {
		t.Fatalf("denied question = %s", got)
	}
	if got := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, refused.ID); got != domain.CauseCanceled {
		t.Fatalf("cause = %s", got)
	}

	stalled := reach(t, store, "Stall the books", domain.Verifying)
	if err := store.SaveBrake(ctx, ownerAccess, stalled.ID, domain.BrakeState{Stalls: 2, Failures: 2, LastGap: "g1"}); err != nil {
		t.Fatal(err)
	}
	held, err := app.RequestApproval(ctx, store, ownerAccess, stalled.ID, "review", domain.ReasonStalled, "Stop and wait")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, ownerAccess, held.ID, domain.Approve); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, stalled.ID); got != string(domain.Verifying) {
		t.Fatalf("stalled approve = %s", got)
	}
	var stalls, failures int
	var gap string
	if err := owner.QueryRow(ctx, `SELECT stalls, failures, last_gap FROM tasks.tasks WHERE id = $1`, stalled.ID).Scan(&stalls, &failures, &gap); err != nil || stalls != 0 || failures != 0 || gap != "" {
		t.Fatalf("reset = %d %d %q %v", stalls, failures, gap, err)
	}
	other := reach(t, store, "Fail the stall", domain.Verifying)
	if err := store.SaveBrake(ctx, ownerAccess, other.ID, domain.BrakeState{Stalls: 2, Failures: 2, LastGap: "g1"}); err != nil {
		t.Fatal(err)
	}
	held, err = app.RequestApproval(ctx, store, ownerAccess, other.ID, "review", domain.ReasonStalled, "Stop and wait")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, ownerAccess, held.ID, domain.Deny); err != nil {
		t.Fatal(err)
	}
	if got := taskStatus(t, owner, other.ID); got != string(domain.Failed) {
		t.Fatalf("stalled deny = %s", got)
	}
	if got := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, other.ID); got != domain.ReasonStalled {
		t.Fatalf("stalled cause = %s", got)
	}
}

func TestBatchHoldFromVerifyingUsesHold(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := reach(t, store, "Batch the books", domain.Verifying)
	if _, err := store.RequestBatchApproval(ctx, ownerAccess, task.ID, "mail.send", agentloop.BatchHold{
		Reason: domain.ReasonAsk, Action: "Send the batch",
	}); err != nil {
		t.Fatal(err)
	}
	var input, from, state string
	if err := owner.QueryRow(ctx, `
		SELECT event.input, approval.from_status, approval.state
		FROM tasks.task_events AS event
		JOIN tasks.approvals AS approval ON approval.task_id = event.task_id
		WHERE event.task_id = $1::uuid
		ORDER BY event.seq DESC
		LIMIT 1`, task.ID).Scan(&input, &from, &state); err != nil {
		t.Fatal(err)
	}
	if input != "hold" || from != string(domain.Verifying) || state != "pending" {
		t.Fatalf("batch = %s from %s state %s", input, from, state)
	}
	if got := taskStatus(t, owner, task.ID); got != string(domain.NeedsApproval) {
		t.Fatalf("status = %s", got)
	}
}

func TestTasksMigrationDownDropsTasksBeforeTemplates(t *testing.T) {
	owner := up(t)
	if err := migrate.Down(context.Background(), owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerRLSOnEscrowReleaseTaintAndBudgetNotice(t *testing.T) {
	owner := up(t)
	appPool := pgtest.App(t, owner)
	ctx := context.Background()
	role, _ := tableOwner(t, owner, []string{"tasks.tasks", "tasks.chat_events"}, []string{
		"tasks.escrow_child()", "tasks.release_escrow()", "tasks.spread_taint()", "tasks.budget_notice()",
	}, true)
	store := postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)}

	parent, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Private parent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, parent.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	stranger := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	childID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', '{}', true),
		       set_config('upmo.shared_edit', '{}', true),
		       set_config('upmo.escrow_probe', '', true),
		       set_config('upmo.escrow_child_of', '', true)`, stranger.UserID, stranger.Allow); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.tasks (id, owner_user_id, goal, status, parent_id, budget)
		VALUES ($1::uuid, $2::uuid, 'Stranger child', 'Preparing', $3::uuid, 100000)`,
		childID, otherUser, parent.ID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		VALUES ($1::uuid, 1, '', 'Preparing', 'open')`, childID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	expectProbeClear(t, tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 100_000 {
		t.Fatalf("stranger held = %d", held)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, parent.ID); spent != 0 {
		t.Fatalf("stranger spent = %d", spent)
	}

	releaseParent := openRoot(t, store, "Release parent")
	releaseChild, err := app.Spawn(ctx, store, ownerAccess, releaseParent.ID, "Release child", 100_000, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	toChecking(t, store, releaseChild.ID)
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET spent = 2000000 WHERE id = $1::uuid`, releaseChild.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.checks (task_id, batch, seq, name, code)
		VALUES ($1::uuid, 1, 1, 'go test', 0)`, releaseChild.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET passes = true WHERE id = $1::uuid`, releaseChild.ID); err != nil {
		t.Fatal(err)
	}
	editor := app.Access{
		UserID: otherUser, Allow: "task:view,task:edit",
		SharedView: "{" + releaseChild.ID + "}", SharedEdit: "{" + releaseChild.ID + "}",
	}
	releaseTx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releaseTx.Rollback(ctx) }()
	if _, err := releaseTx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', $3, true),
		       set_config('upmo.shared_edit', $4, true),
		       set_config('upmo.escrow_probe', '', true),
		       set_config('upmo.escrow_child_of', '', true)`,
		editor.UserID, editor.Allow, editor.SharedView, editor.SharedEdit); err != nil {
		_ = releaseTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := releaseTx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		SELECT $1::uuid, COALESCE(MAX(event.seq), 0) + 1, 'Verifying', 'Done', 'pass'
		FROM tasks.task_events AS event
		WHERE event.task_id = $1::uuid`, releaseChild.ID); err != nil {
		_ = releaseTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := releaseTx.Exec(ctx, `UPDATE tasks.tasks SET status = 'Done' WHERE id = $1::uuid`, releaseChild.ID); err != nil {
		_ = releaseTx.Rollback(ctx)
		t.Fatal(err)
	}
	expectProbeClear(t, releaseTx)
	if err := releaseTx.Commit(ctx); err != nil {
		_ = releaseTx.Rollback(ctx)
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, releaseParent.ID); spent != 2_000_000 {
		t.Fatalf("released spent = %d", spent)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, releaseParent.ID); held != 0 {
		t.Fatalf("released held = %d", held)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, releaseParent.ID) {
		t.Fatal("parent is not paused")
	}
	var seq int
	var body string
	if err := owner.QueryRow(ctx, `
		SELECT seq, body FROM tasks.chat_events WHERE task_id = $1`, releaseParent.ID).Scan(&seq, &body); err != nil || seq != 1 || body != "Budget exhausted." {
		t.Fatalf("notice = %d %q %v", seq, body, err)
	}
	if _, err := appPool.Exec(ctx, `UPDATE tasks.tasks SET status = 'Done' WHERE id = $1::uuid`, releaseChild.ID); err != nil {
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, releaseParent.ID); spent != 2_000_000 {
		t.Fatalf("second done spent = %d", spent)
	}
	var notices int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, releaseParent.ID).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("notices = %d %v", notices, err)
	}

	p := openRoot(t, store, "Taint parent")
	c1, err := app.Spawn(ctx, store, ownerAccess, p.ID, "Child one", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	c2, err := app.Spawn(ctx, store, ownerAccess, p.ID, "Child two", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	sharer := app.Access{
		UserID: otherUser, Allow: "task:view,task:edit",
		SharedView: "{" + c1.ID + "}", SharedEdit: "{" + c1.ID + "}",
	}
	asRole(t, owner, role, sharer, true, func(tx pgx.Tx) {
		if _, err := tx.Exec(context.Background(), `
			SELECT set_config('upmo.escrow_probe', '', true),
			       set_config('upmo.escrow_child_of', '', true)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(context.Background(), `UPDATE tasks.tasks SET tainted = true WHERE id = $1::uuid`, c1.ID); err != nil {
			t.Fatal(err)
		}
		expectProbeClear(t, tx)
	})
	if !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, p.ID) ||
		!taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, c2.ID) {
		t.Fatal("taint did not spread to the parent and the other child")
	}

	budgetParent := openRoot(t, store, "Notice parent")
	budgetChild, err := app.Spawn(ctx, store, ownerAccess, budgetParent.ID, "Notice child", 100_000, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, budgetChild.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	turn := postgres.TaskTurnStore{
		Store: postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)},
		Scope: agentloop.AccessScope{
			UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: budgetChild.ID,
			SharedView: "{" + budgetParent.ID + "," + budgetChild.ID + "}",
			SharedEdit: "{" + budgetParent.ID + "," + budgetChild.ID + "}",
		},
	}
	if err := turn.CommitTurnAndEnqueue(ctx, agentloop.TurnCommit{
		ObjectID: budgetChild.ID, UserID: ownerUser, CostMicroUSD: 100_000,
	}); err != nil {
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, budgetChild.ID); spent != 100_000 {
		t.Fatalf("child spent = %d", spent)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, budgetChild.ID) {
		t.Fatal("child is not paused")
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, budgetParent.ID) {
		t.Fatal("parent paused")
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, budgetParent.ID); spent != 0 {
		t.Fatalf("parent spent = %d", spent)
	}
	seq, body = 0, ""
	if err := owner.QueryRow(ctx, `
		SELECT seq, body FROM tasks.chat_events WHERE task_id = $1`, budgetParent.ID).Scan(&seq, &body); err != nil || seq != 1 || body != "Budget exhausted." {
		t.Fatalf("child notice = %d %q %v", seq, body, err)
	}

	// The child owner cannot edit the parent. The notice insert depends on the probe.
	childTurn := postgres.TaskTurnStore{
		Store: postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)},
		Scope: agentloop.AccessScope{
			UserID: otherUser, Allow: stranger.Allow, ObjectID: childID,
		},
	}
	if err := childTurn.CommitTurnAndEnqueue(ctx, agentloop.TurnCommit{
		ObjectID: childID, UserID: otherUser, CostMicroUSD: 100_000,
	}); err != nil {
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, childID); spent != 100_000 {
		t.Fatalf("child-owner spent = %d", spent)
	}
	seq, body = 0, ""
	if err := owner.QueryRow(ctx, `
		SELECT seq, body FROM tasks.chat_events WHERE task_id = $1`, parent.ID).Scan(&seq, &body); err != nil || seq != 1 || body != "Budget exhausted." {
		t.Fatalf("child-owner notice = %d %q %v", seq, body, err)
	}
}

func TestAppRoleCannotUseTheEscrowProbe(t *testing.T) {
	owner := up(t)
	appPool := pgtest.App(t, owner)
	store := postgres.Store{Pool: appPool, Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "Probe parent")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Probe child", 100_000, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	// UPDATE only sees rows a SELECT policy already allows, so a private id stays
	// at 0 rows even after the update owner check is removed. The visible task
	// is named in the same probe, which is the row that check has to refuse.
	visible := openRoot(t, store, "Visible task")
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET visibility = 'everyone' WHERE id = $1::uuid`, visible.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.chat_events (task_id, seq, body)
		VALUES ($1::uuid, 1, 'Owner note')`, child.ID); err != nil {
		t.Fatal(err)
	}
	probeIDs := child.ID + "," + visible.ID

	tx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', 'task:view,task:edit', true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', '{}', true),
		       set_config('upmo.shared_edit', '{}', true),
		       set_config('upmo.escrow_probe', $2, true),
		       set_config('upmo.escrow_child_of', $3, true)`,
		otherUser, probeIDs, parent.ID); err != nil {
		t.Fatal(err)
	}
	var role, probe, childOf, userID string
	if err := tx.QueryRow(ctx, `
		SELECT current_user,
		       current_setting('upmo.escrow_probe', true),
		       current_setting('upmo.escrow_child_of', true),
		       current_setting('upmo.user_id', true)`).Scan(&role, &probe, &childOf, &userID); err != nil ||
		role != "upmo_app" || probe != probeIDs || childOf != parent.ID || userID != otherUser {
		t.Fatalf("role = %q probe = %q child_of = %q user = %q %v", role, probe, childOf, userID, err)
	}

	var seen int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE id = $1::uuid`, child.ID).Scan(&seen); err != nil || seen != 0 {
		t.Fatalf("probed task rows = %d %v", seen, err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE id = $1::uuid`, visible.ID).Scan(&seen); err != nil || seen != 1 {
		t.Fatalf("visible task rows = %d %v", seen, err)
	}
	tag, err := tx.Exec(ctx, `UPDATE tasks.tasks SET goal = 'Stolen goal' WHERE id = $1::uuid`, child.ID)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("probed task update = %d %v", tag.RowsAffected(), err)
	}
	tag, err = tx.Exec(ctx, `UPDATE tasks.tasks SET goal = 'Stolen goal' WHERE id = $1::uuid`, visible.ID)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("visible task update = %d %v", tag.RowsAffected(), err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1::uuid`, child.ID).Scan(&seen); err != nil || seen != 0 {
		t.Fatalf("probed chat rows = %d %v", seen, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.chat_events (task_id, seq, body)
		VALUES ($1::uuid, 2, 'Budget exhausted.')`, child.ID); sqlState(err) != "42501" {
		t.Fatalf("probed chat insert = %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var goal, body string
	if err := owner.QueryRow(ctx, `SELECT goal FROM tasks.tasks WHERE id = $1`, child.ID).Scan(&goal); err != nil || goal != "Probe child" {
		t.Fatalf("goal = %q %v", goal, err)
	}
	if err := owner.QueryRow(ctx, `SELECT goal FROM tasks.tasks WHERE id = $1`, visible.ID).Scan(&goal); err != nil || goal != "Visible task" {
		t.Fatalf("visible goal = %q %v", goal, err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT body FROM tasks.chat_events WHERE task_id = $1 AND seq = 1`, child.ID).Scan(&body); err != nil || body != "Owner note" {
		t.Fatalf("chat = %q %v", body, err)
	}
	var notices int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, child.ID).Scan(&notices); err != nil || notices != 1 {
		t.Fatalf("chat rows = %d %v", notices, err)
	}
}

func plannedCheck(unit, criterion, code int, verdict domain.Verdict, digest string) domain.Check {
	return domain.Check{
		Name: "check", Code: code, Unit: unit, Criterion: criterion,
		Kind: domain.CheckCommand, Verdict: verdict, Digest: digest,
	}
}

func expectEvidence(t *testing.T, owner *pgxpool.Pool, id string) {
	t.Helper()
	_, err := owner.Exec(context.Background(), `UPDATE tasks.tasks SET passes = true WHERE id = $1::uuid`, id)
	if !strings.Contains(err.Error(), "tasks: evidence") {
		t.Fatalf("passes = %v", err)
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func approvalID(t *testing.T, owner *pgxpool.Pool, taskID string) string {
	t.Helper()
	var id string
	if err := owner.QueryRow(context.Background(), `
		SELECT id::text FROM tasks.approvals
		WHERE task_id = $1 AND state = 'pending'`, taskID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func asAccess(t *testing.T, pool *pgxpool.Pool, access app.Access, query string, args ...any) pgx.Row {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', $3, true),
		       set_config('upmo.shared_edit', $4, true)`,
		access.UserID, access.Allow, arrayLiteral(access.SharedView), arrayLiteral(access.SharedEdit)); err != nil {
		t.Fatal(err)
	}
	return tx.QueryRow(context.Background(), query, args...)
}

func arrayLiteral(value string) string {
	if value == "" {
		return "{}"
	}
	return value
}

func asRole(t *testing.T, owner *pgxpool.Pool, role string, access app.Access, commit bool, fn func(pgx.Tx)) {
	t.Helper()
	tx, err := owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', $3, true),
		       set_config('upmo.shared_edit', $4, true)`,
		access.UserID, access.Allow, arrayLiteral(access.SharedView), arrayLiteral(access.SharedEdit)); err != nil {
		t.Fatal(err)
	}
	fn(tx)
	if !commit {
		return
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func expectProbeClear(t *testing.T, tx pgx.Tx) {
	t.Helper()
	var probe, childOf string
	if err := tx.QueryRow(context.Background(), `
		SELECT current_setting('upmo.escrow_probe', true),
		       current_setting('upmo.escrow_child_of', true)`).Scan(&probe, &childOf); err != nil || probe != "" || childOf != "" {
		t.Fatalf("probe = %q child_of = %q %v", probe, childOf, err)
	}
}

func tableOwner(t *testing.T, owner *pgxpool.Pool, tables, functions []string, login bool) (string, *pgxpool.Pool) {
	t.Helper()
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	role := "tasks_v_" + hex.EncodeToString(buf[:])
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `CREATE ROLE `+role+` NOSUPERUSER NOBYPASSRLS NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	var rolePool *pgxpool.Pool
	t.Cleanup(func() {
		if rolePool != nil {
			rolePool.Close()
		}
		for _, stmt := range []string{
			`REASSIGN OWNED BY ` + role + ` TO CURRENT_USER`,
			`DROP OWNED BY ` + role,
			`DROP ROLE IF EXISTS ` + role,
		} {
			_, _ = owner.Exec(context.Background(), stmt)
		}
	})
	for _, table := range tables {
		if _, err := owner.Exec(ctx, `ALTER TABLE `+table+` OWNER TO `+role); err != nil {
			t.Fatal(err)
		}
	}
	for _, fn := range functions {
		if _, err := owner.Exec(ctx, `ALTER FUNCTION `+fn+` OWNER TO `+role); err != nil {
			t.Fatal(err)
		}
	}
	for _, stmt := range []string{
		`GRANT USAGE ON SCHEMA tasks, identity TO ` + role,
		`GRANT EXECUTE ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) TO ` + role,
	} {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	var grant string
	if err := owner.QueryRow(ctx, `
		SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), $1::text)`, role).Scan(&grant); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if !login {
		return role, nil
	}
	password := "tasks-verify-pass"
	var alter string
	if err := owner.QueryRow(ctx, `
		SELECT format('ALTER ROLE %I LOGIN PASSWORD %L', $1::text, $2::text)`, role, password).Scan(&alter); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, alter); err != nil {
		t.Fatal(err)
	}
	raw, err := url.Parse(owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	raw.User = url.UserPassword(role, password)
	raw.Path = "/" + owner.Config().ConnConfig.Database
	rolePool, err = pgxpool.New(ctx, raw.String())
	if err != nil {
		t.Fatal(err)
	}
	return role, rolePool
}
