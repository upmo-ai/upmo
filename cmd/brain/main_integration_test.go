//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestReadyAndSession(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{SessionIdle: domain.IdleTimeout, SessionMaxAge: domain.MaxAge}
	handler := httpserver.New("127.0.0.1:0", routes(pool, cfg, log)).Handler

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || ready.Body.String() != "ok" {
		t.Fatalf("readyz = %d %q", ready.Code, ready.Body.String())
	}

	session := httptest.NewRecorder()
	handler.ServeHTTP(session, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if session.Code != http.StatusUnauthorized {
		t.Fatalf("session = %d, want 401", session.Code)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	cross := httptest.NewRecorder()
	handler.ServeHTTP(cross, req)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-site delete = %d, want 403", cross.Code)
	}
}
