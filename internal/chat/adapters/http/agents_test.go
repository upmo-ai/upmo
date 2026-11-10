package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

const agentID = "018f3b2c-7a10-7c4a-8e11-cccccccccccc"

// fakeAgents keeps the agents of one person and records the calls.
type fakeAgents struct {
	list    []app.Agent
	created []app.AgentFields
	global  bool
	written []app.AgentFields
	deleted []string
	err     error
}

func (f *fakeAgents) ListAgents(context.Context, app.Access) ([]app.Agent, error) {
	return f.list, f.err
}

func (f *fakeAgents) GetAgent(_ context.Context, _ app.Access, id string) (app.Agent, error) {
	for _, a := range f.list {
		if a.ID == id {
			return a, nil
		}
	}
	return app.Agent{}, app.ErrAgentNotFound
}

func (f *fakeAgents) CreateAgent(_ context.Context, _ app.Access, in app.AgentFields, global bool) (app.Agent, error) {
	f.created = append(f.created, in)
	f.global = global
	return app.Agent{ID: agentID, Name: in.Name, Enabled: in.Enabled, Global: global, Editable: true}, f.err
}

func (f *fakeAgents) UpdateAgent(_ context.Context, _ app.Access, id string, in app.AgentFields) (app.Agent, error) {
	f.written = append(f.written, in)
	return app.Agent{ID: id, Name: in.Name, Route: in.Route, Enabled: in.Enabled, Editable: true}, f.err
}

func (f *fakeAgents) DeleteAgent(_ context.Context, _ app.Access, id string) error {
	f.deleted = append(f.deleted, id)
	return f.err
}

