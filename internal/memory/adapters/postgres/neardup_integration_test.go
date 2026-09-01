//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestNearDupUsesPgvectorCosine(t *testing.T) {
	pool := up(t)
	ctx := context.Background()
	same := axis(0)
	other := axis(1)
	if _, err := pool.Exec(ctx, insertEmbedded, person, bytes32(1), "ada prefers tea", same, "model-a"); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	action, reason, err := postgres.NearDup(ctx, tx, person, same)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if action != "reinforce" || reason != "active_near" {
		t.Fatalf("same vector = %s %s", action, reason)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	action, reason, err = postgres.NearDup(ctx, tx, person, other)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if action != "insert" || reason != "new" {
		t.Fatalf("orthogonal vector = %s %s", action, reason)
	}

	if _, err := pool.Exec(ctx, `UPDATE memory.facts SET invalid_at = now() WHERE user_id = $1`, person); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	action, reason, err = postgres.NearDup(ctx, tx, person, same)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if action != "drop" || reason != "forgotten_near" {
		t.Fatalf("forgotten vector = %s %s", action, reason)
	}
}

const person = "018f3b2c-7a10-7c4a-8e11-111111111111"

const insertEmbedded = `
	INSERT INTO memory.facts (
	  user_id, trust, claim, claim_hash, valid_from, last_confirmed_at,
	  embedding, embedding_model, source_kind, source_id, source_seq, created_at
	) VALUES (
	  $1, 'tool', $3, $2, now(), now(),
	  $4::halfvec(1024), $5, 'person', '018f3b2c-7a10-7c4a-8e11-333333333333', 1, now()
	)`

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, memory.Schema, memory.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func axis(i int) string {
	parts := make([]string, 1024)
	for n := range parts {
		parts[n] = "0"
	}
	parts[i] = "1"
	return "[" + strings.Join(parts, ",") + "]"
}
