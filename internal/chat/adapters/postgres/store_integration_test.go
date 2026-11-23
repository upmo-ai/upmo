//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	httpapi "github.com/upmo-ai/upmo/internal/chat/adapters/http"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
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

func TestGetChatReturnsStoredLines(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	claim, runID := claimRun(t, ctx, store, owner, opened.ID)
	if err := store.PutDelta(ctx, ownerAccess, claim, 1, "text", "The stored answer."); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRound(ctx, ownerAccess, claim, app.RoundCommit{
		Turn: agentloop.TurnCommit{
			RunID: runID, ObjectID: opened.ID, UserID: ownerUser, SeqStart: 1,
			Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "The stored answer."}}, Model: "m1",
		},
		Tail: app.Pending{Seq: 1, Text: "The stored answer."},
		End:  app.RoundDone,
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	ownerAccessWithDelete := ownerAccess
	ownerAccessWithDelete.Allow += ",chat:delete"
	(httpapi.Handler{Book: store, Access: func(*http.Request) (app.Access, bool) { return ownerAccessWithDelete, true }}).Mount(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats/"+opened.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/chats/{id} = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		CanEdit   bool `json:"can_edit"`
		CanDelete bool `json:"can_delete"`
		Lines     []struct {
			Body string `json:"body"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Lines) != 2 || body.Lines[0].Body != "hello" || body.Lines[1].Body != "The stored answer." {
		t.Fatalf("GET chat lines = %+v", body.Lines)
	}
	if !body.CanEdit || !body.CanDelete {
		t.Fatalf("GET chat permissions = edit:%v delete:%v", body.CanEdit, body.CanDelete)
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

func TestLinesBodyCheckEnforces32000UserAnd64000AssistantRunesAsAppRole(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)
	execAsApp := func(fn func(tx pgx.Tx) error) error {
		tx, err := appPool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `
			SELECT set_config('upmo.user_id', $1, true),
			       set_config('upmo.allow', $2, true),
			       set_config('upmo.teams', '{}', true),
			       set_config('upmo.shared_view', '{}', true),
			       set_config('upmo.shared_edit', '{}', true)`,
			ownerUser, ownerAccess.Allow); err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	// User line of 32,001 runes must be refused by lines_body_check.
	if err := execAsApp(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
			VALUES ($1::uuid, 1, 'user', repeat('u', 32001), $2::uuid)`, opened.ID, ownerUser)
		return err
	}); err == nil || !strings.Contains(err.Error(), "lines_body_check") {
		t.Fatalf("32001 user insert err = %v, want lines_body_check", err)
	}

	// User line of 32,000 runes must succeed.
	var userLineID string
	if err := execAsApp(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
			VALUES ($1::uuid, 1, 'user', repeat('u', 32000), $2::uuid)
			RETURNING id::text`, opened.ID, ownerUser).Scan(&userLineID)
	}); err != nil || userLineID == "" {
		t.Fatalf("32000 user insert = %q err %v", userLineID, err)
	}

	// Assistant line of 64,001 runes must be refused by lines_body_check.
	if err := execAsApp(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO chat.lines (chat_id, seq, role, body, model, reply_to)
			VALUES ($1::uuid, 2, 'assistant', repeat('a', 64001), 'm1', $2::uuid)`, opened.ID, userLineID)
		return err
	}); err == nil || !strings.Contains(err.Error(), "lines_body_check") {
		t.Fatalf("64001 assistant insert err = %v, want lines_body_check", err)
	}

	// Assistant line of 64,000 runes must succeed.
	if err := execAsApp(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO chat.lines (chat_id, seq, role, body, model, reply_to)
			VALUES ($1::uuid, 2, 'assistant', repeat('a', 64000), 'm1', $2::uuid)`, opened.ID, userLineID)
		return err
	}); err != nil {
		t.Fatalf("64000 assistant insert err = %v", err)
	}
}

func insertUser(t *testing.T, pool *pgxpool.Pool, id, email, role string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3), now())
		ON CONFLICT (id) DO NOTHING`, id, email, role); err != nil {
		t.Fatal(err)
	}
}

