//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestSessions(t *testing.T) {
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	ada := user(t, ctx, pool, "ada@example.com")
	lee := user(t, ctx, pool, "lee@example.com")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	svc := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	svc.Now = func() time.Time { return now }

	adaText, adaSession, err := svc.Start(ctx, ada, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	leeText, _, err := svc.Start(ctx, lee, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM identity.sessions WHERE id = $1`, adaSession.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 32 {
		t.Fatalf("hash length = %d", len(hash))
	}
	var asToken domain.Token
	copy(asToken[:], hash)
	if _, err := svc.Lookup(ctx, domain.EncodeToken(asToken)); err != app.ErrNotFound {
		t.Fatalf("stored hash opened a session: %v", err)
	}
	got, err := svc.Lookup(ctx, adaText)
	if err != nil || got.UserID != ada {
		t.Fatalf("lookup = %+v %v", got, err)
	}
	if got, err := svc.Lookup(ctx, leeText); err != nil || got.UserID == ada {
		t.Fatalf("lee token resolved as ada: %+v %v", got, err)
	}

	if err := svc.End(ctx, adaSession.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Lookup(ctx, adaText); err != app.ErrNotFound {
		t.Fatalf("signed-out session: %v", err)
	}
	if _, err := svc.Lookup(ctx, leeText); err != nil {
		t.Fatalf("sign-out ended the other session: %v", err)
	}
	if err := svc.EndSessions(ctx, lee); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Lookup(ctx, leeText); err != app.ErrNotFound {
		t.Fatalf("EndSessions left a session: %v", err)
	}

	text, _, err := svc.Start(ctx, ada, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, ada); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE user_id = $1`, ada).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("disabled person still has %d sessions", left)
	}
	if _, err := svc.Lookup(ctx, text); err != app.ErrNotFound {
		t.Fatalf("disabled person resolved: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE identity.users SET disabled_at = NULL WHERE id = $1`, ada); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Start(ctx, ada, "passkey", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, ada); err != nil {
		t.Fatal(err)
	}
	token := mustToken(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $3, $4)`, ada, domain.Hash(token), now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Lookup(ctx, domain.EncodeToken(token)); err != app.ErrNotFound {
		t.Fatalf("disabled filter: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE identity.users SET disabled_at = NULL WHERE id = $1`, ada); err != nil {
		t.Fatal(err)
	}
	old := mustToken(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $3, $4)`, ada, domain.Hash(old), now.Add(-48*time.Hour), now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Start(ctx, ada, "passkey", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Lookup(ctx, domain.EncodeToken(old)); err != app.ErrNotFound {
		t.Fatalf("ended session survived a new start: %v", err)
	}
}

func user(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string) domain.UserID {
	t.Helper()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`, email).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func mustToken(t *testing.T) domain.Token {
	t.Helper()
	token, _, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}
