//go:build integration

package audit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/chat"
	chatapp "github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// chatRig migrates audit, identity and chat on a fresh database and returns
// the owner pool and the request pool that runs as upmo_app.
func chatRig(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

var chatAccess = chatapp.Access{
	UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
	Allow:  "chat:create,chat:view,chat:edit",
}

func TestChatWritesSealTheirActions(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)
	ctx := context.Background()

	opened, err := store.Open(ctx, chatAccess)
	if err != nil {
		t.Fatal(err)
	}
	account := uuid.Must(uuid.NewV7())
	agent := uuid.Must(uuid.NewV7())
	picks := chatapp.Picks{Route: "work", PinAccount: account.String(), PinModel: "model-x", AgentID: agent.String()}
	if err := store.Say(ctx, chatAccess, opened.ID, "the private question", picks); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 2 {
		t.Fatalf("%d events, want the chat create and the line", len(rows))
	}
	if rows[0].Action != audit.ChatCreate || rows[0].TargetID != opened.ID {
		t.Fatalf("first event = %s target %s, want the opened chat %s", rows[0].Action, rows[0].TargetID, opened.ID)
	}
	line := rows[1]
	if line.Action != audit.ChatMessage || line.TargetType != "line" || line.TargetID == "" {
		t.Fatalf("second event = %s %s %s, want the stored line", line.Action, line.TargetType, line.TargetID)
	}
	if containsSecret(line, "the private question") {
		t.Fatalf("message changes hold the line text: %s", changesJSON(t, line))
	}
	if got := changeOf(t, line, "route"); got.To != "work" {
		t.Fatalf("route = %v, want the picked route", got.To)
	}
	if got := changeOf(t, line, "model_provider"); got.To != account.String() {
		t.Fatalf("model_provider = %v, want the pinned account", got.To)
	}
	if got := changeOf(t, line, "model_id"); got.To != "model-x" {
		t.Fatalf("model_id = %v, want the pinned model", got.To)
	}
	if got := changeOf(t, line, "agent_id"); got.To != agent.String() {
		t.Fatalf("agent_id = %v, want the picked agent", got.To)
	}
}

