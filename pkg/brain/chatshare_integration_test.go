//go:build integration

package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

type chatShareWire struct {
	Visibility  string            `json:"visibility"`
	TeamID      string            `json:"team_id,omitempty"`
	PrivateRead bool              `json:"private_read"`
	Members     []shareMemberWire `json:"members"`
}

type chatShareEnv struct {
	owner   *pgxpool.Pool
	pool    *pgxpool.Pool
	handler http.Handler
	members identity.MemberService
	people  map[string]string
	teams   map[string]string
}

type noopEnqueuer struct{}

func (noopEnqueuer) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{}, nil
}

func chatShareSetup(t *testing.T, withReplies bool) *chatShareEnv {
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
	pool := pgtest.App(t, owner)
	members := identity.Members(pool, nil, "", nil, nil)
	var store chat.Store
	if withReplies {
		store = chat.NewStore(pool, noopEnqueuer{})
		store.ReplyRoute = "chat"
	} else {
		store = chat.NewStore(pool, nil)
	}
	store.ReplaceChatGrants = chatGrants{members: members}.ReplaceChatGrantsTx
	mux := http.NewServeMux()
	chat.Handler{
		Book:   store,
		Shares: store,
		Grants: chatGrants{members: members},
		Access: func(r *http.Request) (chat.Access, bool) {
			person, err := identity.ParseUserID(r.Header.Get("X-As"))
			if err != nil {
				return chat.Access{}, false
			}
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: person.String(), CredentialType: "session"})
			access, ok, err := chatAccess(r.Context(), members, person)
			if err != nil || !ok {
				return chat.Access{}, false
			}
			return access, true
		},
	}.Mount(mux, nil)

	env := &chatShareEnv{owner: owner, pool: pool, handler: mux, members: members, people: map[string]string{}, teams: map[string]string{}}
	env.person(t, "Ada", "owner", false)
	for _, name := range []string{"Bob", "Cy", "Dee", "Eve", "Gus"} {
		env.person(t, name, "member", false)
	}
	env.person(t, "Ex", "member", true)
	env.team(t, "Design", "ada", "eve")
	env.team(t, "Build", "ada", "gus")
	env.team(t, "Ops")
	return env
}

func (e *chatShareEnv) person(t *testing.T, name, role string, disabled bool) {
	t.Helper()
	var id string
	err := e.owner.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at, disabled_at)
		SELECT lower($1::text) || '@example.com', sha256(convert_to(lower($1::text) || '@example.com', 'UTF8')), 'k1', $1::text, role.id, now(),
		       CASE WHEN $3::boolean THEN now() END
		FROM identity.roles AS role
		WHERE role.key = $2::text
		RETURNING id::text`, name, role, disabled).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	e.people[strings.ToLower(name)] = id
}

func (e *chatShareEnv) team(t *testing.T, name string, members ...string) {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := e.owner.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.teams[strings.ToLower(name)] = id
	for _, member := range members {
		if _, err := e.owner.Exec(ctx, `
			INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, id, e.people[member]); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *chatShareEnv) call(as, method, path, body string) (int, []byte) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if as != "" {
		req.Header.Set("X-As", e.people[as])
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func chatShareURL(chatID string) string { return "/api/chats/" + chatID + "/share" }

func (e *chatShareEnv) entry(kind, name, role string) shareMemberWire {
	id := e.people[name]
	if kind == "team" {
		id = e.teams[name]
	}
	return shareMemberWire{SubjectType: kind, SubjectID: id, Role: role}
}

func (e *chatShareEnv) put(t *testing.T, as, chatID string, share chatShareWire) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(share)
	if err != nil {
		t.Fatal(err)
	}
	return e.call(as, http.MethodPut, chatShareURL(chatID), string(body))
}

func (e *chatShareEnv) mustPut(t *testing.T, as, chatID string, share chatShareWire) chatShareWire {
	t.Helper()
	code, body := e.put(t, as, chatID, share)
	if code != http.StatusOK {
		t.Fatalf("put as %s = %d %s", as, code, body)
	}
	return shareDecode[chatShareWire](t, body)
}

func (e *chatShareEnv) openChat(t *testing.T, as string) string {
	t.Helper()
	code, body := e.call(as, http.MethodPost, "/api/chats", "")
	if code != http.StatusCreated {
		t.Fatalf("open chat = %d %s", code, body)
	}
	return shareDecode[map[string]string](t, body)["id"]
}

