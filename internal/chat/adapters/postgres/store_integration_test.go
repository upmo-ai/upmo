//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	ownerUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser = "018f3b2c-7a10-7c4a-8e11-222222222222"
)

var (
	ownerAccess = app.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit"}
	otherAccess = app.Access{UserID: otherUser, Allow: "chat:view,chat:edit"}
)

func TestOpenLeavesTheSession(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()

	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	id := opened.ID
	if err := app.Say(ctx, store, ownerAccess, id, " hello ", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	settle(t, owner, id)
	if err := app.Say(ctx, store, ownerAccess, id, "again", app.Picks{}); err != nil {
		t.Fatal(err)
	}

	got, err := app.Read(ctx, store, ownerAccess, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 || got.Lines[0].Body != "hello" || got.Lines[1].Body != "again" {
		t.Fatalf("lines = %#v", got.Lines)
	}
	if got.Lines[0].ID == "" || got.Lines[0].ID == got.Lines[1].ID {
		t.Fatalf("line ids = %q %q", got.Lines[0].ID, got.Lines[1].ID)
	}
	for _, line := range got.Lines {
		if line.Role != "user" || line.Model != nil || line.Tokens != 0 || line.CostMicroUSD != nil {
			t.Fatalf("a said line = %+v", line)
		}
	}

	var sessions int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM identity.sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions = %d", sessions)
	}
	if _, err := app.Read(ctx, store, otherAccess, id); !errors.Is(err, app.ErrGone) {
		t.Fatalf("other person error = %v", err)
	}

	_, err = owner.Exec(ctx, `UPDATE chat.lines SET body = 'changed' WHERE chat_id = $1`, id)
	if err == nil || !strings.Contains(err.Error(), "chat: writer") {
		t.Fatalf("line rewrite error = %v", err)
	}
}

func TestDeleteRequiresChatDeletePermissionInStore(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(ctx, ownerAccess, opened.ID); !errors.Is(err, app.ErrAccess) {
		t.Fatalf("delete without chat:delete = %v", err)
	}
	var exists bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat.chats WHERE id = $1::uuid)`, opened.ID).Scan(&exists); err != nil || !exists {
		t.Fatalf("chat exists=%v err=%v", exists, err)
	}
}

// quietStore is a store whose lines are asked a model for, by a queue that
// only records the job, so that nothing writes a reply into the chat.
func quietStore(t *testing.T, owner *pgxpool.Pool) postgres.Store {
	t.Helper()
	return postgres.Store{Pool: pgtest.App(t, owner), Enqueuer: &recorder{}, ReplyRoute: "chat"}
}

// settle fails the running run of a chat, as a process death would leave it.
func settle(t *testing.T, owner *pgxpool.Pool, chatID string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `
		UPDATE chat.runs SET state = 'failed', claim = NULL, history = NULL, finished_at = now()
		WHERE chat_id = $1::uuid AND state = 'running'`, chatID); err != nil {
		t.Fatal(err)
	}
}

func TestSayWithRepliesOffStoresAFailedReplyInTheSameTransaction(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	got, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil || len(got.Lines) != 2 {
		t.Fatalf("lines = %+v %v", got.Lines, err)
	}
	user, reply := got.Lines[0], got.Lines[1]
	if reply.Role != "assistant" || reply.Status != "failed" || reply.Body != "" ||
		reply.ReplyTo == nil || *reply.ReplyTo != user.ID ||
		len(reply.Attempts) != 1 || reply.Attempts[0].Class != "no_replies" {
		t.Fatalf("reply = %+v", reply)
	}
	if domain.Reason(reply.Attempts[0].Class) != "replies are not set up" {
		t.Fatalf("reason = %q", domain.Reason(reply.Attempts[0].Class))
	}

	// A line that is refused leaves no reply behind: both go together.
	if err := app.Say(ctx, store, otherAccess, opened.ID, "no", app.Picks{}); !errors.Is(err, app.ErrGone) {
		t.Fatalf("other person error = %v", err)
	}
	if again, _ := app.Read(ctx, store, ownerAccess, opened.ID); len(again.Lines) != 2 {
		t.Fatalf("lines = %+v", again.Lines)
	}
}

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	// The stores record audit events, so the audit schema comes up first.
	if err := audit.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestTailReadsOneSnapshotSoARepliesDeltasAndLineNeverComeTogether(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	var runID, runLineID string
	if err := owner.QueryRow(ctx, `SELECT id::text, line_id::text FROM chat.runs WHERE chat_id = $1::uuid`, opened.ID).Scan(&runID, &runLineID); err != nil {
		t.Fatal(err)
	}
	c, err := store.Claim(ctx, ownerAccess, app.ReplyJobArgs{ChatID: opened.ID, LineID: runLineID, ReplyID: runID, Round: 1, UserID: ownerUser}, "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01")
	if err != nil || c.State != app.ClaimGo {
		t.Fatalf("claim = %+v %v", c, err)
	}
	if err := store.PutDelta(ctx, ownerAccess, c, 1, "text", "partial"); err != nil {
		t.Fatal(err)
	}

	// The reply finishes between the two reads of a tail. Under one snapshot
	// the tail still shows the deltas and not the line; read again, it shows
	// the line and not the deltas.
	reader := postgres.WithBetweenReads(store, func() {
		err := store.Finish(ctx, ownerAccess, c, app.ReplyEnd{Model: "m1", Attempts: []domain.Attempt{{Model: "m1", Class: "transient"}}})
		if err != nil {
			t.Error(err)
		}
	})
	tail, err := reader.Tail(ctx, ownerAccess, opened.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	deltas, lines := 0, 0
	for _, e := range tail.Events {
		switch e.Kind {
		case app.EventDelta:
			deltas++
		case app.EventLine:
			if e.LineID == runID {
				lines++
			}
		}
	}
	if deltas != 2 || lines != 0 {
		t.Fatalf("racing tail: %d deltas, %d lines of the reply", deltas, lines)
	}
	tail, err = store.Tail(ctx, ownerAccess, opened.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	deltas, lines = 0, 0
	for _, e := range tail.Events {
		switch e.Kind {
		case app.EventDelta:
			deltas++
		case app.EventLine:
			if e.LineID == runID {
				lines++
			}
		}
	}
	if deltas != 0 || lines != 1 {
		t.Fatalf("later tail: %d deltas, %d lines of the reply", deltas, lines)
	}
}
