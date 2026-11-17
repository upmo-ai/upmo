//go:build integration

package brain

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// securityHeadersHandler is the real mounted brain handler plus one stream
// route that sets its own content type and cache policy.
func securityHeadersHandler(t *testing.T) http.Handler {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	work, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	stream := WithExtraRoutes(func(mux *http.ServeMux, _ Deps) {
		mux.HandleFunc("GET /api/test-stream", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache, no-store")
			_, _ = w.Write([]byte(": ok\n\n"))
		})
	})
	handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil, stream)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func assertAPIHeaders(t *testing.T, label string, h http.Header) {
	t.Helper()
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=(), payment=()",
	}
	for name, value := range want {
		if got := h.Get(name); got != value {
			t.Errorf("%s: %s = %q, want %q", label, name, got, value)
		}
	}
}

func TestSecurityHeadersOnBrainHandler(t *testing.T) {
	handler := securityHeadersHandler(t)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assertAPIHeaders(t, "healthz", health.Header())
	if got := health.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain healthz HSTS = %q, want none", got)
	}
	if got := health.Header().Get("Cache-Control"); got != "" {
		t.Errorf("healthz Cache-Control = %q, want none", got)
	}

	session := httptest.NewRecorder()
	handler.ServeHTTP(session, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if session.Code != http.StatusUnauthorized {
		t.Fatalf("session = %d, want 401", session.Code)
	}
	assertAPIHeaders(t, "session", session.Header())
	if got := session.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain session HSTS = %q, want none", got)
	}
	if got := session.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("session Cache-Control = %q, want no-store", got)
	}

	events := httptest.NewRecorder()
	handler.ServeHTTP(events, httptest.NewRequest(http.MethodGet, "/api/test-stream", nil))
	assertAPIHeaders(t, "stream", events.Header())
	if got := events.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("stream Content-Type = %q", got)
	}
	if got := events.Header().Get("Cache-Control"); got != "no-cache, no-store" {
		t.Errorf("stream Cache-Control = %q", got)
	}
}

func TestSecurityHeadersHSTSOverTLS(t *testing.T) {
	srv := httptest.NewTLSServer(securityHeadersHandler(t))
	defer srv.Close()

	res, err := srv.Client().Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	assertAPIHeaders(t, "tls healthz", res.Header)
	if got := res.Header.Get("Strict-Transport-Security"); got != "max-age=15552000" {
		t.Errorf("TLS HSTS = %q, want max-age=15552000", got)
	}
}
