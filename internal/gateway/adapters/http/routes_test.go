package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

type fakeRouteStore struct {
	rows       []app.ListedRoute
	createErr  error
	updateErr  error
	shareErr   error
	removeErr  error
	lastCreate app.RouteSpec
	lastUpdate app.RouteSpec
	lastShare  struct {
		id, vis string
		teams   []string
	}
	lastRemove string
}

func (f *fakeRouteStore) List(context.Context, app.Caller) ([]app.ListedRoute, error) {
	return append([]app.ListedRoute(nil), f.rows...), nil
}

func (f *fakeRouteStore) Get(_ context.Context, _ app.Caller, id string) (app.ListedRoute, error) {
	for _, r := range f.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return app.ListedRoute{}, app.ErrNotFound
}

func (f *fakeRouteStore) Create(_ context.Context, who app.Caller, spec app.RouteSpec) (app.ListedRoute, error) {
	f.lastCreate = spec
	if f.createErr != nil {
		return app.ListedRoute{}, f.createErr
	}
	return app.ListedRoute{
		ID:          "018f3b2c-7a10-7c4a-8e11-999999999999",
		Name:        spec.Name,
		Capability:  spec.Capability,
		Decision:    spec.Decision,
		Visibility:  spec.Visibility,
		TeamIDs:     spec.TeamIDs,
		OwnerUserID: who.User,
		Hops: []app.ListedHop{{
			Position:  spec.Hops[0].Position,
			Adapter:   "fake",
			Model:     spec.Hops[0].Model,
			Weight:    spec.Hops[0].Weight,
			Retry:     spec.Hops[0].Retry,
			AccountID: spec.Hops[0].AccountID,
		}},
	}, nil
}

func (f *fakeRouteStore) Update(_ context.Context, _ app.Caller, id string, spec app.RouteSpec) (app.ListedRoute, error) {
	f.lastUpdate = spec
	if f.updateErr != nil {
		return app.ListedRoute{}, f.updateErr
	}
	for _, r := range f.rows {
		if r.ID == id {
			r.Name = spec.Name
			r.Capability = spec.Capability
			r.Decision = spec.Decision
			r.Visibility = spec.Visibility
			r.TeamIDs = spec.TeamIDs
			return r, nil
		}
	}
	return app.ListedRoute{}, app.ErrNotFound
}

func (f *fakeRouteStore) Share(_ context.Context, _ app.Caller, id, vis string, teams []string) (app.ListedRoute, error) {
	f.lastShare = struct {
		id, vis string
		teams   []string
	}{id, vis, teams}
	if f.shareErr != nil {
		return app.ListedRoute{}, f.shareErr
	}
	for _, r := range f.rows {
		if r.ID == id {
			r.Visibility = vis
			r.TeamIDs = teams
			return r, nil
		}
	}
	return app.ListedRoute{}, app.ErrNotFound
}

func (f *fakeRouteStore) Remove(_ context.Context, _ app.Caller, id string) error {
	f.lastRemove = id
	return f.removeErr
}

type fakeRoutes struct {
	calls int
	rows  []app.ListedRoute
}

func (f *fakeRoutes) List(context.Context) ([]app.ListedRoute, error) {
	f.calls++
	return append([]app.ListedRoute(nil), f.rows...), nil
}

