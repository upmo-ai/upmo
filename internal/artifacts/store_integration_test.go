//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
	id, err := store.Put(ctx, create, kept(ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf"))
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
		kept(ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := store.Put(ctx, artifacts.Access{UserID: otherUser, Allow: "artifact:create,artifact:view"},
		kept(otherUser, taskID, "notes.txt", artifacts.StoreObject, "task/notes.txt"))
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
		id, err := store.Put(ctx, create, kept(ownerUser, taskID, "secret", artifacts.StoreVolume, key))
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

func kept(owner, task, name, store, key string) artifacts.KeepRecord {
	return artifacts.KeepRecord{Owner: owner, Task: task, Name: name, Store: store, Key: key}
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
	id, err := store.Put(ctx, create, kept(ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf"))
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

func TestArtifacts_UPM222_MetadataAndOldestFirst(t *testing.T) {
	owner := up(t)
	app := pgtest.App(t, owner)
	store := artifacts.Store{Pool: app}
	ctx := context.Background()
	create := artifacts.Access{UserID: ownerUser, Allow: "artifact:create,artifact:view"}

	// Names sort the other way: a.md before b.txt. Oldest-first must not.
	first, err := store.Put(ctx, create, artifacts.KeepRecord{
		Owner: ownerUser, Task: taskID, Name: "b.txt", Store: artifacts.StoreVolume, Key: "b.txt", Size: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(ctx, create, artifacts.KeepRecord{
		Owner: ownerUser, Task: taskID, Name: "a.md", Store: artifacts.StoreVolume, Key: "a.md",
		Kind: "markdown", Mime: " Text/Markdown ; charset=UTF-8 ", Size: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	view := artifacts.Access{UserID: ownerUser, Allow: "artifact:view"}
	listed, err := store.ListByTask(ctx, view, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != first || listed[1].ID != second {
		t.Fatalf("order = %#v", listed)
	}
	if listed[0].Kind != "text" || listed[0].Mime != "text/plain" || listed[0].Size != 4 || listed[0].CreatedAt.IsZero() {
		t.Fatalf("first = %#v", listed[0])
	}
	if listed[1].Kind != "markdown" || listed[1].Mime != "text/markdown" || listed[1].Size != 2 {
		t.Fatalf("second = %#v", listed[1])
	}
	if listed[0].CreatedAt.After(listed[1].CreatedAt) {
		t.Fatalf("created_at out of order: %s then %s", listed[0].CreatedAt, listed[1].CreatedAt)
	}
	got, err := store.Get(ctx, view, first)
	if err != nil || got.Kind != "text" || got.Mime != "text/plain" || got.Size != 4 {
		t.Fatalf("get = %#v err %v", got, err)
	}

	other := artifacts.Access{UserID: otherUser, Allow: "artifact:view"}
	listed, err = store.ListByTask(ctx, other, taskID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("other list = %#v err %v", listed, err)
	}
	if _, err := store.Get(ctx, other, first); !errors.Is(err, artifacts.ErrGone) {
		t.Fatalf("other get = %v", err)
	}
}

func TestArtifacts_UPM222_ImmutableObjectKeyAndStore(t *testing.T) {
	owner := up(t)
	app := pgtest.App(t, owner)
	store := artifacts.Store{Pool: app}
	ctx := context.Background()
	id, err := store.Put(ctx, artifacts.Access{UserID: ownerUser, Allow: "artifact:create,artifact:view"},
		kept(ownerUser, taskID, "report.pdf", artifacts.StoreVolume, "task/report.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE artifacts.artifacts SET object_key = 'evil' WHERE id = $1::uuid`,
		`UPDATE artifacts.artifacts SET store = 'object' WHERE id = $1::uuid`,
	} {
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `
			SELECT set_config('upmo.user_id', $1, true),
			       set_config('upmo.allow', 'artifact:edit,artifact:view', true),
			       set_config('upmo.teams', '{}', true),
			       set_config('upmo.shared_view', '{}', true),
			       set_config('upmo.shared_edit', '{}', true)`, ownerUser)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, stmt, id)
		_ = tx.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" || !strings.Contains(pgErr.Message, "permission denied") {
			t.Fatalf("%s: err = %v, want SQLSTATE 42501 permission denied", stmt, err)
		}
	}
}

func TestArtifacts_UPM222_MigrationRejectsInvalidKindOrNegativeSize(t *testing.T) {
	pool := up(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO artifacts.artifacts (owner_user_id, visibility, task_id, name, store, object_key, kind)
		VALUES ($1::uuid, 'private', $2::uuid, 'x', 'volume', 'k', 'nope')`, ownerUser, taskID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("kind: err = %v, want SQLSTATE 23514", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO artifacts.artifacts (owner_user_id, visibility, task_id, name, store, object_key, size_bytes)
		VALUES ($1::uuid, 'private', $2::uuid, 'x', 'volume', 'k', -1)`, ownerUser, taskID)
	pgErr = nil
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("size: err = %v, want SQLSTATE 23514", err)
	}
}
