package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/agents/domain"
	"github.com/upmo-ai/upmo/internal/gateway"
)

const (
	userID  = "018f3b2c-7a10-7c4a-8e11-111111111111"
	agentID = "018f3b2c-7a10-7c4a-8e11-cccccccccccc"
)

type fakeBook struct {
	list    []app.Agent
	created []app.AgentFields
	written []app.AgentFields
	deleted []string
	err     error
}

func (f *fakeBook) ListAgents(context.Context, app.Access) ([]app.Agent, error) {
	return f.list, f.err
}

func (f *fakeBook) GetAgent(_ context.Context, _ app.Access, id string) (app.Agent, error) {
	for _, a := range f.list {
		if a.ID == id {
			return a, nil
		}
	}
	return app.Agent{}, app.ErrAgentNotFound
}

func (f *fakeBook) AgentForUse(_ context.Context, _ app.Access, id string) (app.Agent, []domain.Lesson, error) {
	a, err := f.GetAgent(context.Background(), app.Access{}, id)
	return a, nil, err
}

func (f *fakeBook) CreateAgent(_ context.Context, _ app.Access, in app.AgentFields) (app.Agent, error) {
	f.created = append(f.created, in)
	return app.Agent{
		ID:                   agentID,
		Name:                 in.Name,
		Slug:                 in.Slug,
		Role:                 in.Role,
		Description:          in.Description,
		Instructions:         in.Instructions,
		DefaultRoute:         in.DefaultRoute,
		DefaultModelProvider: in.DefaultModelProvider,
		DefaultModelID:       in.DefaultModelID,
		ToolClasses:          in.ToolClasses,
		ConnectorIDs:         in.ConnectorIDs,
		KnowledgeFolderIDs:   in.KnowledgeFolderIDs,
		PlanApproval:         in.PlanApproval,
		CheckCriteria:        in.CheckCriteria,
		Enabled:              in.Enabled,
		Version:              1,
		Share:                app.AgentShare{Visibility: in.Visibility, CanManage: true},
	}, f.err
}

func (f *fakeBook) UpdateAgent(_ context.Context, _ app.Access, id string, in app.AgentFields) (app.Agent, error) {
	f.written = append(f.written, in)
	return app.Agent{
		ID:           id,
		Name:         in.Name,
		Slug:         in.Slug,
		Instructions: in.Instructions,
		DefaultRoute: in.DefaultRoute,
		PlanApproval: in.PlanApproval,
		Enabled:      in.Enabled,
		Version:      2,
		Share:        app.AgentShare{Visibility: in.Visibility, CanManage: true},
	}, f.err
}

func (f *fakeBook) DeleteAgent(_ context.Context, _ app.Access, id string) error {
	f.deleted = append(f.deleted, id)
	return f.err
}

func (f *fakeBook) ListVersions(context.Context, app.Access, string) ([]app.Version, error) {
	return nil, nil
}

func (f *fakeBook) GetVersion(context.Context, app.Access, string, int) (app.Version, error) {
	return app.Version{}, app.ErrVersionNotFound
}

