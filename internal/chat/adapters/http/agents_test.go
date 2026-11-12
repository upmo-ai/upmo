package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/gateway"
)

const agentID = "018f3b2c-7a10-7c4a-8e11-cccccccccccc"

type fakeAgents struct {
	list    []app.Agent
	created []app.AgentFields
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

func (f *fakeAgents) CreateAgent(_ context.Context, _ app.Access, in app.AgentFields) (app.Agent, error) {
	f.created = append(f.created, in)
	return app.Agent{ID: agentID, Name: in.Name, Enabled: in.Enabled, Share: app.AgentShare{Visibility: in.Visibility, CanManage: true}}, f.err
}

func (f *fakeAgents) UpdateAgent(_ context.Context, _ app.Access, id string, in app.AgentFields) (app.Agent, error) {
	f.written = append(f.written, in)
	return app.Agent{ID: id, Name: in.Name, Route: in.Route, Enabled: in.Enabled, Share: app.AgentShare{CanManage: true}}, f.err
}

func (f *fakeAgents) DeleteAgent(_ context.Context, _ app.Access, id string) error {
	f.deleted = append(f.deleted, id)
	return f.err
}

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

const memberAllow = "agent:create,agent:view,agent:edit,agent:delete,agent:share"

func TestAgentsListShowsVisibilityAndWhetherThePersonMayManageEach(t *testing.T) {
	book := &fakeAgents{list: []app.Agent{
		{ID: agentID, Name: "Mine", Route: "fast", ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true,
			Share: app.AgentShare{Visibility: "private", CanManage: true, CanUse: true}},
		{ID: "018f3b2c-7a10-7c4a-8e11-dddddddddddd", Name: "House", PromptOverlay: "Rules.", Enabled: true,
			Share: app.AgentShare{Visibility: "everyone", CanManage: false, CanUse: true}},
	}}
	rec := agentCall(t, book, memberAllow, http.MethodGet, "/api/agents", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got []agentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Visibility != "private" || !got[0].CanManage || got[0].Route != "fast" ||
		got[1].Visibility != "everyone" || got[1].CanManage || got[1].PromptOverlay != "Rules." {
		t.Fatalf("list = %+v", got)
	}
}

func TestAgentCreateStoresPrivateByDefaultAndSharedOnlyForAManager(t *testing.T) {
	book := &fakeAgents{}
	body := `{"name":"Research","description":"d","prompt_overlay":"p","route":"fast","model_provider":"OpenAI","model_id":"gpt-x"}`
	rec := agentCall(t, book, memberAllow, http.MethodPost, "/api/agents", body)
	if rec.Code != http.StatusCreated || len(book.created) != 1 {
		t.Fatalf("status %d created %+v", rec.Code, book.created)
	}
	want := app.AgentFields{Name: "Research", Description: "d", PromptOverlay: "p", Route: "fast", ModelProvider: "OpenAI", ModelID: "gpt-x", Enabled: true, Visibility: "private"}
	got := book.created[0]
	if len(got.TeamIDs) == 0 {
		got.TeamIDs = nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created = %+v", book.created[0])
	}

	book.created = nil
	rec = agentCall(t, book, memberAllow, http.MethodPost, "/api/agents", `{"name":"House","visibility":"everyone"}`)
	if rec.Code != http.StatusForbidden || len(book.created) != 0 {
		t.Fatalf("member workspace: status %d created %+v", rec.Code, book.created)
	}
	rec = agentCall(t, book, memberAllow+",settings:manage", http.MethodPost, "/api/agents", `{"name":"House","visibility":"everyone","enabled":false}`)
	if rec.Code != http.StatusCreated || book.created[0].Visibility != "everyone" || book.created[0].Enabled {
		t.Fatalf("manager workspace: status %d created %+v", rec.Code, book.created)
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
		"managed":   {app.ErrAgentManaged, `{"name":"a"}`, http.StatusForbidden, "This agent is managed by your workspace."},
		"no create": {app.ErrAgentCreate, `{"name":"a"}`, http.StatusForbidden, "You cannot create an agent."},
		"broken":    {context.DeadlineExceeded, `{"name":"a"}`, http.StatusInternalServerError, "agents"},
	} {
		allow := memberAllow
		if name == "no create" {
			allow = "agent:view"
		}
		rec := agentCall(t, &fakeAgents{err: tc.err}, allow, http.MethodPost, "/api/agents", tc.body)
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
	book := &fakeAgents{list: []app.Agent{{ID: agentID, Name: "Mine", Route: "deep", Enabled: true,
		Share: app.AgentShare{CanManage: true}}}}
	rec := agentCall(t, book, memberAllow, http.MethodPatch, "/api/agents/"+agentID, `{"route":"fast"}`)
	if rec.Code != http.StatusOK || len(book.written) != 1 || book.written[0].Route != "fast" {
		t.Fatalf("status %d written %+v", rec.Code, book.written)
	}
	rec = agentCall(t, book, memberAllow, http.MethodDelete, "/api/agents/"+agentID, "")
	if rec.Code != http.StatusNoContent || len(book.deleted) != 1 {
		t.Fatalf("delete: status %d deleted %v", rec.Code, book.deleted)
	}
}

type fakeRouteBook struct{ rows []gateway.ListedRoute }

func (f *fakeRouteBook) List(context.Context) ([]gateway.ListedRoute, error) {
	return f.rows, nil
}

type fakeAccountList struct{ rows []gateway.Provider }

func (f *fakeAccountList) List(context.Context, string) ([]gateway.Provider, error) {
	return f.rows, nil
}

func usableSources() (*fakeRouteBook, *fakeAccountList) {
	routes := &fakeRouteBook{rows: []gateway.ListedRoute{{
		Name: "chat", Capability: "chat",
		Hops: []gateway.ListedHop{{Position: 1, Adapter: "fake", Model: "m1", AccountID: "mine"}},
	}}}
	accounts := &fakeAccountList{rows: []gateway.Provider{
		{ID: "mine", Provider: "fake", Status: gateway.StatusConnected, Models: []string{"m1"}, Own: true},
	}}
	return routes, accounts
}

func usableAgentCall(t *testing.T, book *fakeAgents, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	routes, accounts := usableSources()
	mux := http.NewServeMux()
	AgentsHandler{
		Agents: book, Routes: routes, Accounts: accounts, DefaultRoute: "chat",
		Access: func(*http.Request) (app.Access, bool) {
			return app.Access{UserID: userID, Allow: memberAllow}, true
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

func TestAgentsListCarriesTheUsableAnswer(t *testing.T) {
	book := &fakeAgents{list: []app.Agent{
		{ID: agentID, Name: "By model", ModelProvider: "fake", ModelID: "m1", Enabled: true, Share: app.AgentShare{CanUse: true}},
		{ID: "018f3b2c-7a10-7c4a-8e11-dddddddddddd", Name: "Off", Enabled: false, Share: app.AgentShare{CanUse: true}},
	}}
	rec := usableAgentCall(t, book, http.MethodGet, "/api/agents", "")
	var got []agentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[0].Usable != true || got[1].Usable != false {
		t.Fatalf("usable flags = %+v", got)
	}
}
