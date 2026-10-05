//go:build integration

package notices_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestStoreInsertListRLS(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	migrateIdentity(t, owner)
	if err := notices.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	store := notices.Store{Pool: pool}

	userA := insertUser(t, owner, "ada@example.com")
	userB := insertUser(t, owner, "sam@example.com")

	id, err := store.Insert(ctx, userA, notices.KindProposal, "Weekly close", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id == uuid.Nil {
		t.Fatal("expected id")
	}

	rows, err := store.List(ctx, userA)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Kind != notices.KindProposal || rows[0].Title != "Weekly close" {
		t.Fatalf("user A: %#v", rows)
	}

	other, err := store.List(ctx, userB)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("user B: %#v", other)
	}
}

func TestHTTPNotificationsSession(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	migrateIdentity(t, owner)
	if err := notices.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	userID := insertUser(t, owner, "ada@example.com")
	store := notices.Store{Pool: pool}
	if _, err := store.Insert(ctx, userID, notices.KindProposal, "Mail triage", nil); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passwords := identity.Passwords(pool, sessions)
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		t.Fatal(err)
	}
	id := identity.New(sessions, passkeys, passwords)
	uid, err := identity.ParseUserID(userID.String())
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	notices.Handler{
		List: store,
		User: func(r *http.Request) (uuid.UUID, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return uuid.Nil, false
			}
			return person.UserID.UUID, true
		},
	}.Mount(mux, id.RequireSession)

	anon := httptest.NewRecorder()
	mux.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/api/notifications", nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", anon.Code)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
	req.AddCookie(&http.Cookie{
		Name:     "__Host-upmo_session",
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
	})
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("with session = %d, want 200", rec.Code)
	}
	var body []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 || body[0]["title"] != "Mail triage" {
		t.Fatalf("body = %s err %v", rec.Body.String(), err)
	}
}

func migrateIdentity(t *testing.T, owner *pgxpool.Pool) {
	t.Helper()
	if err := migrate.Up(context.Background(), owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
}

func insertUser(t *testing.T, owner *pgxpool.Pool, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := owner.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, $2, (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id`, email, email).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
