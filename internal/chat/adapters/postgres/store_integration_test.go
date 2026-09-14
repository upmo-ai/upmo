//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	ownerUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser = "018f3b2c-7a10-7c4a-8e11-222222222222"
)

var (
	ownerAccess = app.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit"}
	otherAccess = app.Access{UserID: otherUser, Allow: "chat:view,chat:edit"}
)

func TestOpenKeepsTheProfileAndLeavesTheSession(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	id, err := app.Open(ctx, store, ownerAccess, " See {{prior}} ")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, id, " hello "); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, id, "again"); err != nil {
		t.Fatal(err)
	}

	got, err := app.Read(ctx, store, ownerAccess, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "See { {prior}}" || got.Block != domain.Block(got.Profile) {
		t.Fatalf("profile = %q block = %q", got.Profile, got.Block)
	}
	if len(got.Lines) != 2 || got.Lines[0] != "hello" || got.Lines[1] != "again" {
		t.Fatalf("lines = %#v", got.Lines)
	}

	var sessions int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM identity.sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions = %d", sessions)
	}
	if _, err := app.Read(ctx, store, otherAccess, id); !errors.Is(err, app.ErrGone) {
		t.Fatalf("other person error = %v", err)
	}

	_, err = owner.Exec(ctx, `UPDATE chat.chats SET profile = 'changed' WHERE id = $1`, id)
	if err == nil || !strings.Contains(err.Error(), "chat: profile") {
		t.Fatalf("profile rewrite error = %v", err)
	}
	_, err = owner.Exec(ctx, `UPDATE chat.lines SET body = 'changed' WHERE chat_id = $1`, id)
	if err == nil || !strings.Contains(err.Error(), "chat: writer") {
		t.Fatalf("line rewrite error = %v", err)
	}
}

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}