func agentCall(t *testing.T, book *fakeBook, allow, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Handler{
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

func TestAgentsCreateAcceptsLegacyAndNewFields(t *testing.T) {
	book := &fakeBook{}
	body := `{"name":"Research","description":"d","prompt_overlay":"p","route":"fast","model_provider":"OpenAI","model_id":"gpt-x","tool_classes":["web"]}`
	rec := agentCall(t, book, memberAllow, http.MethodPost, "/api/agents", body)
	if rec.Code != http.StatusCreated || len(book.created) != 1 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got agentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PromptOverlay != "p" || got.Instructions != "p" || got.Route != "fast" || got.DefaultRoute != "fast" ||
		got.Slug != "research" || got.Version != 1 || !reflect.DeepEqual(got.ToolClasses, []string{"web"}) {
		t.Fatalf("response = %+v", got)
	}

	book.created = nil
	body2 := `{"name":"Bookkeeper","slug":"ap-clerk","instructions":"Check VAT.","default_route":"work","plan_approval":"always"}`
	rec = agentCall(t, book, memberAllow, http.MethodPost, "/api/agents", body2)
	if rec.Code != http.StatusCreated || book.created[0].Slug != "ap-clerk" || book.created[0].Instructions != "Check VAT." || book.created[0].PlanApproval != "always" {
		t.Fatalf("status %d created %+v", rec.Code, book.created)
	}
}

func TestAgentsPatchSupportsBothFieldNamesAndDelete(t *testing.T) {
	book := &fakeBook{list: []app.Agent{{
		ID: agentID, Name: "Mine", Slug: "mine", DefaultRoute: "deep", PlanApproval: "risky", Enabled: true,
		Share: app.AgentShare{Visibility: "private", CanManage: true},
	}}}
	rec := agentCall(t, book, memberAllow, http.MethodPatch, "/api/agents/"+agentID, `{"route":"fast","prompt_overlay":"New overlay"}`)
	if rec.Code != http.StatusOK || len(book.written) != 1 || book.written[0].DefaultRoute != "fast" || book.written[0].Instructions != "New overlay" {
		t.Fatalf("status %d written %+v", rec.Code, book.written)
	}
	rec = agentCall(t, book, memberAllow, http.MethodDelete, "/api/agents/"+agentID, "")
	if rec.Code != http.StatusNoContent || len(book.deleted) != 1 {
		t.Fatalf("delete: status %d deleted %v", rec.Code, book.deleted)
	}
}

type fakeRouteBook struct{ rows []gateway.ListedRoute }

func (f *fakeRouteBook) List(context.Context, string) ([]gateway.ListedRoute, error) {
	return f.rows, nil
}

type fakeAccountList struct{ rows []gateway.Provider }

func (f *fakeAccountList) List(context.Context, string) ([]gateway.Provider, error) {
	return f.rows, nil
}

func TestAgentsListCarriesUsableAndEmptySlices(t *testing.T) {
	book := &fakeBook{list: []app.Agent{
		{ID: agentID, Name: "By model", Slug: "by-model", DefaultModelProvider: "fake", DefaultModelID: "m1", Enabled: true, Version: 1, Share: app.AgentShare{CanUse: true}},
		{ID: "018f3b2c-7a10-7c4a-8e11-dddddddddddd", Name: "Off", Slug: "off", Enabled: false, Version: 1, Share: app.AgentShare{CanUse: true}},
	}}
	mux := http.NewServeMux()
	Handler{
		Agents: book,
		Routes: &fakeRouteBook{rows: []gateway.ListedRoute{{
			Name: "chat", Capability: "chat", CanUse: true,
			Hops: []gateway.ListedHop{{Position: 1, Adapter: "fake", Model: "m1", AccountID: "mine"}},
		}}},
		Accounts: &fakeAccountList{rows: []gateway.Provider{
			{ID: "mine", Provider: "fake", Status: gateway.StatusConnected, Models: []string{"m1"}, Own: true},
		}},
		DefaultRoute: "chat",
		Access:       func(*http.Request) (app.Access, bool) { return app.Access{UserID: userID, Allow: memberAllow}, true },
	}.Mount(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var got []agentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got[0].Usable || got[1].Usable {
		t.Fatalf("usable = %+v", got)
	}
}

func TestAgentsOnARouteThePersonOnlyManagesAreNotUsable(t *testing.T) {
	book := &fakeBook{list: []app.Agent{
		{ID: agentID, Name: "On team route", Slug: "on-team-route", DefaultRoute: "team", Enabled: true, Version: 1, Share: app.AgentShare{CanUse: true}},
	}}
	mux := http.NewServeMux()
	Handler{
		Agents: book,
		Routes: &fakeRouteBook{rows: []gateway.ListedRoute{{
			Name: "team", Capability: "chat", CanManage: true,
			Hops: []gateway.ListedHop{{Position: 1, Adapter: "fake", Model: "m1", AccountID: "house"}},
		}}},
		Accounts: &fakeAccountList{rows: []gateway.Provider{
			{ID: "house", Provider: "fake", Status: gateway.StatusConnected, Models: []string{"m1"}},
		}},
		Access: func(*http.Request) (app.Access, bool) { return app.Access{UserID: userID, Allow: memberAllow}, true },
	}.Mount(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var got []agentJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Usable {
		t.Fatalf("an agent on a route the person cannot use is offered: %+v", got)
	}
}
