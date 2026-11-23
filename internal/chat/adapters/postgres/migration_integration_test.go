//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
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

func TestKeepChatTriggerAndRevokeProtectRollingSummaryMonotonicity(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) error {
		return store.NarrowedTx(ctx, scopeOf(ownerAccess, opened.ID), func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('upmo.chat_turn_commit', 'spoofed', true)`); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	if err := exec(`UPDATE chat.chats SET rolling_summary = 'injected', summary_up_to_seq = 999 WHERE id = $1::uuid`, opened.ID); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("direct summary update = %v", err)
	}
	claim, runID := claimRun(t, ctx, store, owner, opened.ID)
	var lineID string
	for seq := 2; seq <= 6; seq++ {
		if err := owner.QueryRow(ctx, `INSERT INTO chat.lines (chat_id, seq, body, role, model, reply_to) VALUES ($1::uuid, $2, 'answer', 'assistant', 'm1', $3::uuid) RETURNING id::text`, opened.ID, seq, claim.Job.LineID).Scan(&lineID); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec(`SELECT chat.advance_chat_summary($1::uuid, $2::uuid, $3::uuid, 'wrong', $4::uuid, 6)`, opened.ID, runID, "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa99", lineID); err == nil || !strings.Contains(err.Error(), "chat: fenced") {
		t.Fatalf("wrong claim = %v", err)
	}
	if err := exec(`SELECT chat.advance_chat_summary($1::uuid, $2::uuid, $3::uuid, 'summary', $4::uuid, 6)`, opened.ID, runID, claim.Token, lineID); err != nil {
		t.Fatal(err)
	}
	var line4 string
	if err := owner.QueryRow(ctx, `SELECT id::text FROM chat.lines WHERE chat_id = $1::uuid AND seq = 4`, opened.ID).Scan(&line4); err != nil {
		t.Fatal(err)
	}
	if err := exec(`SELECT chat.advance_chat_summary($1::uuid, $2::uuid, $3::uuid, 'decreased', $4::uuid, 4)`, opened.ID, runID, claim.Token, line4); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE chat.chats SET summary_up_to_seq = 4 WHERE id = $1::uuid`, opened.ID); err == nil || !strings.Contains(err.Error(), "chat: summary") {
		t.Fatalf("table owner decrease = %v", err)
	}
	var summary string
	var cursor int
	if err := owner.QueryRow(ctx, `SELECT rolling_summary, summary_up_to_seq FROM chat.chats WHERE id = $1::uuid`, opened.ID).Scan(&summary, &cursor); err != nil {
		t.Fatal(err)
	}
	if summary != "summary" || cursor != 6 {
		t.Fatalf("summary cursor = %q/%d", summary, cursor)
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
