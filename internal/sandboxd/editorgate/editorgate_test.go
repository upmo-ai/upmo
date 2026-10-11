package editorgate

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/editorjwt"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

type fakeBook struct {
	route     Route
	routeErr  error
	touchErr  error
	touchCall int
}

func (f *fakeBook) EditorRoute(context.Context, string) (Route, error) {
	return f.route, f.routeErr
}

func (f *fakeBook) TouchEditor(context.Context, string) error {
	f.touchCall++
	return f.touchErr
}

func testKeys(t *testing.T) editorjwt.Keys {
	t.Helper()
	master, err := editorjwt.ParseKey(svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes32('e')))
	if err != nil {
		t.Fatal(err)
	}
	key, err := editorjwt.Derive(master)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := editorjwt.New([][]byte{key})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func signEditor(t *testing.T, keys editorjwt.Keys, user, task, session string, now time.Time) string {
	t.Helper()
	tok, err := keys.Sign(user, task, session, now)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestGateProxiesValidJWT(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tkn") != "opaque" {
			t.Fatalf("tkn = %q", r.URL.Query().Get("tkn"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("authorization forwarded")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	keys := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	user := uuid.NewString()
	task := uuid.NewString()
	session := uuid.NewString()
	book := &fakeBook{route: Route{Upstream: u, ConnToken: "opaque", TaskID: task}}
	token := signEditor(t, keys, user, task, session, now)
	req := httptest.NewRequest(http.MethodGet, "/e/sandbox-1/ws", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	Handler{Book: book, Keys: keys, Now: func() time.Time { return now }}.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
	}
	if book.touchCall != 1 {
		t.Fatalf("touch calls = %d", book.touchCall)
	}
}

func TestGateUnauthorized(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:9")
	book := &fakeBook{route: Route{Upstream: u, ConnToken: "x"}}
	keys := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	user := uuid.NewString()
	task := uuid.NewString()
	session := uuid.NewString()
	token := signEditor(t, keys, user, task, session, now)
	cases := []struct {
		name string
		req  *http.Request
	}{
		{"missing", httptest.NewRequest(http.MethodGet, "/e/sandbox-1/", nil)},
		{"bad", httptest.NewRequest(http.MethodGet, "/e/sandbox-1/?token=nope", nil)},
		{"expired", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/e/sandbox-1/", nil)
			r.Header.Set("Authorization", "Bearer "+token)
			return r
		}()},
	}
	h := Handler{Book: book, Keys: keys, Now: func() time.Time { return now.Add(domain.EditorIdle + time.Minute) }}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, c.req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}

func TestGateTaskMismatch(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:9")
	book := &fakeBook{route: Route{Upstream: u, ConnToken: "x", TaskID: uuid.NewString()}}
	keys := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	task := uuid.NewString()
	token := signEditor(t, keys, uuid.NewString(), task, uuid.NewString(), now)
	req := httptest.NewRequest(http.MethodGet, "/e/sb/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	Handler{Book: book, Keys: keys, Now: func() time.Time { return now }}.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestPublicURL(t *testing.T) {
	if got := PublicURL("http://127.0.0.1:8084/", "abc"); got != "http://127.0.0.1:8084/e/abc/" {
		t.Fatalf("got %q", got)
	}
}

func TestKeysFromService(t *testing.T) {
	secret := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes32('b'))
	if _, err := KeysFromService(secret, ""); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBaseURL(t *testing.T) {
	if _, err := ValidateBaseURL("https://x"); err == nil {
		t.Fatal("expected error")
	}
	got, err := ValidateBaseURL("")
	if err != nil || got != DefaultBaseURL {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestParsePathRedirect(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:9")
	book := &fakeBook{route: Route{Upstream: u, ConnToken: "x"}}
	keys := testKeys(t)
	now := time.Unix(1_700_000_000, 0)
	token := signEditor(t, keys, uuid.NewString(), uuid.NewString(), uuid.NewString(), now)
	req := httptest.NewRequest(http.MethodGet, "/e/sandbox-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	Handler{Book: book, Keys: keys, Now: func() time.Time { return now }}.ServeHTTP(rec, req)
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestServiceMasterMatchesBrain(t *testing.T) {
	secret := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes32('d'))
	master, err := svcsig.ParseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	sandboxdKey, err := svcsig.Derive(master, svcsig.AudienceSandboxd)
	if err != nil {
		t.Fatal(err)
	}
	_ = sandboxdKey
	gateKeys, err := KeysFromService(secret, "")
	if err != nil {
		t.Fatal(err)
	}
	editorKeys, err := editorjwt.New([][]byte{mustEditorDerive(t, master)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	user, task, session := uuid.NewString(), uuid.NewString(), uuid.NewString()
	tok, err := editorKeys.Sign(user, task, session, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateKeys.Verify(tok, now); err != nil {
		t.Fatal(err)
	}
}

func mustEditorDerive(t *testing.T, master []byte) []byte {
	t.Helper()
	k, err := editorjwt.Derive(master)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}
