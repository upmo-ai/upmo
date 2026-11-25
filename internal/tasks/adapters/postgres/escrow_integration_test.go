//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestEscrowHoldsTheChildBudgetAndReleasesIt(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, ownerAccess, parent.ID, 2_000_000); err != nil {
		t.Fatal(err)
	}

	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1_200_000, "task:edit,task:delete")
	if err != nil {
		t.Fatal(err)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 1_200_000 {
		t.Fatalf("held = %d, want 1200000", held)
	}
	var childAllow string
	var budget int64
	if err := owner.QueryRow(ctx, `SELECT budget, allow FROM tasks.tasks WHERE id = $1`, child.ID).Scan(&budget, &childAllow); err != nil {
		t.Fatal(err)
	}
	if budget != 1_200_000 || childAllow != "task:edit" {
		t.Fatalf("child budget = %d allow = %q", budget, childAllow)
	}
	if err := app.Spend(ctx, store, ownerAccess, parent.ID, 500_000); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Another", 300_001, "task:view"); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("second reserve error = %v, want %v", err, domain.ErrBudget)
	}
	if err := app.Spend(ctx, store, ownerAccess, child.ID, 200_000); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, child.ID); err != nil {
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, parent.ID); spent != 700_000 {
		t.Fatalf("parent spent = %d, want 700000", spent)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 0 {
		t.Fatalf("held after cancel = %d, want 0", held)
	}
}

func TestSpendExhaustionPausesAndNotifiesOnce(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 10, "task:view")
	if err != nil {
		t.Fatal(err)
	}

	if err := app.Spend(ctx, store, ownerAccess, child.ID, 10); err != nil {
		t.Fatal(err)
	}
	if paused := taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, child.ID); !paused {
		t.Fatal("exact spend did not pause")
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, child.ID); spent != 10 {
		t.Fatalf("spent = %d, want 10", spent)
	}
	var notices int
	var body string
	if err := owner.QueryRow(ctx, `
		SELECT count(*), min(body)
		FROM tasks.chat_events
		WHERE task_id = $1`, parent.ID).Scan(&notices, &body); err != nil {
		t.Fatal(err)
	}
	if notices != 1 || body != domain.NoticeBudget {
		t.Fatalf("notices = %d body = %q", notices, body)
	}
	if status := taskStatus(t, owner, child.ID); status != string(domain.Preparing) {
		t.Fatalf("status = %s", status)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Advance}); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("advance while paused = %v", err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Plan}); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("plan while paused = %v", err)
	}

	if err := app.Spend(ctx, store, ownerAccess, child.ID, 1); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("second spend = %v", err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, parent.ID).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("notices after a second spend = %d", notices)
	}
}

func TestOverspendPreservesFullActualSpendAndPauses(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 10, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, child.ID, 11); err != nil {
		t.Fatalf("overspend = %v", err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, child.ID); spent != 11 {
		t.Fatalf("spent = %d, want 11", spent)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, child.ID) {
		t.Fatal("overspend did not pause")
	}
	var notices int
	var body string
	if err := owner.QueryRow(ctx, `SELECT count(*), min(body) FROM tasks.chat_events WHERE task_id = $1`, parent.ID).Scan(&notices, &body); err != nil {
		t.Fatal(err)
	}
	if notices != 1 || body != domain.NoticeBudget {
		t.Fatalf("notices = %d body = %q", notices, body)
	}
	if err := app.Spend(ctx, store, ownerAccess, child.ID, 1); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("second spend = %v", err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, child.ID); spent != 11 {
		t.Fatalf("spent after refusal = %d, want 11", spent)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, parent.ID).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("notices after refusal = %d", notices)
	}
}

func TestTaintSpreadsAndAuthorityStaysNarrow(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
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
	if err := app.Taint(ctx, store, ownerAccess, first.ID); err != nil {
		t.Fatal(err)
	}
	if !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, parent.ID) || !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, first.ID) || !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, second.ID) {
		t.Fatal("taint did not spread to the parent and the sibling")
	}
	third, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "Send the note", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if !taskBool(t, owner, `SELECT tainted FROM tasks.tasks WHERE id = $1`, third.ID) {
		t.Fatal("a new child of a tainted parent was clean")
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET tainted = false WHERE id = $1`, parent.ID); err == nil || !containsTask(err, "tasks: taint") {
		t.Fatalf("clear taint error = %v", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id, budget, allow, tainted)
		VALUES ($1::uuid, 'private', 'Wide', 'Preparing', $2::uuid, 1, 'task:delete', false)`,
		ownerUser, parent.ID); err == nil || !containsTask(err, "tasks: authority") && !containsTask(err, "tasks: taint") {
		t.Fatalf("wide child error = %v", err)
	}
}