func TestListGatewayRoutes(t *testing.T) {
	book := &fakeRoutes{rows: []app.ListedRoute{{
		Name: "memory-check", Capability: "decide", Decision: "yesno",
		Hops: []app.ListedHop{{Position: 1, Adapter: "openai_compat", Model: "gpt-4.1-mini", Weight: 1, Retry: 1}},
	}}}
	rec := callRoutes(t, book, true)
	if rec.Code != http.StatusOK || book.calls != 1 {
		t.Fatalf("status %d calls %d body %s", rec.Code, book.calls, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["name"] != "memory-check" {
		t.Fatalf("got %#v err %v", got, err)
	}
	if strings.Contains(rec.Body.String(), "credential") {
		t.Fatalf("credential leaked: %s", rec.Body.String())
	}
}

func TestListGatewayRoutesNeedsASession(t *testing.T) {
	book := &fakeRoutes{}
	rec := callRoutes(t, book, false)
	if rec.Code != http.StatusUnauthorized || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

// A signed-in person with no permission at all still reads the routes: the
// composer needs the names to offer them.
func TestListGatewayRoutesOpenToAnySignedInPerson(t *testing.T) {
	book := &fakeRoutes{rows: []app.ListedRoute{{Name: "chat", Capability: "chat"}}}
	rec := callRoutes(t, book, true)
	if rec.Code != http.StatusOK || book.calls != 1 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestListGatewayRoutesRefusesBearer(t *testing.T) {
	book := &fakeRoutes{}
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	routesHandler(book, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func callRoutes(t *testing.T, book *fakeRoutes, signedIn bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil)
	rec := httptest.NewRecorder()
	routesHandler(book, signedIn).ServeHTTP(rec, req)
	return rec
}

func routesHandler(book *fakeRoutes, signedIn bool) http.Handler {
	mux := http.NewServeMux()
	RoutesHandler{
		Book: book,
		Access: func(*http.Request) (string, bool) {
			if !signedIn {
				return "", false
			}
			return "018f3b2c-7a10-7c4a-8e11-111111111111", true
		},
	}.MountRoutes(mux, nil)
	return mux
}

// fakeAccounts answers the provider list with fixed rows.
type fakeAccounts struct {
	rows []domain.Provider
	err  error
}

func (f *fakeAccounts) List(context.Context, string) ([]domain.Provider, error) {
	return f.rows, f.err
}

// usableRoutesHandler is the routes handler with the person's accounts wired,
// so the rows carry their usable answer.
func usableRoutesHandler(t *testing.T, book *fakeRoutes, accounts *fakeAccounts) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	RoutesHandler{
		Book:     book,
		Accounts: accounts,
		Access: func(*http.Request) (string, bool) {
			return "018f3b2c-7a10-7c4a-8e11-111111111111", true
		},
	}.MountRoutes(mux, nil)
	return mux
}

func TestListGatewayRoutesMarkEachUsableForTheCallingPerson(t *testing.T) {
	book := &fakeRoutes{rows: []app.ListedRoute{{
		Name:       "chat",
		Capability: "chat",
		Hops: []app.ListedHop{
			{Position: 1, Adapter: "fake", Model: "m1", AccountID: "mine"},
			{Position: 2, Adapter: "fake", Model: "m2", AccountID: "theirs"},
		},
	}}}
	accounts := &fakeAccounts{rows: []domain.Provider{
		{ID: "mine", Status: domain.StatusNotConnected, Models: []string{"m1"}, Own: true},
	}}
	rec := httptest.NewRecorder()
	usableRoutesHandler(t, book, accounts).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// No hop reaches a usable account of this person's: theirs is Not
	// connected, and the other hop names an account they cannot see.
	if got[0]["usable"] != false {
		t.Fatalf("a route through a Not connected account read usable: %v", got[0])
	}

	// The account connects, and the hop through it makes the route usable.
	accounts.rows[0].Status = domain.StatusConnected
	rec = httptest.NewRecorder()
	usableRoutesHandler(t, book, accounts).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil))
	got = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[0]["usable"] != true {
		t.Fatalf("a route with a usable hop read unusable: %v", got[0])
	}
}

func TestListGatewayRoutesWithoutAccountsMarksNoneUsable(t *testing.T) {
	book := &fakeRoutes{rows: []app.ListedRoute{{Name: "chat", Capability: "chat",
		Hops: []app.ListedHop{{Position: 1, Adapter: "fake", Model: "m1", AccountID: "mine"}}}}}
	rec := httptest.NewRecorder()
	routesHandler(book, true).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil))
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 {
		t.Fatalf("got %#v err %v", got, err)
	}
	if got[0]["usable"] != false {
		t.Fatalf("no accounts wired: usable = %v", got[0]["usable"])
	}
}

