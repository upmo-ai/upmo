//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func triggerCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_trigger WHERE tgname IN ('chat_deltas_stream_notify', 'chat_lines_stream_notify')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStreamMigrationGoesDownAndUpAgain(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	var chatAgents *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('chat.agents')::text`).Scan(&chatAgents); err != nil {
		t.Fatal(err)
	}
	if chatAgents != nil {
		t.Fatalf("chat.agents still exists: %q", *chatAgents)
	}
	if got := triggerCount(t, pool); got != 2 {
		t.Fatalf("triggers after up = %d", got)
	}
	if err := migrate.Down(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	var ns *string
	if err := pool.QueryRow(ctx, `SELECT to_regnamespace('chat')::text`).Scan(&ns); err != nil {
		t.Fatal(err)
	}
	if ns != nil {
		t.Fatalf("chat schema still exists after down: %q", *ns)
	}
	if got := triggerCount(t, pool); got != 0 {
		t.Fatalf("triggers after down = %d, want 0", got)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	if got := triggerCount(t, pool); got != 2 {
		t.Fatalf("triggers after up again = %d", got)
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version_id) FROM chat.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("chat version = %d, want 1", version)
	}
}
