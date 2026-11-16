//go:build integration

package identity_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestEmailUniqueIgnoringCase(t *testing.T) {
	pool, ctx := upOwner(t)
	seedOwner(t, pool, ctx, "ada@example.com")
	_, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('Ada@example.com', 'Other', (SELECT id FROM identity.roles WHERE key = 'member'), now())`)
	if err == nil {
		t.Fatal("email with a different case was accepted")
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.users (email, name, external_id, role_id, joined_at) VALUES ('a@example.com', 'A', 'ext', (SELECT id FROM identity.roles WHERE key = 'member'), now())`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.users (email, name, external_id, role_id, joined_at) VALUES ('b@example.com', 'B', 'ext', (SELECT id FROM identity.roles WHERE key = 'member'), now())`)
	if err == nil {
		t.Fatal("duplicate external_id was accepted")
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('c@example.com', 'C', (SELECT id FROM identity.roles WHERE key = 'member'), now()), ('d@example.com', 'D', (SELECT id FROM identity.roles WHERE key = 'member'), now())`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLastOwnerCannotBeDemoted(t *testing.T) {
	pool, ctx := upOwner(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'admin') WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("demoting the only joined owner committed")
	}
}

func TestLastOwnerCannotBeRemoved(t *testing.T) {
	pool, ctx := upOwner(t)
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
	pool, ctx := upOwner(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('lee@example.com', 'Lee', (SELECT id FROM identity.roles WHERE key = 'member'), now())`); err != nil {
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
	pool, ctx := upOwner(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	var next string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('lee@example.com', 'Lee', (SELECT id FROM identity.roles WHERE key = 'member'), now()) RETURNING id`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'member') WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'owner') WHERE id = $1`, next); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	seedOwner(t, pool, ctx, "noor@example.com")
	var promoted string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('sam@example.com', 'Sam', (SELECT id FROM identity.roles WHERE key = 'admin'), now()) RETURNING id`).Scan(&promoted); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'owner') WHERE id = $1`, promoted); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'admin') WHERE role_id = (SELECT id FROM identity.roles WHERE key = 'owner') AND id <> $1`, promoted); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPendingOwnerDoesNotCount(t *testing.T) {
	pool, ctx := upOwner(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role_id, invited_at) VALUES ('lee@example.com', 'Lee', (SELECT id FROM identity.roles WHERE key = 'owner'), now())`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'member') WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("a pending owner kept the install after the joined owner was demoted")
	}
}

func TestTeamMemberMustHaveJoined(t *testing.T) {
	pool, ctx := upOwner(t)
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
	if err := pool.QueryRow(ctx, `INSERT INTO identity.users (email, name, role_id, invited_at) VALUES ('lee@example.com', 'Lee', (SELECT id FROM identity.roles WHERE key = 'member'), now()) RETURNING id`).Scan(&outsider); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)`, team, outsider)
	if err == nil {
		t.Fatal("a person who has not joined was added to the team")
	}
}

func TestOneGrantPerGranteePerObject(t *testing.T) {
	pool, ctx := upOwner(t)
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
	var viewer string
	if err := pool.QueryRow(ctx, `SELECT id FROM identity.roles WHERE key = 'viewer'`).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, grantee_role_id, level, granted_by)
		VALUES ('task', $1, $2, 'viewer', $3)`, object, viewer, owner); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, grantee_role_id, level, granted_by)
		VALUES ('task', $1, $2, 'editor', $3)`, object, viewer, owner)
	if err == nil {
		t.Fatal("a second grant for the same role was accepted")
	}
}