func TestChatShareReadIsForTheOwnerAndPeopleWhoCanEdit(t *testing.T) {
	e := chatShareSetup(t, false)
	chatID := e.openChat(t, "ada")
	e.mustPut(t, "ada", chatID, chatShareWire{Visibility: "private", Members: []shareMemberWire{
		e.entry("person", "ada", "owner"),
		e.entry("person", "bob", "editor"),
		e.entry("person", "cy", "viewer"),
		e.entry("team", "design", "viewer"),
	}})

	code, body := e.call("ada", http.MethodGet, chatShareURL(chatID), "")
	if code != http.StatusOK {
		t.Fatalf("owner get = %d %s", code, body)
	}
	if keys := shareDecode[map[string]json.RawMessage](t, body); keys["team_id"] != nil {
		t.Fatalf("private share names a team: %s", body)
	}
	got := shareDecode[chatShareWire](t, body)
	if got.Visibility != "private" || got.TeamID != "" || got.PrivateRead || len(got.Members) != 4 {
		t.Fatalf("share = %+v", got)
	}
	if want := (shareMemberWire{SubjectType: "person", SubjectID: e.people["ada"], Name: "Ada", Role: "owner"}); got.Members[0] != want {
		t.Fatalf("first member = %+v, want %+v", got.Members[0], want)
	}
	for _, want := range []shareMemberWire{
		{SubjectType: "person", SubjectID: e.people["bob"], Name: "Bob", Role: "editor"},
		{SubjectType: "person", SubjectID: e.people["cy"], Name: "Cy", Role: "viewer"},
		{SubjectType: "team", SubjectID: e.teams["design"], Name: "Design", Role: "viewer"},
	} {
		if !shareHas(got.Members, want) {
			t.Errorf("share lacks %+v: %+v", want, got.Members)
		}
	}

	if code, body := e.call("bob", http.MethodGet, chatShareURL(chatID), ""); code != http.StatusOK {
		t.Fatalf("editor get = %d %s", code, body)
	}
	if code, body := e.call("cy", http.MethodGet, chatShareURL(chatID), ""); code != http.StatusForbidden {
		t.Fatalf("viewer get = %d %s, want 403", code, body)
	}
	if code, body := e.call("dee", http.MethodGet, chatShareURL(chatID), ""); code != http.StatusNotFound {
		t.Fatalf("stranger get = %d %s, want 404", code, body)
	}
	if code, body := e.call("", http.MethodGet, chatShareURL(chatID), ""); code != http.StatusUnauthorized {
		t.Fatalf("no session get = %d %s, want 401", code, body)
	}
	req := httptest.NewRequest(http.MethodGet, chatShareURL(chatID), nil)
	req.Header.Set("X-As", e.people["ada"])
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bearer get = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

func TestChatSharePutReplacesGrantsAuditsAndEnforcesOwnerOnly(t *testing.T) {
	e := chatShareSetup(t, false)
	ctx := context.Background()
	chatID := e.openChat(t, "ada")
	if code, body := e.call("ada", http.MethodPost, "/api/chats/"+chatID, `{"body":"Ada starts"}`); code != http.StatusAccepted {
		t.Fatalf("ada say = %d %s", code, body)
	}

	got := e.mustPut(t, "ada", chatID, chatShareWire{
		Visibility: "team",
		TeamID:     e.teams["design"],
		Members: []shareMemberWire{
			e.entry("person", "ada", "owner"),
			e.entry("person", "bob", "editor"),
			e.entry("person", "cy", "viewer"),
			e.entry("team", "build", "viewer"),
		},
	})
	if got.Visibility != "team" || got.TeamID != e.teams["design"] || len(got.Members) != 4 {
		t.Fatalf("put share = %+v", got)
	}

	// Verify audit.pending recorded chat.share with grants and visibility changes.
	var action, targetID string
	var changesRaw []byte
	if err := e.owner.QueryRow(ctx, `
		SELECT action, target_id, changes
		FROM audit.pending
		WHERE action = $1 AND target_id = $2
		ORDER BY id DESC LIMIT 1`, string(audit.ChatShare), chatID).Scan(&action, &targetID, &changesRaw); err != nil {
		t.Fatalf("audit chat.share query: %v", err)
	}
	if action != string(audit.ChatShare) || targetID != chatID || !strings.Contains(string(changesRaw), `"grants"`) || !strings.Contains(string(changesRaw), `"visibility"`) {
		t.Fatalf("audit row = %s %s %s", action, targetID, string(changesRaw))
	}

	// Bob (editor) can read, post, and read share, but cannot PUT share.
	if code, body := e.call("bob", http.MethodGet, "/api/chats/"+chatID, ""); code != http.StatusOK {
		t.Fatalf("editor read = %d %s", code, body)
	}
	if code, body := e.call("bob", http.MethodPost, "/api/chats/"+chatID, `{"body":"Bob follows up"}`); code != http.StatusAccepted {
		t.Fatalf("editor say = %d %s", code, body)
	}
	if code, body := e.put(t, "bob", chatID, chatShareWire{
		Visibility: "team",
		TeamID:     e.teams["design"],
		Members: []shareMemberWire{
			e.entry("person", "ada", "owner"),
			e.entry("person", "bob", "editor"),
			e.entry("person", "dee", "editor"),
		},
	}); code != http.StatusForbidden {
		t.Fatalf("editor same-visibility grant put = %d %s, want 403", code, body)
	}
	if code, body := e.put(t, "bob", chatID, chatShareWire{
		Visibility: "everyone",
		Members:    []shareMemberWire{e.entry("person", "ada", "owner")},
	}); code != http.StatusForbidden {
		t.Fatalf("editor put = %d %s, want 403", code, body)
	}

	// Cy (viewer) can read, but cannot post or PUT share.
	if code, body := e.call("cy", http.MethodGet, "/api/chats/"+chatID, ""); code != http.StatusOK {
		t.Fatalf("viewer read = %d %s", code, body)
	}
	if code, body := e.call("cy", http.MethodPost, "/api/chats/"+chatID, `{"body":"Cy cannot write"}`); code != http.StatusForbidden {
		t.Fatalf("viewer say = %d %s, want 403", code, body)
	}
	if code, body := e.put(t, "cy", chatID, chatShareWire{
		Visibility: "everyone",
		Members:    []shareMemberWire{e.entry("person", "ada", "owner")},
	}); code != http.StatusForbidden {
		t.Fatalf("viewer put = %d %s, want 403", code, body)
	}

	// Stranger gets 404 on PUT share.
	if code, body := e.put(t, "dee", chatID, chatShareWire{
		Visibility: "everyone",
		Members:    []shareMemberWire{e.entry("person", "ada", "owner")},
	}); code != http.StatusNotFound {
		t.Fatalf("stranger put = %d %s, want 404", code, body)
	}

	// Direct SQL UPDATE chat.chats SET visibility = 'everyone' as editor under upmo_app is blocked by chat.keep_chat().
	bobID, err := identity.ParseUserID(e.people["bob"])
	if err != nil {
		t.Fatal(err)
	}
	bobAccess, ok, err := chatAccess(ctx, e.members, bobID)
	if err != nil || !ok {
		t.Fatalf("bob chatAccess = %v %v", ok, err)
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true),
		       set_config('upmo.shared_view', $4, true),
		       set_config('upmo.shared_edit', $5, true)`,
		bobAccess.UserID, bobAccess.Allow, bobAccess.Teams, bobAccess.SharedView, bobAccess.SharedEdit); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `UPDATE chat.chats SET visibility = 'everyone', visibility_team_id = NULL WHERE id = $1::uuid`, chatID)
	_ = tx.Rollback(ctx)
	if err == nil || !strings.Contains(err.Error(), "chat: owner") {
		t.Fatalf("editor direct visibility update = %v, want chat: owner", err)
	}

	// Verify GET /api/chats/{id} returns author_id, author_name, and author_avatar on user lines and nulls on assistant lines.
	code, body := e.call("bob", http.MethodGet, "/api/chats/"+chatID, "")
	if code != http.StatusOK {
		t.Fatalf("get chat = %d %s", code, body)
	}
	var thread struct {
		Lines []struct {
			Role         string  `json:"role"`
			Body         string  `json:"body"`
			AuthorID     *string `json:"author_id"`
			AuthorName   *string `json:"author_name"`
			AuthorAvatar *string `json:"author_avatar"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(body, &thread); err != nil {
		t.Fatal(err)
	}
	if len(thread.Lines) < 3 {
		t.Fatalf("lines = %+v", thread.Lines)
	}
	if thread.Lines[0].Role != "user" || thread.Lines[0].AuthorID == nil || *thread.Lines[0].AuthorID != e.people["ada"] || thread.Lines[0].AuthorName == nil || *thread.Lines[0].AuthorName != "Ada" || thread.Lines[0].AuthorAvatar != nil {
		t.Fatalf("line 0 = %+v", thread.Lines[0])
	}
	if thread.Lines[1].Role != "assistant" || thread.Lines[1].AuthorID != nil || thread.Lines[1].AuthorName != nil || thread.Lines[1].AuthorAvatar != nil {
		t.Fatalf("line 1 = %+v", thread.Lines[1])
	}
	if thread.Lines[2].Role != "user" || thread.Lines[2].AuthorID == nil || *thread.Lines[2].AuthorID != e.people["bob"] || thread.Lines[2].AuthorName == nil || *thread.Lines[2].AuthorName != "Bob" || thread.Lines[2].AuthorAvatar != nil {
		t.Fatalf("line 2 = %+v", thread.Lines[2])
	}
}

func TestChatShareReturns409WhileRunningOrWaitingAndSucceedsAfterStop(t *testing.T) {
	e := chatShareSetup(t, true)
	ctx := context.Background()
	chatID := e.openChat(t, "ada")
	if code, body := e.call("ada", http.MethodPost, "/api/chats/"+chatID, `{"body":"still running"}`); code != http.StatusAccepted {
		t.Fatalf("say = %d %s", code, body)
	}

	update := chatShareWire{
		Visibility: "everyone",
		Members: []shareMemberWire{
			e.entry("person", "ada", "owner"),
			e.entry("person", "bob", "editor"),
		},
	}

	// While run is 'running': 409 Conflict, visibility stays private, 0 share rows.
	code, body := e.put(t, "ada", chatID, update)
	if code != http.StatusConflict {
		t.Fatalf("running put = %d %s, want 409", code, body)
	}
	if got := shareDecode[map[string]string](t, body)["error"]; got != "a reply is already on its way" {
		t.Fatalf("409 error = %q", got)
	}
	var vis string
	var shareCount int
	if err := e.owner.QueryRow(ctx, `SELECT visibility FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&vis); err != nil {
		t.Fatal(err)
	}
	if err := e.owner.QueryRow(ctx, `SELECT count(*) FROM identity.shares WHERE object_type = 'chat' AND object_id = $1::uuid`, chatID).Scan(&shareCount); err != nil {
		t.Fatal(err)
	}
	if vis != "private" || shareCount != 0 {
		t.Fatalf("after 409: visibility=%q shares=%d", vis, shareCount)
	}

	// Transition run to 'waiting' with a pending hold: still 409 Conflict.
	var runID string
	if err := e.owner.QueryRow(ctx, `SELECT id::text FROM chat.runs WHERE chat_id = $1::uuid`, chatID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `UPDATE chat.runs SET state = 'waiting' WHERE id = $1::uuid`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO chat.holds (chat_id, line_id, user_id, reason, action, preview, state, hold_turn_seq, calls)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'ask', 'open_task', 'preview', 'pending', 1, ARRAY['{}'::jsonb])`,
		chatID, runID, e.people["ada"]); err != nil {
		t.Fatal(err)
	}
	if code, body := e.put(t, "ada", chatID, update); code != http.StatusConflict {
		t.Fatalf("waiting put = %d %s, want 409", code, body)
	}

	// Stop the active run: PUT share now returns 200 OK.
	if code, body := e.call("ada", http.MethodPost, "/api/chats/"+chatID+"/stop", ""); code != http.StatusNoContent {
		t.Fatalf("stop = %d %s", code, body)
	}
	got := e.mustPut(t, "ada", chatID, update)
	if got.Visibility != "everyone" || len(got.Members) != 2 {
		t.Fatalf("after stop put = %+v", got)
	}
}

func TestChatSharePrivateReadFlagAndDefenseInDepth(t *testing.T) {
	e := chatShareSetup(t, false)
	ctx := context.Background()
	chatID := e.openChat(t, "ada")
	if code, body := e.call("ada", http.MethodPost, "/api/chats/"+chatID, `{"body":"check my private memory"}`); code != http.StatusAccepted {
		t.Fatalf("say = %d %s", code, body)
	}
	var lineID string
	if err := e.owner.QueryRow(ctx, `SELECT id::text FROM chat.lines WHERE chat_id = $1::uuid AND seq = 1`, chatID).Scan(&lineID); err != nil {
		t.Fatal(err)
	}
	// Mark chat private_read = true and record a private_read turn and finding.
	if _, err := e.owner.Exec(ctx, `UPDATE chat.chats SET private_read = true WHERE id = $1::uuid`, chatID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO chat.turns (chat_id, line_id, seq, role, body, tool_name, tool_id, private_read, author_id)
		VALUES ($1::uuid, $2::uuid, 1, 'tool', 'Ada private secret memory', 'search_memory', 'call-mem-1', true, $3::uuid)`,
		chatID, lineID, e.people["ada"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.owner.Exec(ctx, `
		INSERT INTO chat.findings (chat_id, line_id, kind, title, body, private_read, author_id)
		VALUES ($1::uuid, $2::uuid, 'tool_output', 'Personal note', 'Ada top secret finding', true, $3::uuid)`,
		chatID, lineID, e.people["ada"]); err != nil {
		t.Fatal(err)
	}

	// GET /api/chats/{id}/share returns private_read: true.
	code, body := e.call("ada", http.MethodGet, chatShareURL(chatID), "")
	if code != http.StatusOK {
		t.Fatalf("get share = %d %s", code, body)
	}
	before := shareDecode[chatShareWire](t, body)
	if !before.PrivateRead {
		t.Fatalf("GET share private_read = false, want true")
	}

	// Share with Bob; PUT response also returns private_read: true.
	after := e.mustPut(t, "ada", chatID, chatShareWire{
		Visibility: "private",
		Members: []shareMemberWire{
			e.entry("person", "ada", "owner"),
			e.entry("person", "bob", "editor"),
		},
	})
	if !after.PrivateRead {
		t.Fatalf("PUT share private_read = false, want true")
	}

	// Defense-in-depth: Bob's RLS view hides private_read findings and redacts private_read turns.
	bobID, err := identity.ParseUserID(e.people["bob"])
	if err != nil {
		t.Fatal(err)
	}
	bobAccess, ok, err := chatAccess(ctx, e.members, bobID)
	if err != nil || !ok {
		t.Fatalf("bob chatAccess = %v %v", ok, err)
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true),
		       set_config('upmo.shared_view', $4, true),
		       set_config('upmo.shared_edit', $5, true)`,
		bobAccess.UserID, bobAccess.Allow, bobAccess.Teams, bobAccess.SharedView, bobAccess.SharedEdit); err != nil {
		t.Fatal(err)
	}
	var visibleFindings, visiblePrivateTurns int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat.findings WHERE chat_id = $1::uuid`, chatID).Scan(&visibleFindings); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid AND private_read`, chatID).Scan(&visiblePrivateTurns); err != nil {
		t.Fatal(err)
	}
	if visibleFindings != 0 || visiblePrivateTurns != 0 {
		t.Fatalf("bob saw private rows: findings=%d turns=%d", visibleFindings, visiblePrivateTurns)
	}
}

