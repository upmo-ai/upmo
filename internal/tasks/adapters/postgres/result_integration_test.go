//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestParentCancelSetsTheCauseOnOpenChildren(t *testing.T) {
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
	if _, err := app.Cancel(ctx, store, ownerAccess, first.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := app.Cancel(ctx, store, ownerAccess, parent.ID); err != nil {
		t.Fatal(err)
	}
	if cause := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, parent.ID); cause != "" {
		t.Fatalf("parent cause = %q", cause)
	}
	if cause := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, first.ID); cause != "" {
		t.Fatalf("direct cancel cause = %q", cause)
	}
	if status := taskStatus(t, owner, second.ID); status != string(domain.Failed) {
		t.Fatalf("child status = %s", status)
	}
	if cause := taskText(t, owner, `SELECT cause FROM tasks.tasks WHERE id = $1`, second.ID); cause != domain.ParentCanceled {
		t.Fatalf("propagated cause = %q", cause)
	}
}

func TestSaveResultNeutralisesAndStoresOnce(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()
	root := openRoot(t, store, "File the receipts")
	if err := app.SaveResult(ctx, store, ownerAccess, root.ID, " See {{prior}} ", "two pages"); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("preparing result = %v", err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET result = '{"summary":"See","detail":"two pages"}'::jsonb
		WHERE id = $1::uuid`, root.ID); !strings.Contains(err.Error(), "tasks: result") {
		t.Fatalf("preparing sql = %v", err)
	}

	if _, err := app.ApplyTransition(ctx, store, ownerAccess, root.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, root.ID, " See {{prior}} ", "two pages"); err != nil {
		t.Fatal(err)
	}
	summary, detail := resultText(t, owner, root.ID)
	if summary != "See { {prior}}" || detail != "two pages" {
		t.Fatalf("summary = %q detail = %q", summary, detail)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, root.ID, "Again", "two pages"); err != nil {
		t.Fatal(err)
	}
	summary, detail = resultText(t, owner, root.ID)
	if summary != "Again" || detail != "two pages" {
		t.Fatalf("overwrite = %q %q", summary, detail)
	}

	child, err := app.Spawn(ctx, store, ownerAccess, root.ID, "File them", 1, "task:view")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, child.ID, "Child", "two pages"); !errors.Is(err, domain.ErrInput) {
		t.Fatalf("child preparing = %v", err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, child.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, child.ID, "Child", "two pages"); err != nil {
		t.Fatal(err)
	}
	summary, detail = resultText(t, owner, child.ID)
	if summary != "Child" || detail != "two pages" {
		t.Fatalf("child = %q %q", summary, detail)
	}

	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET result = '{"summary": null, "detail": ""}'::jsonb
		WHERE id = $1::uuid`, root.ID); !strings.Contains(err.Error(), "tasks: result") {
		t.Fatalf("null summary = %v", err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET result = '{"summary": "Filed", "detail": null}'::jsonb
		WHERE id = $1::uuid`, root.ID); !strings.Contains(err.Error(), "tasks: result") {
		t.Fatalf("null detail = %v", err)
	}
	long := strings.Repeat("a", 32000)
	if err := app.SaveResult(ctx, store, ownerAccess, root.ID, "Long", long); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"summary": "Long", "detail": long})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET result = $2::jsonb WHERE id = $1::uuid`, root.ID, body); err != nil {
		t.Fatal(err)
	}
	summary, detail = resultText(t, owner, root.ID)
	if summary != "Long" || detail != long {
		t.Fatalf("32000 detail length = %d", len(detail))
	}
	tooLong := strings.Repeat("a", 32001)
	if err := app.SaveResult(ctx, store, ownerAccess, root.ID, "Long", tooLong); !errors.Is(err, domain.ErrResult) {
		t.Fatalf("32001 app = %v", err)
	}
	summary, detail = resultText(t, owner, root.ID)
	if summary != "Long" || detail != long {
		t.Fatal("32001 app changed the row")
	}
	tooBody, err := json.Marshal(map[string]string{"summary": "Long", "detail": tooLong})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET result = $2::jsonb WHERE id = $1::uuid`, root.ID, tooBody); !strings.Contains(err.Error(), "tasks: result") {
		t.Fatalf("32001 sql = %v", err)
	}
	summary, detail = resultText(t, owner, root.ID)
	if summary != "Long" || detail != long {
		t.Fatal("32001 sql changed the row")
	}

	if _, err := app.ApplyTransition(ctx, store, ownerAccess, root.ID, domain.Input{Kind: domain.Advance, Turn: domain.Sentinel}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, root.ID, "Filed", "two pages"); err != nil {
		t.Fatal(err)
	}
	summary, detail = resultText(t, owner, root.ID)
	if summary != "Filed" || detail != "two pages" {
		t.Fatalf("verifying = %q %q", summary, detail)
	}
	if _, err := app.RecordChecks(ctx, store, ownerAccess, root.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, ownerAccess, root.ID, domain.Input{Kind: domain.Pass}); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveResult(ctx, store, ownerAccess, root.ID, "Later", "two pages"); !errors.Is(err, domain.ErrResult) || errors.Is(err, domain.ErrTerminal) {
		t.Fatalf("done result = %v", err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE tasks.tasks
		SET result = '{"summary":"Later","detail":"two pages"}'::jsonb
		WHERE id = $1::uuid`, root.ID); !strings.Contains(err.Error(), "tasks: result") {
		t.Fatalf("done sql = %v", err)
	}
}

func resultText(t *testing.T, owner *pgxpool.Pool, id string) (string, string) {
	t.Helper()
	var summary, detail string
	if err := owner.QueryRow(context.Background(), `
		SELECT result->>'summary', result->>'detail'
		FROM tasks.tasks WHERE id = $1`, id).Scan(&summary, &detail); err != nil {
		t.Fatal(err)
	}
	return summary, detail
}

func TestOneOpenTaskHoldsAWorktree(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	parent := openRoot(t, store, "File the receipts")
	other := openRoot(t, store, "Close the week")
	if err := app.Claim(ctx, store, ownerAccess, parent.ID, " repo/src "); err != nil {
		t.Fatal(err)
	}
	if err := app.Claim(ctx, store, ownerAccess, other.ID, "repo/src"); !errors.Is(err, domain.ErrWorktree) {
		t.Fatalf("second claim error = %v, want %v", err, domain.ErrWorktree)
	}
	if _, err := app.Cancel(ctx, store, ownerAccess, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.Claim(ctx, store, ownerAccess, other.ID, "repo/src"); err != nil {
		t.Fatalf("claim after the first finished: %v", err)
	}
}

func taskText(t *testing.T, owner *pgxpool.Pool, query, id string) string {
	t.Helper()
	var text string
	if err := owner.QueryRow(context.Background(), query, id).Scan(&text); err != nil {
		t.Fatal(err)
	}
	return text
}
