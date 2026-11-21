//go:build integration

package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestSessionHTTP(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	var user string
	err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&user)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := domain.ParseUserID(user)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	svc := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	svc.Now = func() time.Time { return now }
	text, _, err := svc.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.Handler{Sessions: svc, Log: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	mux := http.NewServeMux()
	h.Mount(mux)
	// The brain mount: the audit middleware outside the cross-origin guard, in
	// a server that expects the handler to carry its own guard, so a refused
	// request is still recorded.
	served := audit.Middleware(audit.Routes(), mux, pool, "brain", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))(httpserver.Protect(mux))
	protected := httpserver.NewGuarded("127.0.0.1:0", served).Handler

	t.Run("missing", func(t *testing.T) {
		rec := call(protected, http.MethodGet, "/api/session", "")
		assertUnauthorised(t, rec, false)
	})
	t.Run("malformed", func(t *testing.T) {
		rec := call(protected, http.MethodGet, "/api/session", "nope")
		assertUnauthorised(t, rec, true)
	})
	t.Run("unknown", func(t *testing.T) {
		other, _, err := domain.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		rec := call(protected, http.MethodGet, "/api/session", domain.EncodeToken(other))
		assertUnauthorised(t, rec, true)
	})
	t.Run("current", func(t *testing.T) {
		rec := call(protected, http.MethodGet, "/api/session", text)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("cache = %q", rec.Header().Get("Cache-Control"))
		}
		if rec.Body.String() != `{"has_password":false,"sign_in_method":"passkey","signed_in_at":"2026-08-05T12:00:00Z","user_id":"`+user+`"}`+"\n" {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})

	idle := svc
	idle.Now = func() time.Time { return now.Add(domain.IdleTimeout) }
	idleMux := http.NewServeMux()
	httpapi.Handler{Sessions: idle, Log: h.Log}.Mount(idleMux)
	t.Run("idle", func(t *testing.T) {
		rec := call(idleMux, http.MethodGet, "/api/session", text)
		assertUnauthorised(t, rec, true)
	})

	fresh, _, err := svc.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	absolute := svc
	absolute.Now = func() time.Time { return now.Add(domain.MaxAge) }
	absMux := http.NewServeMux()
	httpapi.Handler{Sessions: absolute, Log: h.Log}.Mount(absMux)
	t.Run("expired", func(t *testing.T) {
		rec := call(absMux, http.MethodGet, "/api/session", fresh)
		assertUnauthorised(t, rec, true)
	})

	t.Run("cross-site delete", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example")
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: fresh})
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

func call(h http.Handler, method, path, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertUnauthorised(t *testing.T, rec *httptest.ResponseRecorder, cleared bool) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != "{\"error\":\"unauthorised\"}\n" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache = %q", rec.Header().Get("Cache-Control"))
	}
	if !cleared {
		return
	}
	res := rec.Result()
	cookies := res.Cookies()
	if len(cookies) != 1 || cookies[0].Name != httpapi.CookieName || cookies[0].MaxAge >= 0 {
		t.Fatalf("clear cookie = %+v", cookies)
	}
}