func TestChatSharePutRefusals(t *testing.T) {
	e := chatShareSetup(t, false)
	adaChat := e.openChat(t, "ada")
	deeChat := e.openChat(t, "dee")
	owner := e.entry("person", "ada", "owner")
	tests := []struct {
		name string
		as   string
		chat string
		body chatShareWire
		text string
	}{
		{"unknown visibility", "ada", adaChat, chatShareWire{Visibility: "secret", Members: []shareMemberWire{owner}}, "unknown visibility"},
		{"unknown person", "ada", adaChat, chatShareWire{Visibility: "private", Members: []shareMemberWire{
			owner, {SubjectType: "person", SubjectID: shareGhostPerson, Role: "editor"},
		}}, "that person is not a member"},
		{"disabled person", "ada", adaChat, chatShareWire{Visibility: "private", Members: []shareMemberWire{
			owner, e.entry("person", "ex", "viewer"),
		}}, "that person is not a member"},
		{"unknown team", "ada", adaChat, chatShareWire{Visibility: "private", Members: []shareMemberWire{
			owner, {SubjectType: "team", SubjectID: shareGhostTeam, Role: "viewer"},
		}}, "that team does not exist"},
		{"duplicate person", "ada", adaChat, chatShareWire{Visibility: "private", Members: []shareMemberWire{
			owner, e.entry("person", "bob", "editor"), e.entry("person", "bob", "viewer"),
		}}, "that access is listed twice"},
		{"owner removed", "ada", adaChat, chatShareWire{Visibility: "private", Members: []shareMemberWire{
			e.entry("person", "bob", "editor"),
		}}, "the owner cannot be removed"},
		{"owner role changed", "ada", adaChat, chatShareWire{Visibility: "private", Members: []shareMemberWire{
			e.entry("person", "ada", "editor"),
		}}, "the owner cannot be changed"},
		{"team visibility needs team when multiple", "ada", adaChat, chatShareWire{Visibility: "team", Members: []shareMemberWire{owner}}, "the owner belongs to more than one team"},
		{"team visibility not member", "ada", adaChat, chatShareWire{Visibility: "team", TeamID: e.teams["ops"], Members: []shareMemberWire{owner}}, "the owner does not belong to that team"},
		{"team visibility no team", "dee", deeChat, chatShareWire{Visibility: "team", Members: []shareMemberWire{e.entry("person", "dee", "owner")}}, "the owner belongs to no team"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body := e.put(t, tc.as, tc.chat, tc.body)
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d %s, want 422", code, body)
			}
			if got := shareDecode[map[string]string](t, body)["error"]; got != tc.text {
				t.Fatalf("error = %q, want %q", got, tc.text)
			}
		})
	}
}
