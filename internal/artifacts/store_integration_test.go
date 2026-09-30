//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestPutOutlivesTheSandbox(t *testing.T) {
	owner := up(t)
	app := pgtest.App(t, owner)
	store := artifacts.Store{Pool: app}
	ctx := context.Background()

	create := artifacts.Access{UserID: ownerUser, Allow: "artifact:create,artifact:view"}
	id, err := store.Put(ctx, create, ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf")
	if err != nil {
		t.Fatal(err)
	}

	var key string
	if err := owner.QueryRow(ctx, `SELECT object_key FROM artifacts.artifacts WHERE id = $1::uuid`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "task/report.pdf" {
		t.Fatalf("key = %q", key)
	}

	other := artifacts.Access{UserID: otherUser, Allow: "artifact:view"}
	if n := visible(t, app, other, id); n != 0 {
		t.Fatalf("other person rows = %d", n)
	}
}

func TestEraseRemovesOnlyTheOwner(t *testing.T) {
	owner := up(t)
	store := artifacts.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	mine, err := store.Put(ctx, artifacts.Access{UserID: ownerUser, Allow: "artifact:create,artifact:view"},
		ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := store.Put(ctx, artifacts.Access{UserID: otherUser, Allow: "artifact:create,artifact:view"},
		otherUser, taskID, "notes.txt", artifacts.StoreObject, "task/notes.txt")
	if err != nil {
		t.Fatal(err)
	}

	keys, err := store.Erase(ctx, artifacts.Access{UserID: ownerUser, Allow: "artifact:delete,artifact:view"}, ownerUser)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(keys, []string{"task/report.pdf"}) {
		t.Fatalf("keys = %#v", keys)
	}
	if n := rows(t, owner, `SELECT count(*) FROM artifacts.artifacts WHERE id = $1::uuid`, mine); n != 0 {
		t.Fatalf("erased rows = %d", n)
	}
	if n := rows(t, owner, `SELECT count(*) FROM artifacts.artifacts WHERE id = $1::uuid`, theirs); n != 1 {
		t.Fatalf("other person rows = %d", n)
	}
}

func TestPutRefusesABadKey(t *testing.T) {
	owner := up(t)
	store := artifacts.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	create := artifacts.Access{UserID: ownerUser, Allow: "artifact:create"}

	for _, key := range []string{"/etc/passwd", "../secret"} {
		id, err := store.Put(ctx, create, ownerUser, taskID, "secret", artifacts.StoreVolume, key)
		if !errors.Is(err, artifacts.ErrArtifact) {
			t.Fatalf("key %q: error = %v", key, err)
		}
		if id != "" {
			t.Fatalf("key %q: id = %q", key, id)
		}
	}
	if n := rows(t, owner, `SELECT count(*) FROM artifacts.artifacts`); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}

// visible counts the rows a person can see for id under row security.
func visible(t *testing.T, app *pgxpool.Pool, access artifacts.Access, id string) int {
	t.Helper()
	ctx := context.Background()
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', '{}', true),
		       set_config('upmo.shared_edit', '{}', true)`,
		access.UserID, access.Allow)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM artifacts.artifacts WHERE id = $1::uuid`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func rows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, artifacts.Schema, artifacts.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestListByTaskAndGet(t *testing.T) {
	owner := up(t)
	app := pgtest.App(t, owner)
	store := artifacts.Store{Pool: app}
	ctx := context.Background()
	create := artifacts.Access{UserID: ownerUser, Allow: "artifact:create,artifact:view"}
	id, err := store.Put(ctx, create, ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	view := artifacts.Access{UserID: ownerUser, Allow: "artifact:view"}
	listed, err := store.ListByTask(ctx, view, taskID)
	if err != nil || len(listed) != 1 || listed[0].ID != id || listed[0].Name != "report.pdf" {
		t.Fatalf("listed = %#v err %v", listed, err)
	}
	got, err := store.Get(ctx, view, id)
	if err != nil || got.Key != "task/report.pdf" {
		t.Fatalf("get = %#v err %v", got, err)
	}
	other := artifacts.Access{UserID: otherUser, Allow: "artifact:view"}
	listed, err = store.ListByTask(ctx, other, taskID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("other list = %#v err %v", listed, err)
	}
	if _, err := store.Get(ctx, other, id); !errors.Is(err, artifacts.ErrGone) {
		t.Fatalf("other get = %v", err)
	}
}
