//go:build integration

package brain

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

const signInAgainBody = "{\"error\":\"sign in again\"}\n"

var (
	stepUpNow  = time.Date(2026, 11, 25, 0, 11, 0, 0, time.UTC)
	stepUpTen  = time.Date(2026, 11, 25, 0, 10, 0, 0, time.UTC)
	stepUpPast = time.Date(2026, 11, 24, 0, 11, 0, 0, time.UTC)
)

// stepUpEnv is the brain handler on a pinned clock with Ada, an owner who has
// one passkey, signed in through a real session row.
type stepUpEnv struct {
	owner   *pgxpool.Pool
	handler http.Handler
	ctx     context.Context
	ada     string
	cookie  string
}

func newStepUpEnv(t *testing.T) *stepUpEnv {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	withWorkerAt(t, owner, &cfg)
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	// The resumed reply of an approved hold is queued, never run.
	workers := river.NewWorkers()
	river.AddWorker(workers, parkedReply{})
	work, err := jobs.Open(pool, jobs.WithWorkers(workers))
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return stepUpNow }
	handler, err := routesAt(now, pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ada string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&ada); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.passkeys (
		  user_id, credential_id, public_key, sign_count, aaguid, transports,
		  backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1::uuid, '\x01', '\x01', 0, '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', 'Passkey', now())`, ada); err != nil {
		t.Fatal(err)
	}
	uid, err := identity.ParseUserID(ada)
	if err != nil {
		t.Fatal(err)
	}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	sessions.Now = now
	cookie, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	return &stepUpEnv{owner: owner, handler: handler, ctx: ctx, ada: ada, cookie: cookie}
}

type parkedReply struct {
	river.WorkerDefaults[chat.ReplyJobArgs]
}

func (parkedReply) Work(context.Context, *river.Job[chat.ReplyJobArgs]) error { return nil }

// as sets how and when Ada's session last signed in.
func (e *stepUpEnv) as(t *testing.T, method string, signedIn time.Time) {
	t.Helper()
	if _, err := e.owner.Exec(e.ctx, `UPDATE identity.sessions SET sign_in_method = $2, signed_in_at = $3 WHERE user_id = $1::uuid`, e.ada, method, signedIn); err != nil {
		t.Fatal(err)
	}
}

func (e *stepUpEnv) call(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "__Host-upmo_session", Value: e.cookie})
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *stepUpEnv) text(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	if err := e.owner.QueryRow(e.ctx, query, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

func refusedStepUp(t *testing.T, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || rec.Body.String() != signInAgainBody {
		t.Fatalf("%s = %d %q, want 403 %q", label, rec.Code, rec.Body.String(), signInAgainBody)
	}
}

// task seeds one of Ada's tasks, Working or Needs approval, with the given
// budget and spend.
func (e *stepUpEnv) task(t *testing.T, goal, status string, budget, spent int64, paused bool) string {
	t.Helper()
	tx, err := e.owner.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(e.ctx) }()
	var id string
	if err := tx.QueryRow(e.ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, budget, spent, paused, allow)
		VALUES ($1::uuid, 'private', $2, 'Working', $3, $4, $5, 'task:create,task:view,task:edit')
		RETURNING id::text`, e.ada, goal, budget, spent, paused).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(e.ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor)
		VALUES ($1::uuid, 1, '', 'Working', 'open', 'person')`, id); err != nil {
		t.Fatal(err)
	}
	if status == "Needs approval" {
		if _, err := tx.Exec(e.ctx, `
			INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input, actor)
			VALUES ($1::uuid, 2, 'Working', 'Needs approval', 'pause', 'person')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(e.ctx, `UPDATE tasks.tasks SET status = 'Needs approval' WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStepUpTaskFund(t *testing.T) {
	e := newStepUpEnv(t)
	id := e.task(t, "Fund me", "Working", 2_000_000, 2_000_000, true)
	state := func() string {
		return e.text(t, `SELECT budget::text || ' ' || paused::text FROM tasks.tasks WHERE id = $1::uuid`, id)
	}

	e.as(t, "passkey", stepUpPast)
	refusedStepUp(t, "stale fund", e.call(http.MethodPost, "/api/tasks/"+id, `{"kind":"fund","amount":3000000}`))
	if got := state(); got != "2000000 true" {
		t.Fatalf("after stale fund = %s, want 2000000 true", got)
	}

	e.as(t, "password", stepUpTen)
	refusedStepUp(t, "password fund", e.call(http.MethodPost, "/api/tasks/"+id, `{"kind":"fund","amount":3000000}`))
	if got := state(); got != "2000000 true" {
		t.Fatalf("after password fund = %s, want 2000000 true", got)
	}

	e.as(t, "passkey", stepUpTen)
	if rec := e.call(http.MethodPost, "/api/tasks/"+id, `{"kind":"fund","amount":3000000}`); rec.Code != http.StatusOK {
		t.Fatalf("fresh fund = %d %s", rec.Code, rec.Body.String())
	}
	if got := state(); got != "3000000 false" {
		t.Fatalf("after fresh fund = %s, want 3000000 false", got)
	}

	// Other steps stay on the session limits alone.
	e.as(t, "passkey", stepUpPast)
	if rec := e.call(http.MethodPost, "/api/tasks/"+id, `{"kind":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("stale non-fund step = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestStepUpTaskShare(t *testing.T) {
	e := newStepUpEnv(t)
	id := e.task(t, "Share me", "Working", 0, 0, false)
	body := `{"visibility":"everyone","members":[{"subject_type":"person","subject_id":"` + e.ada + `","role":"owner"}]}`

	e.as(t, "passkey", stepUpPast)
	refusedStepUp(t, "stale task share", e.call(http.MethodPut, "/api/tasks/"+id+"/share", body))
	if got := e.text(t, `SELECT visibility FROM tasks.tasks WHERE id = $1::uuid`, id); got != "private" {
		t.Fatalf("visibility after stale share = %s", got)
	}

	e.as(t, "passkey", stepUpTen)
	if rec := e.call(http.MethodPut, "/api/tasks/"+id+"/share", body); rec.Code != http.StatusOK {
		t.Fatalf("fresh task share = %d %s", rec.Code, rec.Body.String())
	}
	if got := e.text(t, `SELECT visibility FROM tasks.tasks WHERE id = $1::uuid`, id); got != "everyone" {
		t.Fatalf("visibility after fresh share = %s", got)
	}
}

func TestStepUpApprovalApprove(t *testing.T) {
	e := newStepUpEnv(t)
	id := e.task(t, "Approve me", "Needs approval", 0, 0, false)
	var approval string
	if err := e.owner.QueryRow(e.ctx, `
		INSERT INTO tasks.approvals (task_id, user_id, tool, reason, action)
		VALUES ($1::uuid, $2::uuid, 'mail.send', 'ask', 'Send the receipts')
		RETURNING id::text`, id, e.ada).Scan(&approval); err != nil {
		t.Fatal(err)
	}
	state := func() string {
		return e.text(t, `SELECT state FROM tasks.approvals WHERE id = $1::uuid`, approval)
	}

	e.as(t, "passkey", stepUpPast)
	refusedStepUp(t, "stale approve", e.call(http.MethodPost, "/api/approvals/"+approval+"/approve", `{}`))
	if got := state(); got != "pending" {
		t.Fatalf("approval after stale approve = %s", got)
	}

	e.as(t, "passkey", stepUpTen)
	if rec := e.call(http.MethodPost, "/api/approvals/"+approval+"/approve", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("fresh approve = %d %s", rec.Code, rec.Body.String())
	}
	if got := state(); got != "approved" {
		t.Fatalf("approval after fresh approve = %s", got)
	}
}

func TestStepUpTaskEditor(t *testing.T) {
	e := newStepUpEnv(t)
	id := e.task(t, "Edit me", "Working", 0, 0, false)

	e.as(t, "passkey", stepUpPast)
	stale := e.call(http.MethodPost, "/api/tasks/"+id+"/editor", "")
	refusedStepUp(t, "stale editor", stale)
	if strings.Contains(stale.Body.String(), "url") {
		t.Fatalf("stale editor body has a URL: %s", stale.Body.String())
	}

	// No sandbox is configured, so the fresh open passes the gate and is then
	// refused by the editor itself.
	e.as(t, "passkey", stepUpTen)
	if rec := e.call(http.MethodPost, "/api/tasks/"+id+"/editor", ""); rec.Code == http.StatusForbidden || rec.Body.String() == signInAgainBody {
		t.Fatalf("fresh editor = %d %s, want past the step-up gate", rec.Code, rec.Body.String())
	}
}

func TestStepUpChatShare(t *testing.T) {
	e := newStepUpEnv(t)
	var chatID string
	if err := e.owner.QueryRow(e.ctx, `INSERT INTO chat.chats (owner_user_id, visibility) VALUES ($1::uuid, 'private') RETURNING id::text`, e.ada).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	body := `{"visibility":"everyone","members":[{"subject_type":"person","subject_id":"` + e.ada + `","role":"owner"}]}`

	e.as(t, "passkey", stepUpPast)
	refusedStepUp(t, "stale chat share", e.call(http.MethodPut, "/api/chats/"+chatID+"/share", body))
	if got := e.text(t, `SELECT visibility FROM chat.chats WHERE id = $1::uuid`, chatID); got != "private" {
		t.Fatalf("visibility after stale share = %s", got)
	}

	e.as(t, "passkey", stepUpTen)
	if rec := e.call(http.MethodPut, "/api/chats/"+chatID+"/share", body); rec.Code != http.StatusOK {
		t.Fatalf("fresh chat share = %d %s", rec.Code, rec.Body.String())
	}
	if got := e.text(t, `SELECT visibility FROM chat.chats WHERE id = $1::uuid`, chatID); got != "everyone" {
		t.Fatalf("visibility after fresh share = %s", got)
	}
}

func TestStepUpChatHoldApprove(t *testing.T) {
	e := newStepUpEnv(t)
	var chatID, lineID, runID, holdID string
	if err := e.owner.QueryRow(e.ctx, `INSERT INTO chat.chats (owner_user_id, visibility, tainted) VALUES ($1::uuid, 'private', true) RETURNING id::text`, e.ada).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(e.ctx, `INSERT INTO chat.lines (chat_id, seq, body, author_id) VALUES ($1::uuid, 1, 'open a task', $2::uuid) RETURNING id::text`, chatID, e.ada).Scan(&lineID); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(e.ctx, `
		INSERT INTO chat.runs (id, chat_id, line_id, author_id, state, history)
		VALUES (uuidv7(), $1::uuid, $2::uuid, $3::uuid, 'waiting', '[]')
		RETURNING id::text`, chatID, lineID, e.ada).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(e.ctx, `
		INSERT INTO chat.holds (chat_id, line_id, user_id, reason, action, calls)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'ask', 'Open a task',
		        ARRAY['{"call_id":"c1","tool_name":"open_task","op":"change","action":"Open a task","preview":"","untrusted_origin":false}'::jsonb])
		RETURNING id::text`, chatID, runID, e.ada).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	path := "/api/chats/" + chatID + "/holds/" + holdID + "/approve"
	state := func() string { return e.text(t, `SELECT state FROM chat.holds WHERE id = $1::uuid`, holdID) }

	e.as(t, "passkey", stepUpPast)
	refusedStepUp(t, "stale hold approve", e.call(http.MethodPost, path, `{"allow_for_chat":true}`))
	if got := state(); got != "pending" {
		t.Fatalf("hold after stale approve = %s", got)
	}

	e.as(t, "password", stepUpTen)
	refusedStepUp(t, "password hold approve", e.call(http.MethodPost, path, `{"allow_for_chat":true}`))
	if got := state(); got != "pending" {
		t.Fatalf("hold after password approve = %s", got)
	}

	// A fresh step-up approves the hold, and the tainted chat still stores no allowance.
	e.as(t, "passkey", stepUpTen)
	if rec := e.call(http.MethodPost, path, `{"allow_for_chat":true}`); rec.Code != http.StatusAccepted {
		t.Fatalf("fresh hold approve = %d %s", rec.Code, rec.Body.String())
	}
	if got := state(); got != "approved" {
		t.Fatalf("hold after fresh approve = %s", got)
	}
	if got := e.text(t, `SELECT count(*)::text FROM chat.tool_allowances WHERE chat_id = $1::uuid`, chatID); got != "0" {
		t.Fatalf("tainted chat allowances = %s, want 0", got)
	}
}

func TestStepUpProviderCredentials(t *testing.T) {
	e := newStepUpEnv(t)
	create := `{"provider":"openai","name":"Ada key","adapter":"openai_compat","plan_kind":"metered",
		"base_url":"https://api.example.com","api_key":"sk-ada-aaaa-aaaa-aaaa-aaaa","models":["m1"]}`
	credentials := func() string { return e.text(t, `SELECT count(*)::text FROM gateway.credentials`) }

	e.as(t, "passkey", stepUpPast)
	refusedStepUp(t, "stale provider create", e.call(http.MethodPost, "/api/providers", create))
	if got := credentials(); got != "0" {
		t.Fatalf("credentials after stale create = %s, want 0", got)
	}

	e.as(t, "passkey", stepUpTen)
	made := e.call(http.MethodPost, "/api/providers", create)
	if made.Code != http.StatusCreated {
		t.Fatalf("fresh provider create = %d %s", made.Code, made.Body.String())
	}
	if got := credentials(); got != "1" {
		t.Fatalf("credentials after fresh create = %s, want 1", got)
	}
	var account struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(made.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	name := func() string {
		return e.text(t, `SELECT coalesce((SELECT name FROM gateway.provider_accounts WHERE id = $1::uuid), '')`, account.ID)
	}

	e.as(t, "passkey", stepUpPast)
	refusedStepUp(t, "stale provider change", e.call(http.MethodPut, "/api/providers/"+account.ID, `{"name":"Renamed"}`))
	if got := name(); got != "Ada key" {
		t.Fatalf("name after stale change = %q", got)
	}
	refusedStepUp(t, "stale provider delete", e.call(http.MethodDelete, "/api/providers/"+account.ID, ""))
	if got := name(); got != "Ada key" {
		t.Fatalf("account after stale delete = %q", got)
	}

	e.as(t, "passkey", stepUpTen)
	if rec := e.call(http.MethodPut, "/api/providers/"+account.ID, `{"name":"Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("fresh provider change = %d %s", rec.Code, rec.Body.String())
	}
	if got := name(); got != "Renamed" {
		t.Fatalf("name after fresh change = %q", got)
	}
	if rec := e.call(http.MethodDelete, "/api/providers/"+account.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("fresh provider delete = %d %s", rec.Code, rec.Body.String())
	}
	if got := name(); got != "" {
		t.Fatalf("account after fresh delete = %q, want none", got)
	}
}

func TestIdentityServiceFreshUsesStepUp(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	now := func() time.Time { return stepUpNow }
	sessions := identity.Sessions(pool, identity.IdleTimeout, identity.MaxAge)
	sessions.Now = now
	emails := sealer.WorkerEmail(t, owner)
	passkeys, err := identity.Passkeys(pool, emails, sessions, "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = now
	adapter := identityAdapter{id: identity.New(sessions, passkeys, identity.Passwords(pool, emails, sessions))}
	var ada string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&ada); err != nil {
		t.Fatal(err)
	}
	if !adapter.Fresh(ctx, Principal{UserID: ada, Method: "password", SignedIn: stepUpTen}) {
		t.Fatal("fresh password without a passkey = false")
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.passkeys (
		  user_id, credential_id, public_key, sign_count, aaguid, transports,
		  backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1::uuid, '\x01', '\x01', 0, '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', 'Passkey', now())`, ada); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		p    Principal
		want bool
	}{
		{"password on a passkey account", Principal{UserID: ada, Method: "password", SignedIn: stepUpTen}, false},
		{"fresh passkey", Principal{UserID: ada, Method: "passkey", SignedIn: stepUpTen}, true},
		{"passkey 11 minutes ago", Principal{UserID: ada, Method: "passkey", SignedIn: stepUpNow.Add(-11 * time.Minute)}, false},
		{"no user", Principal{Method: "passkey", SignedIn: stepUpTen}, false},
	}
	for _, c := range cases {
		if got := adapter.Fresh(ctx, c.p); got != c.want {
			t.Errorf("%s: Fresh = %v, want %v", c.name, got, c.want)
		}
	}
}
