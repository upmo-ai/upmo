package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
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
	rec := callRoutes(t, book, "settings:manage")
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

func TestListGatewayRoutesRefusesEmptyAllow(t *testing.T) {
	book := &fakeRoutes{}
	rec := callRoutes(t, book, "")
	if rec.Code != http.StatusUnauthorized || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestListGatewayRoutesRefusesViewer(t *testing.T) {
	book := &fakeRoutes{}
	rec := callRoutes(t, book, "task:view")
	if rec.Code != http.StatusUnauthorized || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestListGatewayRoutesRefusesBearer(t *testing.T) {
	book := &fakeRoutes{}
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	routesHandler(book, "settings:manage").ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func callRoutes(t *testing.T, book *fakeRoutes, allow string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/routes", nil)
	rec := httptest.NewRecorder()
	routesHandler(book, allow).ServeHTTP(rec, req)
	return rec
}

func routesHandler(book *fakeRoutes, allow string) http.Handler {
	mux := http.NewServeMux()
	RoutesHandler{
		Book: book,
		Access: func(*http.Request) (string, string, bool) {
			if allow == "" {
				return "", "", false
			}
			return "018f3b2c-7a10-7c4a-8e11-111111111111", allow, true
		},
	}.MountRoutes(mux, nil)
	return mux
}