func TestSpendOnZeroBudgetPausesOnceAndThenRefuses(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, allow)
		VALUES ($1::uuid, 'private', 'Unfunded', 'Preparing', $2)
		RETURNING id::text`, ownerUser, ownerAccess.Allow).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		VALUES ($1::uuid, 1, '', 'Preparing', 'open')`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, id); budget != 0 {
		t.Fatalf("budget = %d, want 0", budget)
	}
	if err := app.Spend(ctx, store, ownerAccess, id, 1); err != nil {
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, id); spent != 1 {
		t.Fatalf("spent = %d, want 1", spent)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, id) {
		t.Fatal("zero budget did not pause")
	}
	if notices := taskInt(t, owner, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, id); notices != 1 {
		t.Fatalf("notices = %d, want 1", notices)
	}
	if err := app.Spend(ctx, store, ownerAccess, id, 1); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("second spend = %v", err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, id); spent != 1 {
		t.Fatalf("spent after refusal = %d, want 1", spent)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, id) {
		t.Fatal("pause was cleared")
	}
	if notices := taskInt(t, owner, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, id); notices != 1 {
		t.Fatalf("notices after refusal = %d", notices)
	}
}

func TestFundTopUpIncreasesRootBudgetClearsPausedAndRejectsInvalidUpdates(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, ownerAccess, child.ID, 5); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("child fund = %v, want %v", err, domain.ErrBudget)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, child.ID); err != nil {
		t.Fatal(err)
	}

	if err := app.Spend(ctx, store, ownerAccess, parent.ID, 2_250_000); err != nil {
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, parent.ID); spent != 2_250_000 {
		t.Fatalf("spent = %d", spent)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, parent.ID) {
		t.Fatal("crossing spend did not pause")
	}
	if notices := taskInt(t, owner, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, parent.ID); notices != 1 {
		t.Fatalf("notices = %d, want 1", notices)
	}

	var updated time.Time
	if err := owner.QueryRow(ctx, `SELECT updated_at FROM tasks.tasks WHERE id = $1`, parent.ID).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, ownerAccess, parent.ID, 2_000_000); err != nil {
		t.Fatalf("equal fund = %v", err)
	}
	var updatedAgain time.Time
	if err := owner.QueryRow(ctx, `SELECT updated_at FROM tasks.tasks WHERE id = $1`, parent.ID).Scan(&updatedAgain); err != nil {
		t.Fatal(err)
	}
	if !updatedAgain.Equal(updated) || !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, parent.ID) {
		t.Fatal("equal fund changed the row")
	}
	if err := app.Fund(ctx, store, ownerAccess, parent.ID, 1_900_000); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("decrease = %v, want %v", err, domain.ErrBudget)
	}
	if err := app.Fund(ctx, store, ownerAccess, parent.ID, 2_250_000); err != nil {
		t.Fatal(err)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, parent.ID) {
		t.Fatal("fund equal to spent cleared paused")
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, parent.ID); budget != 2_250_000 {
		t.Fatalf("budget = %d, want 2250000", budget)
	}

	editor := app.Access{
		UserID:     otherUser,
		Allow:      "task:view,task:edit",
		SharedView: "{" + parent.ID + "}",
		SharedEdit: "{" + parent.ID + "}",
	}
	code, raw := postFund(t, store, editor, parent.ID, `{"kind":"fund","amount":3000000}`)
	if code != http.StatusOK {
		t.Fatalf("shared editor fund = %d %s", code, raw)
	}
	var funded struct {
		Budget int64 `json:"budget_micro_usd"`
		Spent  int64 `json:"spent_micro_usd"`
	}
	if err := json.Unmarshal(raw, &funded); err != nil {
		t.Fatal(err)
	}
	if funded.Budget != 3_000_000 || funded.Spent != 2_250_000 {
		t.Fatalf("funded = %+v", funded)
	}
	if taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, parent.ID) {
		t.Fatal("top-up above spent + held left the task paused")
	}

	viewer := app.Access{
		UserID:     otherUser,
		Allow:      "task:view",
		SharedView: "{" + parent.ID + "}",
	}
	code, raw = postFund(t, store, viewer, parent.ID, `{"kind":"fund","amount":4000000}`)
	if code != http.StatusNotFound || strings.TrimSpace(string(raw)) != `{"error":"no such task"}` {
		t.Fatalf("view-only fund = %d %s", code, raw)
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, parent.ID); budget != 3_000_000 {
		t.Fatalf("budget after view-only = %d", budget)
	}

	if _, err := app.ApplyTransition(ctx, store, ownerAccess, parent.ID, domain.Input{Kind: domain.Fail}); err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, ownerAccess, parent.ID, 4_000_000); !errors.Is(err, domain.ErrTerminal) {
		t.Fatalf("terminal fund = %v, want %v", err, domain.ErrTerminal)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET budget = budget - 1 WHERE id = $1::uuid`, parent.ID); err == nil || !containsTask(err, "tasks: budget") {
		t.Fatalf("sql decrease = %v", err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET budget = budget + 1 WHERE id = $1::uuid`, parent.ID); err == nil || !containsTask(err, "tasks: terminal") {
		t.Fatalf("sql terminal increase = %v", err)
	}
}