func TestChatRetrySealsTheLineItAnswers(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	// A retry asks the queue again, so the store needs one to ask.
	store := chat.NewStore(appPool, stubEnqueuer{})
	store.ReplyRoute = "work"
	ctx := context.Background()

	opened, err := store.Open(ctx, chatAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Say(ctx, chatAccess, opened.ID, "ask once", chatapp.Picks{Route: "work"}); err != nil {
		t.Fatal(err)
	}
	sealed()

	var userLine, failedLine string
	if err := owner.QueryRow(ctx, `
		SELECT id::text FROM chat.lines
		WHERE chat_id = $1::uuid AND role = 'user'
		ORDER BY seq DESC LIMIT 1`, opened.ID).Scan(&userLine); err != nil {
		t.Fatal(err)
	}
	// The reply failed before any text, the way a dead job leaves the line.
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, status, reply_to, attempts)
		SELECT $1::uuid, COALESCE(MAX(seq), 0) + 1, 'assistant', '', '', 'failed', $2::uuid,
		       '[{"model": "", "class": "transient"}]'::jsonb
		FROM chat.lines WHERE chat_id = $1::uuid`, opened.ID, userLine); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE chat.runs SET state = 'failed', claim = NULL, history = NULL, finished_at = now()
		WHERE chat_id = $1::uuid`, opened.ID); err != nil {
		t.Fatal(err)
	}
	// The retry names the failed assistant line; the user line it answers is
	// the event's target.
	if err := owner.QueryRow(ctx, `
		SELECT id::text FROM chat.lines
		WHERE chat_id = $1::uuid AND role = 'assistant' AND status = 'failed'
		ORDER BY seq DESC LIMIT 1`, opened.ID).Scan(&failedLine); err != nil {
		t.Fatal(err)
	}

	if err := store.Retry(ctx, chatAccess, opened.ID, failedLine, nil); err != nil {
		t.Fatal(err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ChatRetry {
		t.Fatalf("events = %v, want one chat.retry", actions(rows))
	}
	row := rows[0]
	if row.TargetID != userLine {
		t.Fatalf("target = %s, want the retried user line %s", row.TargetID, userLine)
	}
	if got := changeOf(t, row, "route"); got.To != "work" {
		t.Fatalf("route = %v, want the stored picks' route", got.To)
	}
	if containsSecret(row, "ask once") {
		t.Fatalf("retry changes hold the line text: %s", changesJSON(t, row))
	}
}

func TestChatSweepSealsOnlyWhenItDeletes(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)
	ctx := context.Background()

	opened, err := store.Open(ctx, chatAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Say(ctx, chatAccess, opened.ID, "a line", chatapp.Picks{}); err != nil {
		t.Fatal(err)
	}
	sealed()
	stale := uuid.Must(uuid.NewV7())
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body, at)
		VALUES ($1::uuid, $2::uuid, 0, 'text', 'lost text', now() - interval '3 hours')`,
		opened.ID, stale); err != nil {
		t.Fatal(err)
	}

	swept, err := store.Sweep(ctx)
	if err != nil || swept != 1 {
		t.Fatalf("swept = %d %v, want the one stale delta", swept, err)
	}
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ChatSweep {
		t.Fatalf("events = %v, want one chat.sweep", actions(rows))
	}
	row := rows[0]
	// The sweep runs on the service's schedule, so its writer seeds the
	// system actor with the service credential when no request scope exists.
	if row.ActorType != "system" || row.CredentialType != "service" {
		t.Fatalf("actor = %s %s, want system service", row.ActorType, row.CredentialType)
	}
	if got := changeOf(t, row, "rows"); got.To != float64(1) {
		t.Fatalf("rows = %v, want the deleted delta", got.To)
	}
	if containsSecret(row, "lost text") {
		t.Fatalf("sweep changes hold the delta text: %s", changesJSON(t, row))
	}

	if swept, err := store.Sweep(ctx); err != nil || swept != 0 {
		t.Fatalf("second sweep = %d %v, want nothing left", swept, err)
	}
	if rows := sealed(); len(rows) != 0 {
		t.Fatalf("a sweep that deleted nothing sealed %v", actions(rows))
	}
}

func TestChatRouteSealsWithThePersonActor(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	mux := http.NewServeMux()
	chat.Handler{
		Book: store,
		Access: func(r *http.Request) (chatapp.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: chatAccess.UserID, CredentialType: "session"})
			return chatAccess, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ChatCreate {
		t.Fatalf("events = %v, want one chat.create", actions(rows))
	}
	row := rows[0]
	// The adapter recorded the event, so the middleware left no fallback.
	if row.Reason != "" {
		t.Fatalf("reason = %q, want the handler's own event", row.Reason)
	}
	if row.ActorType != "user" || row.ActorID != chatAccess.UserID || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.CredentialType)
	}
}

func TestChatMessageRouteSealsItsOwnEvent(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	mux := http.NewServeMux()
	chat.Handler{
		Book: store,
		Access: func(r *http.Request) (chatapp.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: chatAccess.UserID, CredentialType: "session"})
			return chatAccess, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", rec.Code, rec.Body.String())
	}
	var opened struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil || opened.ID == "" {
		t.Fatalf("open body = %s err %v", rec.Body.String(), err)
	}

	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats/"+opened.ID, strings.NewReader(`{"body":"the confidential question"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("say = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 2 || rows[0].Action != audit.ChatCreate || rows[1].Action != audit.ChatMessage {
		t.Fatalf("events = %v, want the chat create and the line's chat.message", actions(rows))
	}
	line := rows[1]
	// The store sealed the event in its own transaction, so the middleware had
	// nothing to fall back on: the reason is empty, not unrecorded.
	if line.Reason != "" {
		t.Fatalf("reason = %q, want the store's own event, not the unrecorded fallback", line.Reason)
	}
	if line.ActorType != "user" || line.ActorID != chatAccess.UserID || line.CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", line.ActorType, line.ActorID, line.CredentialType)
	}
	if containsSecret(line, "the confidential question") {
		t.Fatalf("message changes hold the line text: %s", changesJSON(t, line))
	}
}

