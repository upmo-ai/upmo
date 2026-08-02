//go:build integration

package identity_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestEmailUniqueIgnoringCase(t *testing.T) {
	pool, ctx := up(t)
	seedOwner(t, pool, ctx, "ada@example.com")
	_, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role, joined_at) VALUES ('Ada@example.com', 'Other', 'member', now())`)
	if err == nil {
		t.Fatal("email with a different case was accepted")
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.users (email, name, external_id, role, joined_at) VALUES ('a@example.com', 'A', 'ext', 'member', now())`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.users (email, name, external_id, role, joined_at) VALUES ('b@example.com', 'B', 'ext', 'member', now())`)
	if err == nil {
		t.Fatal("duplicate external_id was accepted")
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.users (email, name, role, joined_at) VALUES ('c@example.com', 'C', 'member', now()), ('d@example.com', 'D', 'member', now())`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLastOwnerCannotBeDemoted(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role = 'admin' WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("demoting the only joined owner committed")
	}
}

func TestLastOwnerCannotBeRemoved(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET joined_at = NULL WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("removing the only joined owner committed")
	}
}

func TestDeletingTheOnlyOwnerFails(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role, joined_at) VALUES ('lee@example.com', 'Lee', 'member', now())`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity.users WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("deleting the only joined owner committed")
	}
}

func TestOwnershipCanBeHandedOver(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	var next string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.users (email, name, role, joined_at) VALUES ('lee@example.com', 'Lee', 'member', now()) RETURNING id`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role = 'member' WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role = 'owner' WHERE id = $1`, next); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	seedOwner(t, pool, ctx, "noor@example.com")
	var promoted string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.users (email, name, role, joined_at) VALUES ('sam@example.com', 'Sam', 'admin', now()) RETURNING id`).Scan(&promoted); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role = 'owner' WHERE id = $1`, promoted); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role = 'admin' WHERE role = 'owner' AND id <> $1`, promoted); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPendingOwnerDoesNotCount(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role, invited_at) VALUES ('lee@example.com', 'Lee', 'owner', now())`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role = 'member' WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("a pending owner kept the install after the joined owner was demoted")
	}
}

func TestTeamMemberMustHaveJoined(t *testing.T) {
	pool, ctx := up(t)
	seedOwner(t, pool, ctx, "ada@example.com")
	var team string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Ship') RETURNING id`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO identity.teams (name) VALUES ('ship')`)
	if err == nil {
		t.Fatal("team name with a different case was accepted")
	}
	var outsider string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.users (email, name, role, invited_at) VALUES ('lee@example.com', 'Lee', 'member', now()) RETURNING id`).Scan(&outsider); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)`, team, outsider)
	if err == nil {
		t.Fatal("a person who has not joined was added to the team")
	}
}

func TestOneGrantPerGranteePerObject(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	var team string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Ship') RETURNING id`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	object := "00000000-0000-7000-8000-000000000001"
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', $1, $2, 'editor', $2)`, object, owner); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', $1, $2, 'viewer', $2)`, object, owner)
	if err == nil {
		t.Fatal("a second grant for the same user was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, team_id, level, granted_by)
		VALUES ('task', $1, $2, 'viewer', $3)`, object, team, owner); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, team_id, level, granted_by)
		VALUES ('task', $1, $2, 'editor', $3)`, object, team, owner)
	if err == nil {
		t.Fatal("a second grant for the same team was accepted")
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, level, granted_by)
		VALUES ('task', $1, 'viewer', $2)`, object, owner)
	if err == nil {
		t.Fatal("a share with no grantee was accepted")
	}
}

