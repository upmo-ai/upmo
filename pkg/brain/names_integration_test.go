//go:build integration

package brain

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
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
	task := randomID(t, owner)
	if _, err := grants.TaskGrants(live, task); err != nil {
		t.Fatalf("taskGrants.TaskGrants live = %v, want nil", err)
	}
	if rows, err := grants.TaskGrants(ctx, task); !errors.Is(err, identity.ErrNotSignedIn) || rows != nil {
		t.Fatalf("taskGrants.TaskGrants no principal = %v %v, want ErrNotSignedIn", rows, err)
	}

	chats := chatGrants{members: members}
	if _, err := chats.ChatGrants(live, task); err != nil {
		t.Fatalf("chatGrants.ChatGrants live = %v, want nil", err)
	}
	for name, c := range map[string]context.Context{"no principal": ctx, "ended session": ended} {
		if rows, err := chats.ChatGrants(c, task); !errors.Is(err, identity.ErrNotSignedIn) || rows != nil {
			t.Fatalf("chatGrants.ChatGrants %s = %v %v, want ErrNotSignedIn", name, rows, err)
		}
	}
	if people, teams, err := chats.Names(ctx, []string{ada}, nil); !errors.Is(err, identity.ErrNotSignedIn) || people != nil || teams != nil {
		t.Fatalf("chatGrants.Names no principal = %v %v %v, want ErrNotSignedIn", people, teams, err)
	}
}

func randomID(t *testing.T, owner *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := owner.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A grant write under Ada's principal that names Bob as grantor, or one with
// no principal, is refused before any write: the grantor stored is always
// the principal.
func TestGrantAdapterRefusesAnotherActor(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)
	members := identity.Members(appPool, nil, "", nil, nil)
	person := func(name string) (string, context.Context) {
		t.Helper()
		var id string
		var hash []byte
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES (lower($1::text) || '@example.com', sha256(convert_to(lower($1::text) || '@example.com', 'UTF8')), 'k1', $1::text, (SELECT id FROM identity.roles WHERE key = 'owner'), now())
			RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
			VALUES ($1::uuid, decode(md5(random()::text) || md5(random()::text), 'hex'), now(), now(), now() + interval '1 hour')
			RETURNING token_hash`, id).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		userID, err := identity.ParseUserID(id)
		if err != nil {
			t.Fatal(err)
		}
		return id, identity.WithPrincipal(ctx, identity.Principal{UserID: userID, TokenHash: hash})
	}
	ada, asAda := person("Ada")
	bob, _ := person("Bob")
	shares := func() int {
		t.Helper()
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM identity.shares`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	task := randomID(t, owner)
	taskWrite := []tasks.ShareGrant{{SubjectType: "person", SubjectID: bob, Role: "editor"}}
	chatWrite := []chat.ShareGrant{{SubjectType: "person", SubjectID: bob, Role: "editor"}}
	inTx := func(write func(tx pgx.Tx) error) error {
		t.Helper()
		tx, err := appPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := write(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	for name, c := range map[string]struct {
		ctx context.Context
		by  string
	}{
		"another actor": {asAda, bob},
		"no principal":  {ctx, ada},
	} {
		if err := inTx(func(tx pgx.Tx) error {
			return taskGrants{members: members}.ReplaceTaskGrantsTx(c.ctx, tx, task, taskWrite, c.by)
		}); !errors.Is(err, identity.ErrNotSignedIn) {
			t.Fatalf("task %s: err = %v, want ErrNotSignedIn", name, err)
		}
		if err := inTx(func(tx pgx.Tx) error {
			return chatGrants{members: members}.ReplaceChatGrantsTx(c.ctx, tx, task, chatWrite, c.by)
		}); !errors.Is(err, identity.ErrNotSignedIn) {
			t.Fatalf("chat %s: err = %v, want ErrNotSignedIn", name, err)
		}
		if err := (taskGrants{members: members}).ReplaceTaskGrants(c.ctx, task, taskWrite, c.by); !errors.Is(err, identity.ErrNotSignedIn) {
			t.Fatalf("task own tx %s: err = %v, want ErrNotSignedIn", name, err)
		}
		if got := shares(); got != 0 {
			t.Fatalf("%s: shares = %d, want 0", name, got)
		}
	}

	// Control: Ada as her own grantor writes, and the row names her.
	if err := inTx(func(tx pgx.Tx) error {
		return taskGrants{members: members}.ReplaceTaskGrantsTx(asAda, tx, task, taskWrite, ada)
	}); err != nil {
		t.Fatal(err)
	}
	var grantedBy string
	if err := owner.QueryRow(ctx, `SELECT granted_by::text FROM identity.shares WHERE object_id = $1::uuid`, task).Scan(&grantedBy); err != nil {
		t.Fatal(err)
	}
	if grantedBy != ada {
		t.Fatalf("granted_by = %s, want Ada %s", grantedBy, ada)
	}
}
