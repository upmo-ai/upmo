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
