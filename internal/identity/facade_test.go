package identity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
)

func TestSessionSeamContext(t *testing.T) {
	parsed, err := identity.ParseUserID("00000000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	principal := identity.Principal{
		ID:        "session-1",
		UserID:    parsed,
		SessionID: "session-1",
		Method:    "passkey",
		SignedIn:  now,
	}

	ctx := identity.WithPrincipal(context.Background(), principal)
	got, ok := identity.SessionFrom(ctx)
	if !ok {
		t.Fatal("expected principal from context")
	}
	if got.UserID != parsed || got.SessionID != "session-1" || got.Method != "passkey" || !got.SignedIn.Equal(now) {
		t.Fatalf("unexpected principal: %+v", got)
	}
	if got.User() != parsed {
		t.Fatalf("User() = %v, want %v", got.User(), parsed)
	}
}

func TestRequireSessionRefusesUnauthenticated(t *testing.T) {
	id := identity.Identity{}
	handler := id.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec.Body.String() != "{\"error\":\"unauthorized\"}\n" {
		t.Fatalf("body = %q, want unauthorized", rec.Body.String())
	}
}
