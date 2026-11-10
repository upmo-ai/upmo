//go:build integration

package postgres_test

import (
	"context"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// migrationsUpTo is the chat migrations with only the files named.
func migrationsUpTo(t *testing.T, names ...string) fs.FS {
	t.Helper()
	files := fstest.MapFS{}
	for _, name := range names {
		raw, err := fs.ReadFile(chat.Migrations(), name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: raw}
	}
	return files
}

// shape describes the chat schema: columns, constraints, indexes, triggers,
// functions and policies, one line each, sorted by the query.
func shape(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable
		       || ' ' || COALESCE(column_default, '')
		FROM information_schema.columns WHERE table_schema = 'chat'
		UNION ALL
		SELECT 'constraint ' || cls.relname || ' ' || con.conname || ' ' || pg_get_constraintdef(con.oid)
		FROM pg_constraint AS con
		JOIN pg_class AS cls ON cls.oid = con.conrelid
		JOIN pg_namespace AS nsp ON nsp.oid = cls.relnamespace WHERE nsp.nspname = 'chat'
		UNION ALL
		SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'chat'
		UNION ALL
		SELECT 'trigger ' || cls.relname || ' ' || pg_get_triggerdef(trg.oid)
		FROM pg_trigger AS trg
		JOIN pg_class AS cls ON cls.oid = trg.tgrelid
		JOIN pg_namespace AS nsp ON nsp.oid = cls.relnamespace
		WHERE nsp.nspname = 'chat' AND NOT trg.tgisinternal
		UNION ALL
		SELECT 'function ' || proc.proname || '(' || pg_get_function_arguments(proc.oid) || ')'
		FROM pg_proc AS proc JOIN pg_namespace AS nsp ON nsp.oid = proc.pronamespace WHERE nsp.nspname = 'chat'
		UNION ALL
		SELECT 'policy ' || tablename || ' ' || policyname || ' ' || cmd || ' ' || COALESCE(qual, '') || ' ' || COALESCE(with_check, '')
		FROM pg_policies WHERE schemaname = 'chat'
		UNION ALL
		SELECT 'table ' || relname || ' rls ' || relrowsecurity || ' ' || relforcerowsecurity
		FROM pg_class AS cls JOIN pg_namespace AS nsp ON nsp.oid = cls.relnamespace
		WHERE nsp.nspname = 'chat' AND cls.relkind = 'r' AND cls.relname <> 'goose_db_version'
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		out = append(out, line)
	}
	return out
}

func triggerCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_trigger WHERE tgname IN ('chat_deltas_stream_notify', 'chat_lines_stream_notify')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStreamMigrationDownRestoresTheLinesShapeExactly(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, migrationsUpTo(t, "00001_chat.sql", "00002_chat_lines.sql")); err != nil {
		t.Fatal(err)
	}
	before := shape(t, pool)
	if err := migrate.Up(ctx, pool, chat.Schema, migrationsUpTo(t, "00001_chat.sql", "00002_chat_lines.sql", "00003_chat_stream.sql")); err != nil {
		t.Fatal(err)
	}
	if got := triggerCount(t, pool); got != 2 {
		t.Fatalf("triggers after up = %d", got)
	}
	if reflect.DeepEqual(before, shape(t, pool)) {
		t.Fatal("the migration changed nothing")
	}

	// Rows that only the new shape allows are given up on the way down: the
	// failed reply goes and the long one is cut to what 00002 allows.
	var chatID, userLine string
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat.chats (owner_user_id, visibility) VALUES ($1::uuid, 'private') RETURNING id::text`, ownerUser).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat.lines (chat_id, seq, body) VALUES ($1::uuid, 1, 'q') RETURNING id::text`, chatID).Scan(&userLine); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, status, reply_to)
		VALUES ($1::uuid, 2, 'assistant', '', 'm1', 'failed', $2::uuid),
		       ($1::uuid, 3, 'assistant', repeat('x', 5000), 'm1', 'done', $2::uuid)`, chatID, userLine); err != nil {
		t.Fatal(err)
	}

	raw, err := fs.ReadFile(chat.Migrations(), "00003_chat_stream.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(raw), "-- +goose Down")
	if !found {
		t.Fatal("no down section")
	}
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	if got := shape(t, pool); !reflect.DeepEqual(before, got) {
		t.Fatalf("shape after down differs from 00002:\nbefore %q\nafter  %q", before, got)
	}
	var lines, longest int
	if err := pool.QueryRow(ctx, `SELECT count(*), max(char_length(body)) FROM chat.lines WHERE chat_id = $1::uuid`, chatID).Scan(&lines, &longest); err != nil {
		t.Fatal(err)
	}
	if lines != 2 || longest != 4000 {
		t.Fatalf("lines %d longest %d after down", lines, longest)
	}
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
	if got := triggerCount(t, pool); got != 2 {
		t.Fatalf("triggers after up = %d", got)
	}
	if err := migrate.Down(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	if got := triggerCount(t, pool); got != 2 {
		t.Fatalf("triggers after up again = %d", got)
	}
}

func TestPicksMigrationDownRestoresTheLinesShapeExactly(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, migrationsUpTo(t, "00001_chat.sql", "00002_chat_lines.sql", "00003_chat_stream.sql")); err != nil {
		t.Fatal(err)
	}
	before := shape(t, pool)
	if err := migrate.Up(ctx, pool, chat.Schema, migrationsUpTo(t, "00001_chat.sql", "00002_chat_lines.sql", "00003_chat_stream.sql", "00004_chat_picks.sql")); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, shape(t, pool)) {
		t.Fatal("the migration changed nothing")
	}
	raw, err := fs.ReadFile(chat.Migrations(), "00004_chat_picks.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(raw), "-- +goose Down")
	if !found {
		t.Fatal("no down section")
	}
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	if got := shape(t, pool); !reflect.DeepEqual(before, got) {
		t.Fatalf("shape after down differs from 00003:\nbefore %q\nafter  %q", before, got)
	}
}

func TestAgentsMigrationDownRestoresThePicksShapeExactly(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	upTo4 := migrationsUpTo(t, "00001_chat.sql", "00002_chat_lines.sql", "00003_chat_stream.sql", "00004_chat_picks.sql")
	if err := migrate.Up(ctx, pool, chat.Schema, upTo4); err != nil {
		t.Fatal(err)
	}
	before := shape(t, pool)
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, shape(t, pool)) {
		t.Fatal("the migration changed nothing")
	}
	raw, err := fs.ReadFile(chat.Migrations(), "00005_chat_agents.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(raw), "-- +goose Down")
	if !found {
		t.Fatal("no down section")
	}
	if _, err := pool.Exec(ctx, down); err != nil {
		t.Fatal(err)
	}
	if got := shape(t, pool); !reflect.DeepEqual(before, got) {
		t.Fatalf("shape after down differs from 00004:\nbefore %q\nafter  %q", before, got)
	}
}