func TestPrincipalShapes(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO identity.principals (kind, user_id) VALUES ('user', $1)`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.principals (kind, agent_id) VALUES ('agent', uuidv7())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.principals (kind, run_id, user_id) VALUES ('run', uuidv7(), $1)`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.principals (kind, user_id, agent_id) VALUES ('user', $1, uuidv7())`, owner); err == nil {
		t.Fatal("a user principal with an agent id was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.principals (kind, agent_id) VALUES ('run', uuidv7())`); err == nil {
		t.Fatal("a run principal without a user id was accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.principals (kind) VALUES ('service')`); err == nil {
		t.Fatal("an unknown principal kind was accepted")
	}
}

func TestUnknownRoleAndLevelRejected(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role, joined_at) VALUES ('lee@example.com', 'Lee', 'boss', now())`); err == nil {
		t.Fatal("role boss was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', uuidv7(), $1, 'author', $1)`, owner); err == nil {
		t.Fatal("level author was accepted")
	}
}

func TestOwnerIsNotAShareLevel(t *testing.T) {
	pool, ctx := up(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	_, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', uuidv7(), $1, 'owner', $1)`, owner)
	if err == nil {
		t.Fatal("share level owner was accepted")
	}
}

func TestDownRemovesTheIdentitySchema(t *testing.T) {
	pool, ctx := up(t)
	if err := migrate.Down(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	var schemas int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'identity'`).Scan(&schemas); err != nil {
		t.Fatal(err)
	}
	if schemas != 0 {
		t.Fatal("identity schema remains after down")
	}
}

func TestSchemaChecksMatchTheDomain(t *testing.T) {
	pool, ctx := up(t)
	rows, err := pool.Query(ctx, `
		SELECT c.conname, pg_get_constraintdef(c.oid)
		FROM pg_constraint AS c
		JOIN pg_namespace AS n ON n.oid = c.connamespace
		WHERE n.nspname = 'identity' AND c.contype = 'c'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	checks := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatal(err)
		}
		checks[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	assertQuoted(t, checks, "users_role_check", values(domain.Roles()))
	assertQuoted(t, checks, "shares_level_check", values(domain.Levels()))
	assertQuoted(t, checks, "principals_kind_check", values(domain.Kinds()))
	if strings.Contains(checks["shares_level_check"], "'owner'") {
		t.Fatalf("share level check includes owner: %s", checks["shares_level_check"])
	}

	var workspaces int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'identity' AND table_name = 'workspaces'`).Scan(&workspaces); err != nil {
		t.Fatal(err)
	}
	if workspaces != 0 {
		t.Fatal("identity.workspaces exists")
	}

	var crossing int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_constraint AS c
		JOIN pg_namespace AS n ON n.oid = c.connamespace
		JOIN pg_class AS foreign_table ON foreign_table.oid = c.confrelid
		JOIN pg_namespace AS foreign_ns ON foreign_ns.oid = foreign_table.relnamespace
		WHERE c.contype = 'f' AND n.nspname = 'identity' AND foreign_ns.nspname <> 'identity'`).Scan(&crossing); err != nil {
		t.Fatal(err)
	}
	if crossing != 0 {
		t.Fatalf("%d foreign keys leave the identity schema", crossing)
	}
}

func up(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func seedOwner(t *testing.T, pool *pgxpool.Pool, ctx context.Context, email string) string {
	t.Helper()
	var owner string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role, joined_at)
		VALUES ($1, 'Ada', 'owner', now()) RETURNING id`, email).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	return owner
}

func values[T ~string](items []T) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = string(item)
	}
	return out
}

func assertQuoted(t *testing.T, checks map[string]string, name string, want []string) {
	t.Helper()
	def, ok := checks[name]
	if !ok {
		t.Fatalf("missing check %s", name)
	}
	got := quoted(def)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s values = %v, want %v (%s)", name, got, want, def)
	}
}

func quoted(def string) []string {
	var out []string
	rest := def
	for {
		start := strings.Index(rest, "'")
		if start < 0 {
			return out
		}
		rest = rest[start+1:]
		end := strings.Index(rest, "'")
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		rest = rest[end+1:]
	}
}