func TestFundWithSpentNearMaxInt64AndLiveHoldStaysPaused(t *testing.T) {
	const (
		openedBudget int64 = 2_000_000
		childBudget  int64 = 1
		raisedBudget int64 = 1_000_000_000
	)
	// MaxInt64 + a live hold of 1 wraps if Fund adds before it compares.
	const spent int64 = math.MaxInt64

	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", childBudget, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET spent = $2, paused = true
		WHERE id = $1::uuid`, parent.ID, spent); err != nil {
		t.Fatal(err)
	}

	var budget, held, gotSpent int64
	var paused bool
	if err := owner.QueryRow(ctx, `SELECT budget, held, spent, paused FROM tasks.tasks WHERE id = $1`, parent.ID).Scan(&budget, &held, &gotSpent, &paused); err != nil {
		t.Fatal(err)
	}
	if budget != openedBudget || held != childBudget || gotSpent != spent || !paused {
		t.Fatalf("before fund budget=%d held=%d spent=%d paused=%v, want budget=%d held=%d spent=%d paused=true", budget, held, gotSpent, paused, openedBudget, childBudget, spent)
	}

	if err := app.Fund(ctx, store, ownerAccess, parent.ID, raisedBudget); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT budget, held, spent, paused FROM tasks.tasks WHERE id = $1`, parent.ID).Scan(&budget, &held, &gotSpent, &paused); err != nil {
		t.Fatal(err)
	}
	if budget != raisedBudget || held != childBudget || gotSpent != spent || !paused {
		t.Fatalf("after fund budget=%d held=%d spent=%d paused=%v, want budget=%d held=%d spent=%d paused=true", budget, held, gotSpent, paused, raisedBudget, childBudget, spent)
	}

	var childGot int64
	var childStatus string
	if err := owner.QueryRow(ctx, `SELECT budget, status FROM tasks.tasks WHERE id = $1`, child.ID).Scan(&childGot, &childStatus); err != nil {
		t.Fatal(err)
	}
	if childGot != childBudget || childStatus != string(domain.Preparing) {
		t.Fatalf("child budget=%d status=%s, want budget=%d status=%s", childGot, childStatus, childBudget, domain.Preparing)
	}
}