// agentCall sends a request as a signed-in person whose allow list is allow,
// which may be empty; "none" is no session at all.
func agentCall(t *testing.T, book *fakeAgents, allow, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	AgentsHandler{
		Agents: book,
		Access: func(*http.Request) (app.Access, bool) {
			if allow == "none" {
				return app.Access{}, false
			}
			return app.Access{UserID: userID, Allow: allow}, true
		},
	}.Mount(mux, nil)
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

const memberAllow = "chat:create,chat:view,chat:edit"

func TestAgentsListShowsScopeAndWhetherThePersonMayChangeEach(t *testing.T) {
	book := &fakeAgents{list: []app.Agent{
		{ID: agentID, Name: "Mine", Route: "fast", ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true, Editable: true},
		{ID: "018f3b2c-7a10-7c4a-8e11-dddddddddddd", Name: "House", PromptOverlay: "Rules.", Enabled: true, Global: true},
	}}
	rec := agentCall(t, book, memberAllow, http.MethodGet, "/api/agents", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got []agentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Scope != "own" || !got[0].Editable || got[0].Route != "fast" || got[0].ModelProvider != "OpenAI" ||
		got[1].Scope != "global" || got[1].Editable || got[1].PromptOverlay != "Rules." {
		t.Fatalf("list = %+v", got)
	}
	empty := agentCall(t, &fakeAgents{}, memberAllow, http.MethodGet, "/api/agents", "")
	if strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("an empty list is %q", empty.Body.String())
	}
}

func TestAgentsNeedASignedInPerson(t *testing.T) {
	book := &fakeAgents{}
	for _, tc := range [][2]string{{http.MethodGet, "/api/agents"}, {http.MethodPost, "/api/agents"},
		{http.MethodPatch, "/api/agents/" + agentID}, {http.MethodDelete, "/api/agents/" + agentID}} {
		if rec := agentCall(t, book, "none", tc[0], tc[1], `{}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: status %d", tc[0], tc[1], rec.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	r.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	AgentsHandler{Agents: book, Access: func(*http.Request) (app.Access, bool) { return app.Access{UserID: userID, Allow: memberAllow}, true }}.Mount(mux, nil)
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a token: status %d", rec.Code)
	}
}

func TestAgentCreateStoresOwnByDefaultAndGlobalOnlyForAManager(t *testing.T) {
	book := &fakeAgents{}
	body := `{"name":"Research","description":"d","prompt_overlay":"p","route":"fast","model_provider":"OpenAI","model_id":"gpt-x"}`
	rec := agentCall(t, book, memberAllow, http.MethodPost, "/api/agents", body)
	if rec.Code != http.StatusCreated || len(book.created) != 1 || book.global {
		t.Fatalf("status %d created %+v global %v", rec.Code, book.created, book.global)
	}
	want := app.AgentFields{Name: "Research", Description: "d", PromptOverlay: "p", Route: "fast", ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true}
	if book.created[0] != want {
		t.Fatalf("created = %+v", book.created[0])
	}

	// A member asking for the global scope is refused; the book is not called.
	book.created = nil
	rec = agentCall(t, book, memberAllow, http.MethodPost, "/api/agents", `{"name":"House","scope":"global"}`)
	if rec.Code != http.StatusForbidden || len(book.created) != 0 {
		t.Fatalf("member global: status %d created %+v", rec.Code, book.created)
	}
	rec = agentCall(t, book, memberAllow+",settings:manage", http.MethodPost, "/api/agents", `{"name":"House","scope":"global","enabled":false}`)
	if rec.Code != http.StatusCreated || !book.global || book.created[0].Enabled {
		t.Fatalf("manager global: status %d global %v created %+v", rec.Code, book.global, book.created)
	}
	rec = agentCall(t, book, memberAllow, http.MethodPost, "/api/agents", `{"name":"x","scope":"team"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown scope: status %d", rec.Code)
	}
}

func TestAgentRefusalsArePlainMessagesWithTheRightStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		body   string
		status int
		text   string
	}{
		"no name":   {nil, `{"name":" "}`, http.StatusUnprocessableEntity, "Give the agent a name."},
		"half pair": {nil, `{"name":"a","model_provider":"OpenAI"}`, http.StatusUnprocessableEntity, "Give both a provider and a model, or neither."},
		"duplicate": {app.ErrAgentDuplicate, `{"name":"a"}`, http.StatusConflict, "An agent with that name already exists."},
		"global":    {app.ErrAgentGlobal, `{"name":"a"}`, http.StatusForbidden, "This agent is managed by your workspace."},
		"broken":    {context.DeadlineExceeded, `{"name":"a"}`, http.StatusInternalServerError, "agents"},
		"bad json":  {nil, `{`, http.StatusBadRequest, "bad request"},
	} {
		rec := agentCall(t, &fakeAgents{err: tc.err}, memberAllow, http.MethodPost, "/api/agents", tc.body)
		var got struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != tc.status || got.Error != tc.text {
			t.Fatalf("%s: status %d error %q, want %d %q", name, rec.Code, got.Error, tc.status, tc.text)
		}
	}
}

func TestAgentChangeIsPartialAndDeleteAnswersNoContent(t *testing.T) {
	book := &fakeAgents{list: []app.Agent{{ID: agentID, Name: "Mine", Route: "deep", Enabled: true, Editable: true}}}
	rec := agentCall(t, book, memberAllow, http.MethodPatch, "/api/agents/"+agentID, `{"route":"fast"}`)
	if rec.Code != http.StatusOK || len(book.written) != 1 || book.written[0].Route != "fast" || book.written[0].Name != "Mine" || !book.written[0].Enabled {
		t.Fatalf("status %d written %+v", rec.Code, book.written)
	}
	rec = agentCall(t, book, memberAllow, http.MethodPatch, "/api/agents/"+agentID, `{"enabled":false}`)
	if rec.Code != http.StatusOK || book.written[1].Enabled || book.written[1].Route != "deep" {
		t.Fatalf("status %d written %+v", rec.Code, book.written)
	}
	if rec := agentCall(t, book, memberAllow, http.MethodPatch, "/api/agents/018f3b2c-7a10-7c4a-8e11-eeeeeeeeeeee", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: status %d", rec.Code)
	}
	rec = agentCall(t, book, memberAllow, http.MethodDelete, "/api/agents/"+agentID, "")
	if rec.Code != http.StatusNoContent || len(book.deleted) != 1 || rec.Body.Len() != 0 {
		t.Fatalf("delete: status %d deleted %v", rec.Code, book.deleted)
	}
}

func TestAgentsNeedNoChatPermissionOnlyASession(t *testing.T) {
	// Signed in with no permission at all: lists, and creates their own.
	book := &fakeAgents{}
	if rec := agentCall(t, book, "", http.MethodGet, "/api/agents", ""); rec.Code != http.StatusOK {
		t.Fatalf("list with no permission: status %d", rec.Code)
	}
	if rec := agentCall(t, book, "", http.MethodPost, "/api/agents", `{"name":"Mine"}`); rec.Code != http.StatusCreated || book.global {
		t.Fatalf("create own with no permission: status %d global %v", rec.Code, book.global)
	}
	// A person with no permission is refused the global scope; a settings-only
	// manager, who holds no chat permission, is not.
	if rec := agentCall(t, book, "", http.MethodPost, "/api/agents", `{"name":"H","scope":"global"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("global without settings:manage: status %d", rec.Code)
	}
	if rec := agentCall(t, book, "settings:manage", http.MethodPost, "/api/agents", `{"name":"H","scope":"global"}`); rec.Code != http.StatusCreated || !book.global {
		t.Fatalf("global for a settings-only manager: status %d global %v", rec.Code, book.global)
	}
}