func TestPrincipalShapes(t *testing.T) {
	pool, ctx := upOwner(t)
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
	pool, ctx := upOwner(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `INSERT INTO identity.roles (key, name) VALUES ('boss', 'Boss')`); err == nil {
		t.Fatal("role boss was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', uuidv7(), $1, 'author', $1)`, owner); err == nil {
		t.Fatal("level author was accepted")
	}
}

func TestOwnerIsNotAShareLevel(t *testing.T) {
	pool, ctx := upOwner(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	_, err := pool.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('task', uuidv7(), $1, 'owner', $1)`, owner)
	if err == nil {
		t.Fatal("share level owner was accepted")
	}
}

func TestSystemRolesAndCustomPermissions(t *testing.T) {
	pool, ctx := upOwner(t)
	seedOwner(t, pool, ctx, "ada@example.com")
	var keys []string
	rows, err := pool.Query(ctx, `SELECT key FROM identity.roles WHERE key IS NOT NULL ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := values(domain.Roles())
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Fatalf("system roles = %v, want %v", keys, want)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.roles SET name = 'Proprietor' WHERE key = 'owner'`); err == nil {
		t.Fatal("a system role was renamed")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM identity.roles WHERE key = 'viewer'`); err == nil {
		t.Fatal("a system role was deleted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.roles (name, permissions) VALUES ('Steward', ARRAY['owners:manage'])`); err == nil {
		t.Fatal("a custom role held an owner-only permission")
	}
	var custom string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.roles (name, permissions) VALUES ('Steward', ARRAY['task:view']) RETURNING id`).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('lee@example.com', 'Lee', $1, now())`, custom); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM identity.roles WHERE id = $1`, custom); err == nil {
		t.Fatal("a role in use was deleted")
	}
}

func TestDisabledOwnerDoesNotCount(t *testing.T) {
	pool, ctx := upOwner(t)
	owner := seedOwner(t, pool, ctx, "ada@example.com")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("disabling the only joined owner committed")
	}
}

func TestDownRemovesTheIdentitySchema(t *testing.T) {
	pool, ctx := upOwner(t)
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
	pool, ctx := upOwner(t)
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
	assertQuoted(t, checks, "roles_key_check", values(domain.Roles()))
	for _, permission := range domain.OwnerOnly() {
		if !strings.Contains(checks["roles_permissions_check"], "'"+string(permission)+"'") {
			t.Fatalf("owner-only %s missing from %s", permission, checks["roles_permissions_check"])
		}
	}
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

func TestTwoConcurrentDemotionsLeaveOneOwner(t *testing.T) {
	pool, ctx := upOwner(t)
	first := seedOwner(t, pool, ctx, "ada@example.com")
	var second string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('lee@example.com', 'Lee', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			if _, err := tx.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'member') WHERE id = $1`, id); err != nil {
				_ = tx.Rollback(ctx)
				errs <- err
				return
			}
			errs <- tx.Commit(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	failed := 0
	for err := range errs {
		if err != nil {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed commits = %d, want 1", failed)
	}
	var owners int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM identity.users AS person
		JOIN identity.roles AS role ON role.id = person.role_id
		WHERE role.key = 'owner' AND person.joined_at IS NOT NULL AND person.disabled_at IS NULL`).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != 1 {
		t.Fatalf("owners = %d, want 1", owners)
	}
}

func TestIdentityMigrationVersions(t *testing.T) {
	pool, ctx := upOwner(t)
	var n, latest int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(max(version_id), 0)
		FROM identity.goose_db_version
		WHERE is_applied AND version_id > 0`).Scan(&n, &latest); err != nil {
		t.Fatal(err)
	}
	if n != 3 || latest != 3 {
		t.Fatalf("applied versions = %d latest = %d, want 3 and 3", n, latest)
	}
}

func TestPasswordChangeDoesNotFireTheOwnerGuard(t *testing.T) {
	ownerPool, pool, ctx := upBoth(t)
	owner := seedOwner(t, ownerPool, ctx, "ada@example.com")
	if _, err := pool.Exec(ctx, `UPDATE identity.users SET password_hash = 'stored' WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	var def, source string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_triggerdef(oid)
		FROM pg_trigger
		WHERE tgname = 'users_keep_joined_owner' AND tgrelid = 'identity.users'::regclass`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "UPDATE OF role_id, joined_at, disabled_at") {
		t.Fatalf("trigger = %s", def)
	}
	if err := pool.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE proname = 'keep_joined_owner'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, "pg_advisory_xact_lock") {
		t.Fatal("owner guard does not take a lock")
	}
	var nameCheck, shares, links, throttle int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_constraint WHERE conname = 'users_name_check'),
		  (SELECT count(*) FROM pg_indexes WHERE schemaname = 'identity' AND indexname = 'shares_object'),
		  (SELECT count(*) FROM pg_indexes WHERE schemaname = 'identity' AND indexname = 'sign_in_links_id'),
		  (SELECT count(*) FROM pg_indexes WHERE schemaname = 'identity' AND indexname = 'throttle_updated')`).Scan(&nameCheck, &shares, &links, &throttle); err != nil {
		t.Fatal(err)
	}
	if nameCheck != 1 || shares != 1 || links != 1 || throttle != 1 {
		t.Fatalf("schema pieces name %d shares %d links %d throttle %d", nameCheck, shares, links, throttle)
	}
	if _, err := ownerPool.Exec(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('empty@example.com', '', (SELECT id FROM identity.roles WHERE key = 'member'), now())`); err == nil {
		t.Fatal("an empty name was accepted")
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($2, $1, 'recover', now() + interval '1 minute')
		RETURNING id::text`, owner, []byte{1, 2, 3}).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("link id was empty")
	}
}

func up(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	owner, ctx := upOwner(t)
	return pgtest.App(t, owner), ctx
}

func upBoth(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, context.Context) {
	t.Helper()
	owner, ctx := upOwner(t)
	return owner, pgtest.App(t, owner), ctx
}

func upOwner(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	// Identity writes record their audit events in the same transaction, so
	// the audit schema migrates with identity here.
	if err := audit.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func seedOwner(t *testing.T, pool *pgxpool.Pool, ctx context.Context, email string) string {
	t.Helper()
	var owner string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now()) RETURNING id`, email).Scan(&owner); err != nil {
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