func TestChildBudgetCannotBeRaisedDirectly(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Fund(ctx, store, ownerAccess, parent.ID, 2_000_000); err != nil {
		t.Fatal(err)
	}
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, child.ID); budget != 1 {
		t.Fatalf("child budget = %d, want 1", budget)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 1 {
		t.Fatalf("held = %d, want 1", held)
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, parent.ID); budget != 2_000_000 {
		t.Fatalf("root budget = %d, want 2000000", budget)
	}
	if status := taskStatus(t, owner, parent.ID); status != string(domain.Preparing) {
		t.Fatalf("parent status = %s", status)
	}

	raise := `UPDATE tasks.tasks SET budget = budget + 1 WHERE id = $1::uuid`
	if _, err := owner.Exec(ctx, raise, child.ID); err == nil || !containsTask(err, "tasks: budget") {
		t.Fatalf("child raise = %v", err)
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, child.ID); budget != 1 {
		t.Fatalf("child budget = %d, want 1", budget)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 1 {
		t.Fatalf("held = %d, want 1", held)
	}

	if _, err := owner.Exec(ctx, raise, parent.ID); err != nil {
		t.Fatal(err)
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, parent.ID); budget != 2_000_001 {
		t.Fatalf("root budget = %d, want 2000001", budget)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 1 {
		t.Fatalf("held = %d, want 1", held)
	}
	if budget := taskInt(t, owner, `SELECT budget FROM tasks.tasks WHERE id = $1`, child.ID); budget != 1 {
		t.Fatalf("child budget = %d, want 1", budget)
	}
}

func TestBudgetNoticeSkipsNonExhaustedPause(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := openRoot(t, store, "File the receipts")
	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET spent = 500000, paused = true
		WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if notices := taskInt(t, owner, `SELECT count(*) FROM tasks.chat_events WHERE task_id = $1`, task.ID); notices != 0 {
		t.Fatalf("notices = %d, want 0", notices)
	}
}

func TestConcurrentChildExhaustionWritesDistinctNoticeSeq(t *testing.T) {
	owner := up(t)
	base := pgtest.App(t, owner)
	cfg := base.Config().Copy()
	cfg.MaxConns = 40
	wide, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wide.Close)
	store := postgres.Store{Pool: wide, Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	// ChildCap is 6 open children. The notice race needs 32, so the insert
	// ceiling is off only while the rows are created. Spends run with it on.
	if _, err := owner.Exec(ctx, `ALTER TABLE tasks.tasks DISABLE TRIGGER tasks_limit_child`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), `ALTER TABLE tasks.tasks ENABLE TRIGGER tasks_limit_child`)
	})
	type group struct{ parent, a, b string }
	groups := make([]group, 16)
	for i := range groups {
		parent := openRoot(t, store, "File the receipts")
		groups[i] = group{
			parent.ID,
			insertOpenChild(t, owner, parent.ID, "Read the folder", 100),
			insertOpenChild(t, owner, parent.ID, "File them", 100),
		}
	}
	if _, err := owner.Exec(ctx, `ALTER TABLE tasks.tasks ENABLE TRIGGER tasks_limit_child`); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, len(groups)*2)
	for _, g := range groups {
		for _, id := range []string{g.a, g.b} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				<-start
				if err := app.Spend(context.Background(), store, ownerAccess, id, 100); err != nil {
					errs <- err
				}
			}(id)
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	for _, g := range groups {
		for _, id := range []string{g.a, g.b} {
			if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, id); spent != 100 {
				t.Fatalf("child spent = %d", spent)
			}
			if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, id) {
				t.Fatal("child spend did not pause")
			}
		}
		rows, err := owner.Query(ctx, `SELECT seq FROM tasks.chat_events WHERE task_id = $1::uuid ORDER BY seq`, g.parent)
		if err != nil {
			t.Fatal(err)
		}
		var seqs []int
		for rows.Next() {
			var seq int
			if err := rows.Scan(&seq); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			seqs = append(seqs, seq)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
			t.Fatalf("notice seq = %v, want [1 2]", seqs)
		}
	}
}

