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

func TestFireOpensOneTaskPerKey(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	templateID := saveAutomationTemplate(t, store, "File the {{kind}}", 40)
	automationID, err := app.SaveAutomation(ctx, store, ownerAccess, templateID, domain.KindCron, " 0 9 * * 1 ")
	if err != nil {
		t.Fatal(err)
	}

	var kind, spec string
	if err := owner.QueryRow(ctx, `SELECT kind, spec FROM tasks.automations WHERE id = $1`, automationID).Scan(&kind, &spec); err != nil {
		t.Fatal(err)
	}
	if kind != domain.KindCron || spec != "0 9 * * 1" {
		t.Fatalf("trigger = %s %q", kind, spec)
	}

	first, reused, err := app.Fire(ctx, store, ownerAccess, automationID, "monday", map[string]string{"kind": "{{no}}"}, "")
	if err != nil || reused {
		t.Fatalf("first = %s reused %v err %v", first, reused, err)
	}
	again, reused, err := app.Fire(ctx, store, ownerAccess, automationID, "monday", map[string]string{"kind": "other"}, "")
	if err != nil || !reused || again != first {
		t.Fatalf("again = %s reused %v err %v", again, reused, err)
	}

	var goal string
	var budget int
	var automation string
	var children, runs int
	if err := owner.QueryRow(ctx, `
		SELECT goal, budget, automation_id::text,
		       (SELECT count(*) FROM tasks.tasks AS child WHERE child.parent_id = task.id),
		       (SELECT count(*) FROM tasks.runs WHERE task_id = task.id)
		FROM tasks.tasks AS task
		WHERE id = $1`, first).Scan(&goal, &budget, &automation, &children, &runs); err != nil {
		t.Fatal(err)
	}
	if goal != "File the { {no}}" || budget != 40 || automation != automationID || children != 0 || runs != 0 {
		t.Fatalf("task goal %q budget %d automation %s children %d runs %d", goal, budget, automation, children, runs)
	}
	var opened int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE automation_id = $1`, automationID).Scan(&opened); err != nil {
		t.Fatal(err)
	}
	if opened != 1 {
		t.Fatalf("opened = %d", opened)
	}

	if _, _, err := app.Fire(ctx, store, ownerAccess, automationID, "later", map[string]string{"kind": "receipts"}, first); !errors.Is(err, domain.ErrSelf) {
		t.Fatalf("self error = %v", err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE automation_id = $1`, automationID).Scan(&opened); err != nil {
		t.Fatal(err)
	}
	if opened != 1 {
		t.Fatalf("opened after self = %d", opened)
	}
	if _, _, err := app.Fire(ctx, store, otherAccess, automationID, "else", map[string]string{"kind": "receipts"}, ""); !errors.Is(err, app.ErrGone) {
		t.Fatalf("other person error = %v", err)
	}
	failures, _, _ := automationState(t, owner, automationID)
	if failures != 0 {
		t.Fatalf("failures after self = %d", failures)
	}
}

func TestBreakerAndOwnerLeave(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	templateID := saveAutomationTemplate(t, store, "File the {{kind}}", 40)
	automationID, err := app.SaveAutomation(ctx, store, ownerAccess, templateID, domain.KindEvent, "receipts.filed")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if _, _, err := app.Fire(ctx, store, ownerAccess, automationID, "miss", nil, ""); !errors.Is(err, domain.ErrSlot) {
			t.Fatalf("miss %d error = %v", i, err)
		}
	}
	if _, _, err := app.Fire(ctx, store, ownerAccess, automationID, "ok", map[string]string{"kind": "receipts"}, ""); err != nil {
		t.Fatal(err)
	}
	failures, mode, reason := automationState(t, owner, automationID)
	if failures != 0 || mode != domain.ModeOn || reason != "" {
		t.Fatalf("after success = %d %s %q", failures, mode, reason)
	}

	for i := 0; i < 2; i++ {
		if _, _, err := app.Fire(ctx, store, ownerAccess, automationID, "again", nil, ""); !errors.Is(err, domain.ErrSlot) {
			t.Fatalf("again %d error = %v", i, err)
		}
	}
	if _, _, err := app.Fire(ctx, store, ownerAccess, automationID, "third", nil, ""); !errors.Is(err, domain.ErrSlot) {
		t.Fatalf("third error = %v", err)
	}
	failures, mode, reason = automationState(t, owner, automationID)
	if failures != domain.BreakerLimit || mode != domain.ModeOff || reason != domain.ReasonBreaker {
		t.Fatalf("breaker = %d %s %q", failures, mode, reason)
	}
	if _, _, err := app.Fire(ctx, store, ownerAccess, automationID, "fourth", map[string]string{"kind": "receipts"}, ""); !errors.Is(err, domain.ErrMode) {
		t.Fatalf("off error = %v", err)
	}

	if err := app.OwnerLeft(ctx, store, ownerAccess, ownerUser); err != nil {
		t.Fatal(err)
	}
	_, mode, reason = automationState(t, owner, automationID)
	if mode != domain.ModeOff || reason != domain.ReasonOwnerLeft {
		t.Fatalf("left = %s %q", mode, reason)
	}
	if err := app.SetMode(ctx, store, ownerAccess, automationID, domain.ModeOn); !errors.Is(err, domain.ErrMode) {
		t.Fatalf("turn on after leaving error = %v", err)
	}

	admin := app.Access{UserID: ownerUser, Allow: "members:manage,task:view,task:edit"}
	if err := app.Reassign(ctx, store, ownerAccess, automationID, otherUser); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("reassign without admin error = %v", err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', 'task:view,task:edit', true)`, ownerUser); err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE tasks.automations SET owner_user_id = $2::uuid WHERE id = $1::uuid`, automationID, otherUser)
	if err == nil {
		t.Fatalf("direct reassign rows = %d", tag.RowsAffected())
	}
	if !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("direct reassign error = %v", err)
	}

	if err := app.Reassign(ctx, store, admin, automationID, otherUser); err != nil {
		t.Fatal(err)
	}
	_, mode, reason = automationState(t, owner, automationID)
	var ownerID string
	if err := owner.QueryRow(ctx, `SELECT owner_user_id::text FROM tasks.automations WHERE id = $1`, automationID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if ownerID != otherUser || mode != domain.ModeOff || reason != "" {
		t.Fatalf("reassigned owner %s mode %s reason %q", ownerID, mode, reason)
	}

	next := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	if err := app.SetMode(ctx, store, next, automationID, domain.ModeOn); err != nil {
		t.Fatal(err)
	}
	opened, _, err := app.Fire(ctx, store, next, automationID, "handed", map[string]string{"kind": "receipts"}, "")
	if err != nil || opened == "" {
		t.Fatalf("handed fire = %s %v", opened, err)
	}
}

func saveAutomationTemplate(t *testing.T, store postgres.Store, goal string, budget int) string {
	t.Helper()
	id, err := app.SaveTemplate(context.Background(), store, ownerAccess, "", domain.Template{
		Goal:    goal,
		AgentID: "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa",
		Budget:  budget,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func automationState(t *testing.T, owner *pgxpool.Pool, id string) (int, string, string) {
	t.Helper()
	var failures int
	var mode, reason string
	if err := owner.QueryRow(context.Background(), `SELECT failures, mode, reason FROM tasks.automations WHERE id = $1`, id).Scan(&failures, &mode, &reason); err != nil {
		t.Fatal(err)
	}
	return failures, mode, reason
}
