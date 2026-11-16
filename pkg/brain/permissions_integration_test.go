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

func TestRoutePermissionRefusal_UPM243_KeepsSession(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.New(t)
	// Session writes record their audit events in the same transaction, so the
	// audit schema migrates with identity here.
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	var ownerID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		SELECT 'owner@example.com', 'Owner', id, now() FROM identity.roles WHERE key = 'owner'
		RETURNING id::text`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	var memberID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.roles (name, permissions) VALUES ('No permissions', ARRAY[]::text[])
		RETURNING id::text`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('member@example.com', 'Member', $1::uuid, now()) RETURNING id::text`, memberID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	uid, err := identity.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{SessionIdle: identity.IdleTimeout, SessionMaxAge: identity.MaxAge, PublicURL: "http://localhost:5173", WebAuthnRPID: "localhost"}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	token, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	work, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, cookie bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "__Host-upmo_session", Value: token, Path: "/", Secure: true, HttpOnly: true})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/api/tasks", "/api/chats"} {
		if got := call(path, true).Code; got != http.StatusForbidden {
			t.Errorf("signed-in GET %s = %d, want 403", path, got)
		}
		if got := call(path, false).Code; got != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d, want 401", path, got)
		}
	}
	if got := call("/api/session", true).Code; got != http.StatusOK {
		t.Fatalf("session after permission refusals = %d, want 200", got)
	}
}
