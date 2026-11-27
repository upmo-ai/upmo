//go:build integration

package brain

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// A names lookup with no principal, or with a principal whose session has
// ended, is an error: never an empty answer that reads as "unknown".
func TestBrainNamesFailClosed(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	members := identity.Members(pgtest.App(t, owner), nil, "", nil, nil)

	var ada string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&ada); err != nil {
		t.Fatal(err)
	}
	session := func(expires string) []byte {
		t.Helper()
		var hash []byte
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
			VALUES ($1::uuid, decode(md5(random()::text) || md5(random()::text), 'hex'), now(), now(), now() + $2::interval)
			RETURNING token_hash`, ada, expires).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	adaID, err := identity.ParseUserID(ada)
	if err != nil {
		t.Fatal(err)
	}
	as := func(hash []byte) context.Context {
		return identity.WithPrincipal(ctx, identity.Principal{UserID: adaID, TokenHash: hash})
	}
	live, ended := as(session("1 hour")), as(session("-1 minute"))
	exists := personExists(members)

	// Control: a live session finds Ada.
	if ok, err := exists(live, ada); err != nil || !ok {
		t.Fatalf("personExists live = %v %v, want true", ok, err)
	}
	for name, c := range map[string]context.Context{"no principal": ctx, "ended session": ended} {
		ok, err := exists(c, ada)
		if !errors.Is(err, identity.ErrNotSignedIn) || ok {
			t.Fatalf("personExists %s = %v %v, want ErrNotSignedIn", name, ok, err)
		}
	}

	grants := taskGrants{members: members}
	people, _, err := grants.Names(live, []string{ada}, nil)
	if err != nil || people[ada] != "Ada" {
		t.Fatalf("taskGrants.Names live = %v %v, want Ada", people, err)
	}
	if people, teams, err := grants.Names(ctx, []string{ada}, nil); !errors.Is(err, identity.ErrNotSignedIn) || people != nil || teams != nil {
		t.Fatalf("taskGrants.Names no principal = %v %v %v, want ErrNotSignedIn", people, teams, err)
	}
}