func TestChatRouteRefusalRecordsDenied(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	stranger := chatapp.Access{UserID: "018f3b2c-7a10-7c4a-8e11-444444444444"}
	mux := http.NewServeMux()
	chat.Handler{
		Book: store,
		Access: func(r *http.Request) (chatapp.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: stranger.UserID, CredentialType: "session"})
			// Signed in, but no chat permission: the handler refuses.
			return stranger, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a person without chat permission = %d %s, want 403", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ChatCreate {
		t.Fatalf("events = %v, want one denied chat.create", actions(rows))
	}
	if rows[0].Outcome != audit.Denied || rows[0].ActorID != stranger.UserID {
		t.Fatalf("event = %s %s %s, want the refused person denied", rows[0].Action, rows[0].Outcome, rows[0].ActorID)
	}
}

func TestChatRenameUpdatesTitleAndSealsAuditWithoutContentLeak(t *testing.T) {
	owner, appPool := chatRig(t)
	sealed := newSealer(t, owner)
	store := chat.NewStore(appPool, nil)

	mux := http.NewServeMux()
	chat.Handler{
		Book: store,
		Access: func(r *http.Request) (chatapp.Access, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: chatAccess.UserID, CredentialType: "session"})
			return chatAccess, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("open = %d %s", rec.Code, rec.Body.String())
	}
	var opened struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil || opened.ID == "" {
		t.Fatalf("open body = %s err %v", rec.Body.String(), err)
	}

	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats/"+opened.ID, strings.NewReader(`{"body":"First user question"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("say = %d %s", rec.Code, rec.Body.String())
	}

	var listed []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listed) != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	if len(listed) != 1 || listed[0].Title != "First user question" {
		t.Fatalf("list = %+v", listed)
	}

	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/chats/"+opened.ID, strings.NewReader(`{"title":"Q4 Planning"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}
	var renamed struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &renamed); err != nil || renamed.Title != "Q4 Planning" {
		t.Fatalf("rename body = %s err %v", rec.Body.String(), err)
	}

	var read struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats/"+opened.ID, nil))
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &read) != nil || read.Title != "Q4 Planning" {
		t.Fatalf("read = %d %s", rec.Code, rec.Body.String())
	}
	listed = nil
	rec = httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].Title != "Q4 Planning" {
		t.Fatalf("list = %d %s err %v", rec.Code, rec.Body.String(), err)
	}

	// The rename seals one chat.update event that holds the field name alone.
	rows := sealed()
	var update *audit.Row
	for i := range rows {
		if rows[i].Action == audit.ChatUpdate {
			update = &rows[i]
		}
	}
	if update == nil {
		t.Fatalf("events = %v, want one chat.update", actions(rows))
	}
	if strings.Join(update.Changes.Fields, ",") != "title" {
		t.Fatalf("fields = %v, want the title field", update.Changes.Fields)
	}
	if hasAnyValue(*update) {
		t.Fatalf("changes hold a value: %s", changesJSON(t, *update))
	}
	if containsSecret(*update, "Q4 Planning") {
		t.Fatalf("changes hold the title text: %s", changesJSON(t, *update))
	}
}

// stubEnqueuer takes every job the chat store asks for and keeps none: the
// retry only needs a queue to hand the reply to.
type stubEnqueuer struct{}

func (stubEnqueuer) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{}, nil
}