func TestListGatewayRoutesRefusesWhenTheAccountsFail(t *testing.T) {
	book := &fakeRoutes{rows: []app.ListedRoute{{Name: "chat", Capability: "chat"}}}
	rec := httptest.NewRecorder()
	usableRoutesHandler(t, book, &fakeAccounts{err: context.DeadlineExceeded}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRouteCRUDEndpointsHTTP(t *testing.T) {
	userID := "018f3b2c-7a10-7c4a-8e11-111111111111"
	acctID := "018f3b2c-7a10-7c4a-8e11-222222222222"
	routeID := "018f3b2c-7a10-7c4a-8e11-333333333333"
	installedID := "018f3b2c-7a10-7c4a-8e11-444444444444"
	teamID := "018f3b2c-7a10-7c4a-8e11-555555555555"

	store := &fakeRouteStore{
		rows: []app.ListedRoute{
			{
				ID: routeID, Name: "mine", Capability: "chat", Visibility: domain.VisibilityPrivate, OwnerUserID: userID,
				Hops: []app.ListedHop{{Position: 1, Adapter: "fake", Model: "m1", Weight: 1, Retry: 1, AccountID: acctID}},
			},
			{
				ID: installedID, Name: "chat", Capability: "chat", Visibility: domain.VisibilityEveryone, Installed: true,
				Hops: []app.ListedHop{{Position: 1, Adapter: "fake", Model: "m1", Weight: 1, Retry: 1, AccountID: acctID}},
			},
		},
	}
	allow := ""
	svc := app.RouteService{
		Store: store,
		Allow: func(context.Context, string) (string, error) { return allow, nil },
	}
	mux := http.NewServeMux()
	RoutesHandler{
		Service: svc,
		Accounts: &fakeAccounts{rows: []domain.Provider{
			{ID: acctID, Status: domain.StatusConnected, Models: []string{"m1"}, Own: true},
		}},
		Access: func(*http.Request) (string, bool) { return userID, true },
	}.MountRoutes(mux, nil)

	// Create private route.
	body := `{"name":"custom-chat","capability":"chat","hops":[{"account_id":"` + acctID + `","model":"m1"}]}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/gateway/routes", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created app.ListedRoute
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Usable || !created.CanManage || !created.CanUse || !created.IsOwner {
		t.Fatalf("created flags = %+v", created)
	}

	// Reject >3 hops with 422.
	tooMany := `{"name":"big","capability":"chat","hops":[` +
		`{"account_id":"` + acctID + `","model":"m1"},` +
		`{"account_id":"` + acctID + `","model":"m2"},` +
		`{"account_id":"` + acctID + `","model":"m3"},` +
		`{"account_id":"` + acctID + `","model":"m4"}]}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/gateway/routes", strings.NewReader(tooMany)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf(">3 hops = %d %s, want 422", rec.Code, rec.Body.String())
	}

	// Sharing without settings:manage is 403.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/gateway/routes/"+routeID+"/share", strings.NewReader(`{"visibility":"everyone"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("share without perm = %d %s, want 403", rec.Code, rec.Body.String())
	}

	// Sharing with settings:manage succeeds.
	allow = domain.SharePermission
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/gateway/routes/"+routeID+"/share", strings.NewReader(`{"visibility":"team","team_ids":["`+teamID+`"]}`)))
	if rec.Code != http.StatusOK || store.lastShare.vis != domain.VisibilityTeam {
		t.Fatalf("share with perm = %d %s", rec.Code, rec.Body.String())
	}

	// Editing or deleting an installed route is 403.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/gateway/routes/"+installedID, strings.NewReader(`{"name":"hacked"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("edit installed = %d %s, want 403", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/gateway/routes/"+installedID, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("delete installed = %d %s, want 403", rec.Code, rec.Body.String())
	}

	// Deleting own route returns 204.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/gateway/routes/"+routeID, nil))
	if rec.Code != http.StatusNoContent || store.lastRemove != routeID {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
}
