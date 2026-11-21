//go:build integration

package brain

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/chat"
	chatapp "github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// TestScoutChatReadsASharedChatForItsSharedPeopleOnly runs the scout adapter
// against real shares: an editor and a viewer read the chat, a stranger gets
// an error and no lines.
func TestScoutChatReadsASharedChatForItsSharedPeopleOnly(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	// The chat store records audit events, so the audit schema comes up first.
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	members := identity.Members(pool, nil, "", nil, nil)
	store := chat.NewStore(pool, nil)

	person := func(name, role string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			SELECT lower($1::text) || '@example.com', sha256(convert_to(lower($1::text) || '@example.com', 'UTF8')), 'k1', $1::text, role.id, now()
			FROM identity.roles AS role
			WHERE role.key = $2::text
			RETURNING id`, name, role).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	ada, bob, cy, dee := person("Ada", "owner"), person("Bob", "member"), person("Cy", "member"), person("Dee", "member")

	adaAccess, ok, err := chatAccess(ctx, members, identity.UserID{UUID: ada})
	if err != nil || !ok {
		t.Fatalf("ada access = %v %v", ok, err)
	}
	opened, err := chatapp.Open(ctx, store, adaAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := chatapp.Say(ctx, store, adaAccess, opened.ID, "hello", chatapp.Picks{}); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []struct {
		user  uuid.UUID
		level string
	}{{bob, "editor"}, {dee, "viewer"}} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
			VALUES ('chat', $1::uuid, $2, $3, $4)`, opened.ID, grant.user, grant.level, ada); err != nil {
			t.Fatal(err)
		}
	}
	bobAccess, ok, err := chatAccess(ctx, members, identity.UserID{UUID: bob})
	if err != nil || !ok {
		t.Fatalf("bob access = %v %v", ok, err)
	}
	if err := chatapp.Say(ctx, store, bobAccess, opened.ID, "hi there", chatapp.Picks{}); err != nil {
		t.Fatal(err)
	}

	scout := scoutChat{store: store, members: members}
	want := []string{"hello", "hi there"}
	for name, user := range map[string]uuid.UUID{"editor": bob, "viewer": dee, "owner": ada} {
		got, read, err := scout.RecentLines(ctx, user, opened.ID, 10)
		if err != nil || !read || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s lines = %v %v %v", name, got, read, err)
		}
	}
	got, read, err := scout.RecentLines(ctx, cy, opened.ID, 10)
	if err == nil || read || len(got) != 0 {
		t.Fatalf("stranger lines = %v %v %v", got, read, err)
	}
}
