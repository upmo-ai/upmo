//go:build integration

package identity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestRecheckFollowsTheSessionWhileARequestStaysOpen(t *testing.T) {
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
	err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('ada@example.com', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&user)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := domain.ParseUserID(user)
	if err != nil {
		t.Fatal(err)
	}
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	text, session, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/x/stream", nil)
	req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: text})

	principal, ok, err := identity.Recheck(ctx, sessions, req)
	if err != nil || !ok || principal.UserID != uid || principal.SessionID != session.ID {
		t.Fatalf("live session = %+v %v %v", principal, ok, err)
	}
	if _, ok, err := identity.Recheck(ctx, sessions, httptest.NewRequest(http.MethodGet, "/", nil)); ok || err != nil {
		t.Fatalf("no cookie = %v %v", ok, err)
	}

	if err := sessions.End(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := identity.Recheck(ctx, sessions, req); ok || err != nil {
		t.Fatalf("ended session = %v %v, want false and no error", ok, err)
	}
}