func TestChatRenameUpdatesTitleAndSealsAuditWithoutContentLeak(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)

	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "First user question", app.Picks{}); err != nil {
		t.Fatal(err)
	}

	// The list shows the chat's derived title.
	derived, err := app.List(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if len(derived) != 1 || derived[0].ID != opened.ID || derived[0].Title != "First user question" {
		t.Fatalf("derived = %+v", derived)
	}

	renamed, err := app.Rename(ctx, store, ownerAccess, opened.ID, "Q4 Planning")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Q4 Planning" {
		t.Fatalf("rename = %+v", renamed)
	}
	listed, err := app.List(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Title != "Q4 Planning" {
		t.Fatalf("list = %+v", listed)
	}
	read, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil || read.Title != "Q4 Planning" {
		t.Fatalf("read = %+v %v", read, err)
	}

	// The 60-rune limit is the Postgres check constraint, not just the domain.
	if _, err := owner.Exec(ctx, `UPDATE chat.chats SET title = repeat('a', 61) WHERE id = $1::uuid`, opened.ID); err == nil || !strings.Contains(err.Error(), "chats_title_check") {
		t.Fatalf("61-rune title update = %v", err)
	}

	// A shared editor renames, and a viewer cannot.
	insertUser(t, owner, ownerUser, "rename-owner@example.com", "owner")
	insertUser(t, owner, otherUser, "rename-editor@example.com", "member")
	if _, err := owner.Exec(ctx, `INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by) VALUES ('chat', $1::uuid, $2::uuid, 'editor', $3::uuid)`, opened.ID, otherUser, ownerUser); err != nil {
		t.Fatal(err)
	}
	editor := app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + opened.ID + "}", SharedEdit: "{" + opened.ID + "}"}
	if _, err := app.Rename(ctx, store, editor, opened.ID, "Q4 Review"); err != nil {
		t.Fatal(err)
	}
	listed, err = app.List(ctx, store, ownerAccess)
	if err != nil || len(listed) != 1 || listed[0].Title != "Q4 Review" {
		t.Fatalf("editor rename = %+v %v", listed, err)
	}
	viewer := app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + opened.ID + "}"}
	if _, err := app.Rename(ctx, store, viewer, opened.ID, "nope"); !errors.Is(err, app.ErrSay) {
		t.Fatalf("viewer rename error = %v", err)
	}
}

func TestChatServerSearchMatchesTitleAndLineBodyUnderRLS(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := quietStore(t, owner)

	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model)
		VALUES ($1::uuid, 1, 'assistant', 'The release date is November 30.', 'm')`, opened.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Rename(ctx, store, ownerAccess, opened.ID, "Roadmap"); err != nil {
		t.Fatal(err)
	}

	// Chat B is another person's private chat, and it matches the query too.
	creator := app.Access{UserID: otherUser, Allow: "chat:create,chat:view,chat:edit"}
	other, err := app.Open(ctx, store, creator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body)
		VALUES ($1::uuid, 1, 'user', 'November 30 secret launch.')`, other.ID); err != nil {
		t.Fatal(err)
	}

	ids := func(summaries []app.ChatSummary) []string {
		got := make([]string, 0, len(summaries))
		for _, summary := range summaries {
			got = append(got, summary.ID)
		}
		return got
	}

	got, err := app.Search(ctx, store, ownerAccess, "Roadmap")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != opened.ID || got[0].Title != "Roadmap" {
		t.Fatalf("Roadmap = %v", ids(got))
	}

	got, err = app.Search(ctx, store, ownerAccess, "November")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != opened.ID {
		t.Fatalf("November = %v", ids(got))
	}

	got, err = app.Search(ctx, store, ownerAccess, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("nonexistent = %v", ids(got))
	}
}