func TestReleaseEscrowSettlesFinishedChildSpend(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	parent, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "File the receipts"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := app.Spawn(ctx, store, ownerAccess, parent.ID, "File them", 1_000_000, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	toChecking(t, store, child.ID)
	if err := app.SaveResult(ctx, store, ownerAccess, child.ID, "Filed", "two pages"); err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, child.ID, 1_250_000); err != nil {
		t.Fatal(err)
	}
	passChild(t, store, child.ID)
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, parent.ID); spent != 1_250_000 {
		t.Fatalf("settled spent = %d, want 1250000", spent)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 0 {
		t.Fatalf("settled held = %d, want 0", held)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET status = 'Done', updated_at = now() WHERE id = $1::uuid`, child.ID); err != nil {
		t.Fatal(err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, parent.ID); spent != 1_250_000 {
		t.Fatalf("spent after second done = %d", spent)
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, parent.ID); held != 0 {
		t.Fatalf("held after second done = %d", held)
	}

	again, err := app.ApplyTransition(ctx, store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: "Close the week"})
	if err != nil {
		t.Fatal(err)
	}
	full, err := app.Spawn(ctx, store, ownerAccess, again.ID, "File them", 1_000_000, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	toChecking(t, store, full.ID)
	if err := app.Spend(ctx, store, ownerAccess, full.ID, 2_000_000); err != nil {
		t.Fatal(err)
	}
	passChild(t, store, full.ID)
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, again.ID); spent != 2_000_000 {
		t.Fatalf("exhausted parent spent = %d", spent)
	}
	if !taskBool(t, owner, `SELECT paused FROM tasks.tasks WHERE id = $1`, again.ID) {
		t.Fatal("settlement at budget did not pause the parent")
	}
	if held := taskInt(t, owner, `SELECT held FROM tasks.tasks WHERE id = $1`, again.ID); held != 0 {
		t.Fatalf("exhausted parent held = %d", held)
	}
	if err := app.Spend(ctx, store, ownerAccess, again.ID, 1); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("spend after settlement = %v", err)
	}
	if spent := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1`, again.ID); spent != 2_000_000 {
		t.Fatalf("spent after refused parent spend = %d", spent)
	}
}

func TestSpendUpdatesTaskTimestamp(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	task := openRoot(t, store, "File the receipts")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET updated_at = $2 WHERE id = $1::uuid`, task.ID, old); err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, task.ID, 1); err != nil {
		t.Fatal(err)
	}
	var spent int64
	var updated time.Time
	if err := owner.QueryRow(ctx, `SELECT spent, updated_at FROM tasks.tasks WHERE id = $1`, task.ID).Scan(&spent, &updated); err != nil {
		t.Fatal(err)
	}
	if spent != 1 || !updated.After(old) {
		t.Fatalf("spent = %d updated = %s", spent, updated)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET paused = true WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.Spend(ctx, store, ownerAccess, task.ID, 1); !errors.Is(err, domain.ErrExhausted) {
		t.Fatalf("paused spend = %v", err)
	}
	var again time.Time
	var spentAgain int64
	if err := owner.QueryRow(ctx, `SELECT spent, updated_at FROM tasks.tasks WHERE id = $1`, task.ID).Scan(&spentAgain, &again); err != nil {
		t.Fatal(err)
	}
	if spentAgain != 1 || !again.Equal(updated) {
		t.Fatalf("after refusal spent = %d updated = %s, want %s", spentAgain, again, updated)
	}
}

func insertOpenChild(t *testing.T, owner *pgxpool.Pool, parent, goal string, budget int64) string {
	t.Helper()
	ctx := context.Background()
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id, budget, allow)
		VALUES ($1::uuid, 'private', $2, 'Preparing', $3::uuid, $4, 'task:view')
		RETURNING id::text`, ownerUser, goal, parent, budget).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		VALUES ($1::uuid, 1, '', 'Preparing', 'open')`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

func toChecking(t *testing.T, store postgres.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, id, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, id, domain.Input{Kind: domain.Advance, Turn: domain.Sentinel}); err != nil {
		t.Fatal(err)
	}
}

func passChild(t *testing.T, store postgres.Store, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := app.RecordChecks(ctx, store, ownerAccess, id, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, id, domain.Input{Kind: domain.Pass}); err != nil {
		t.Fatal(err)
	}
}

func postFund(t *testing.T, store postgres.Store, access app.Access, id, body string) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	tasks.Handler{
		StepUp: func(*http.Request) bool { return true },
		Book:   store, List: store, View: store,
		Access: func(*http.Request) (app.Access, bool) { return access, true },
	}.Mount(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tasks/"+id, strings.NewReader(body)))
	return rec.Code, rec.Body.Bytes()
}

func taskInt(t *testing.T, owner *pgxpool.Pool, query, id string) int64 {
	t.Helper()
	var n int64
	if err := owner.QueryRow(context.Background(), query, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func taskBool(t *testing.T, owner *pgxpool.Pool, query, id string) bool {
	t.Helper()
	var flag bool
	if err := owner.QueryRow(context.Background(), query, id).Scan(&flag); err != nil {
		t.Fatal(err)
	}
	return flag
}
