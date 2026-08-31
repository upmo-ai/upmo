//go:build integration

package memory_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestPersonalUserID(t *testing.T) {
	owner, _ := up(t)
	ctx := context.Background()
	var cols string
	err := owner.QueryRow(ctx, `
		SELECT string_agg(column_name, ',' ORDER BY column_name)
		FROM information_schema.columns
		WHERE table_schema = 'memory' AND table_name = 'facts'`).Scan(&cols)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cols, "user_id") {
		t.Fatal("facts have no user_id")
	}
	for _, banned := range []string{"workspace_id", "owner_user_id", "visibility", "pending"} {
		if strings.Contains(cols, banned) {
			t.Fatalf("facts include %s", banned)
		}
	}
}

func TestProtectIsNotUsed(t *testing.T) {
	owner, app := up(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `SELECT identity.protect('memory.facts')`); err == nil {
		t.Fatal("identity.protect accepted a fact table")
	}
	var policies int
	if err := app.QueryRow(ctx, `
		SELECT count(*) FROM pg_policies
		WHERE schemaname = 'memory' AND policyname LIKE 'may_%'`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 0 {
		t.Fatalf("work-object policies on memory: %d", policies)
	}
}

func TestActiveClaimHashIsUniquePerPerson(t *testing.T) {
	owner, _ := up(t)
	ctx := context.Background()
	hash := bytes32("ab")
	other := bytes32("cd")
	if _, err := owner.Exec(ctx, insertFact, personA, hash, "ada prefers tea"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, insertFact, personA, hash, "ada prefers tea"); err == nil {
		t.Fatal("duplicate active claim_hash was accepted")
	}
	if _, err := owner.Exec(ctx, insertFact, personB, hash, "lee prefers tea"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, insertFact, personA, other, "ada prefers coffee"); err != nil {
		t.Fatal(err)
	}
}

func TestForgetIsATombstone(t *testing.T) {
	owner, app := up(t)
	ctx := context.Background()
	hash := bytes32("ab")
	var id string
	if err := owner.QueryRow(ctx, insertFact+` RETURNING id`, personA, hash, "ada prefers tea").Scan(&id); err != nil {
		t.Fatal(err)
	}
	withPerson(t, app, personA, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE memory.facts SET invalid_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	})
	if err := attempt(t, app, personA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM memory.facts WHERE id = $1`, id)
		return err
	}); err == nil {
		t.Fatal("forget deleted the row")
	}
	var claim string
	var gone bool
	if err := owner.QueryRow(ctx, `SELECT claim, invalid_at IS NOT NULL FROM memory.facts WHERE id = $1`, id).Scan(&claim, &gone); err != nil {
		t.Fatal(err)
	}
	if claim != "ada prefers tea" || !gone {
		t.Fatalf("tombstone = %q gone=%v", claim, gone)
	}
	if _, err := owner.Exec(ctx, insertFact, personA, hash, "ada prefers tea"); err != nil {
		t.Fatal("a forgotten hash blocked a later active row")
	}
}

func TestMixedEmbeddingModelRejected(t *testing.T) {
	owner, _ := up(t)
	ctx := context.Background()
	vec := half()
	if _, err := owner.Exec(ctx, insertEmbedded, personA, bytes32("ab"), "one", vec, "model-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, insertEmbedded, personA, bytes32("cd"), "two", vec, "model-b"); err == nil {
		t.Fatal("mixed embedding_model was accepted")
	}
	if _, err := owner.Exec(ctx, insertEmbedded, personA, bytes32("cd"), "two", vec, "model-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, insertEmbedded, personB, bytes32("ef"), "three", vec, "model-b"); err != nil {
		t.Fatal(err)
	}
}

func TestRowSecurityIsTheSignedInPerson(t *testing.T) {
	_, app := up(t)
	ctx := context.Background()
	withPerson(t, app, personA, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, insertFact, personA, bytes32("ab"), "ada prefers tea"); err != nil {
			t.Fatal(err)
		}
	})
	if err := attempt(t, app, personA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insertFact, personB, bytes32("cd"), "lee prefers coffee")
		return err
	}); err == nil {
		t.Fatal("wrote another person's fact")
	}
	withPerson(t, app, personB, func(tx pgx.Tx) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM memory.facts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("saw %d facts belonging to someone else", n)
		}
	})
}

func TestClaimTextIsImmutable(t *testing.T) {
	owner, app := up(t)
	ctx := context.Background()
	var id string
	if err := owner.QueryRow(ctx, insertFact+` RETURNING id`, personA, bytes32("ab"), "ada prefers tea").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := attempt(t, app, personA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE memory.facts SET claim = 'rewritten' WHERE id = $1`, id)
		return err
	}); err == nil {
		t.Fatal("claim text was updated in place")
	}
	withPerson(t, app, personA, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE memory.facts SET last_confirmed_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	})
}

const (
	personA = "018f3b2c-7a10-7c4a-8e11-111111111111"
	personB = "018f3b2c-7a10-7c4a-8e11-222222222222"
)

const insertFact = `
	INSERT INTO memory.facts (
	  user_id, trust, claim, claim_hash, valid_from, last_confirmed_at,
	  source_kind, source_id, source_seq, created_at
	) VALUES (
	  $1, 'user', $3, $2, now(), now(),
	  'person', '018f3b2c-7a10-7c4a-8e11-333333333333', 0, now()
	)`

const insertEmbedded = `
	INSERT INTO memory.facts (
	  user_id, trust, claim, claim_hash, valid_from, last_confirmed_at,
	  embedding, embedding_model, source_kind, source_id, source_seq, created_at
	) VALUES (
	  $1, 'tool', $3, $2, now(), now(),
	  $4::halfvec(1024), $5, 'person', '018f3b2c-7a10-7c4a-8e11-333333333333', 1, now()
	)`

func up(t *testing.T) (owner, app *pgxpool.Pool) {
	t.Helper()
	owner = pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, memory.Schema, memory.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

func withPerson(t *testing.T, pool *pgxpool.Pool, user string, fn func(pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, user); err != nil {
		t.Fatal(err)
	}
	fn(tx)
	if t.Failed() {
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func attempt(t *testing.T, pool *pgxpool.Pool, user string, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, user); err != nil {
		t.Fatal(err)
	}
	return fn(tx)
}

func bytes32(b string) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b[0]
	}
	return out
}

func half() string {
	parts := make([]string, 1024)
	for i := range parts {
		parts[i] = "0"
	}
	parts[0] = "1"
	return "[" + strings.Join(parts, ",") + "]"
}
