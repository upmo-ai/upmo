package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

const pageUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

func TestPersonalisationPage(t *testing.T) {
	at := time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC)
	store := &fakeEditor{page: app.Page{
		Profile:   "Prefers tea.",
		ProfileAt: at,
		Facts: []app.FactRow{{
			ID: "018f3b2c-7a10-7c4a-8e11-222222222222", Claim: "Ada prefers tea", Trust: "user", Confirmed: at,
		}},
	}}
	h := handler(store, pageUser)
	mux := http.NewServeMux()
	h.Mount(mux, nil)

	got := serve(t, mux, http.MethodGet, "/api/personalization", "")
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"profile":"Prefers tea."`) {
		t.Fatalf("get %d %s", got.Code, got.Body.String())
	}
	if cache := got.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("cache %q", cache)
	}
	if !strings.Contains(got.Body.String(), `"facts":[{`) || strings.Contains(got.Body.String(), `"facts":null`) {
		t.Fatalf("facts %s", got.Body.String())
	}

	saved := serve(t, mux, http.MethodPut, "/api/personalization/profile", `{"body":" Prefers coffee. "}`)
	if saved.Code != http.StatusOK || store.profile != "Prefers coffee." {
		t.Fatalf("save %d profile=%q body=%s", saved.Code, store.profile, saved.Body.String())
	}

	added := serve(t, mux, http.MethodPost, "/api/personalization/facts", `{"claim":"Ada prefers coffee"}`)
	if added.Code != http.StatusCreated || store.claim != "Ada prefers coffee" || !strings.Contains(added.Body.String(), store.id) {
		t.Fatalf("add %d %s", added.Code, added.Body.String())
	}

	edited := serve(t, mux, http.MethodPut, "/api/personalization/facts/"+store.id, `{"claim":"Ada prefers water"}`)
	if edited.Code != http.StatusOK || store.edited != "Ada prefers water" {
		t.Fatalf("edit %d %s", edited.Code, edited.Body.String())
	}

	gone := serve(t, mux, http.MethodDelete, "/api/personalization/facts/"+store.id, "")
	if gone.Code != http.StatusNoContent || store.forgotten != store.id {
		t.Fatalf("forget %d id=%s", gone.Code, store.forgotten)
	}
}

func TestPersonalisationRefuses(t *testing.T) {
	store := &fakeEditor{}
	mux := http.NewServeMux()
	handler(store, "").Mount(mux, nil)
	if got := serve(t, mux, http.MethodGet, "/api/personalization", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("signed out %d", got.Code)
	}

	mux = http.NewServeMux()
	handler(store, pageUser).Mount(mux, nil)
	token := httptest.NewRequest(http.MethodGet, "/api/personalization", nil)
	token.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, token)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "needs a signed-in person") {
		t.Fatalf("bearer %d %s", rec.Code, rec.Body.String())
	}

	cases := []struct {
		method string
		path   string
		body   string
		err    error
		status int
		text   string
	}{
		{http.MethodPut, "/api/personalization/profile", `{"body":"too long"}`, app.ErrProfile, http.StatusBadRequest, "the profile is too long"},
		{http.MethodPost, "/api/personalization/facts", `{"claim":"   "}`, app.ErrClaim, http.StatusBadRequest, "a fact needs words"},
		{http.MethodPost, "/api/personalization/facts", `{"claim":"Ada prefers tea"}`, app.ErrDuplicate, http.StatusConflict, "already remembered"},
		{http.MethodDelete, "/api/personalization/facts/018f3b2c-7a10-7c4a-8e11-222222222222", "", app.ErrGone, http.StatusNotFound, "no such fact"},
		{http.MethodPut, "/api/personalization/profile", `{`, nil, http.StatusBadRequest, "the profile needs a body"},
		{http.MethodPost, "/api/personalization/facts", `{}`, nil, http.StatusBadRequest, "a fact needs words"},
	}
	for _, tc := range cases {
		store.err = tc.err
		got := serve(t, mux, tc.method, tc.path, tc.body)
		if got.Code != tc.status || !strings.Contains(got.Body.String(), tc.text) {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, got.Code, got.Body.String())
		}
	}
}

func handler(store *fakeEditor, user string) Handler {
	return Handler{
		Editor: store,
		Now:    func() time.Time { return time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC) },
		User: func(*http.Request) (string, bool) {
			return user, user != ""
		},
	}
}

func serve(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

type fakeEditor struct {
	page      app.Page
	err       error
	profile   string
	claim     string
	edited    string
	id        string
	forgotten string
}

func (f *fakeEditor) InPerson(ctx context.Context, userID string, fn func(app.Person) error) error {
	if f.err != nil {
		return f.err
	}
	return fn(fakePerson{f})
}

type fakePerson struct{ f *fakeEditor }

func (p fakePerson) WriteProfile(ctx context.Context, body string, at time.Time) error {
	p.f.profile = body
	return nil
}

func (p fakePerson) ReadPage(ctx context.Context) (app.Page, error) {
	return p.f.page, nil
}

func (p fakePerson) InsertUser(ctx context.Context, claim string, hash [32]byte, at time.Time) (string, error) {
	p.f.claim = claim
	p.f.id = "018f3b2c-7a10-7c4a-8e11-333333333333"
	return p.f.id, nil
}

func (p fakePerson) Tombstone(ctx context.Context, id string, at time.Time) error {
	p.f.forgotten = id
	return nil
}

func (p fakePerson) Supersede(ctx context.Context, id, claim string, hash [32]byte, at time.Time) (string, error) {
	p.f.edited = claim
	return id, nil
}

var _ app.Editor = (*fakeEditor)(nil)
var _ app.Person = fakePerson{}
