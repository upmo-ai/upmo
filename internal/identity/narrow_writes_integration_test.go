//go:build integration

package identity_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

var allowedWriteColumns = map[string][]string{
	"users":      {"password_hash", "webauthn_handle", "joined_at", "passkey_offered_at"},
	"sessions":   {"last_seen_at", "sign_in_method", "signed_in_at"},
	"api_tokens": {"last_used_at"},
}

func TestAppCannotUpdateProtectedColumns(t *testing.T) {
	owner, ctx := upOwner(t)
	appPool := pgtest.AppLogin(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	sessionHash := []byte{10, 10, 10, 10}
	tokenHash := []byte{11, 11, 11, 11}
	var sessionID, tokenID, memberRoleID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour')
		RETURNING id::text`, person, sessionHash).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'probe', $2, ARRAY['task:view'], now(), now() + interval '1 day')
		RETURNING id::text`, person, tokenHash).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRoleID); err != nil {
		t.Fatal(err)
	}

	other := seedOwner(t, owner, ctx, "bea@example.com")
	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"sessions.user_id", `UPDATE identity.sessions SET user_id = $2 WHERE id = $1`, []any{sessionID, other}},
		{"sessions.token_hash", `UPDATE identity.sessions SET token_hash = $2 WHERE id = $1`, []any{sessionID, []byte{9, 9}}},
		{"api_tokens.user_id", `UPDATE identity.api_tokens SET user_id = $2 WHERE id = $1`, []any{tokenID, other}},
		{"api_tokens.token_hash", `UPDATE identity.api_tokens SET token_hash = $2 WHERE id = $1`, []any{tokenID, []byte{8, 8}}},
		{"users.email", `UPDATE identity.users SET email = 'evil@example.com' WHERE id = $1`, []any{person}},
		{"users.role_id", `UPDATE identity.users SET role_id = $2 WHERE id = $1`, []any{person, memberRoleID}},
		{"users.disabled_at", `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, []any{person}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := appPool.Exec(ctx, tc.sql, tc.args...)
			assertDenied(t, tc.name, err)
		})
	}
}

func TestAppCannotInsertUsersOrTokensDirectly(t *testing.T) {
	owner, ctx := upOwner(t)
	appPool := pgtest.AppLogin(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	var memberRoleID string
	if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRoleID); err != nil {
		t.Fatal(err)
	}

	stored, hash, keyID, err := sealer.NewEmail(t).Seal(t.Context(), "rogue@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = appPool.Exec(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Rogue', $4, now())`, stored, hash, keyID, memberRoleID)
	assertDenied(t, "insert users", err)

	_, err = appPool.Exec(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'rogue', $2, ARRAY['task:view'], now(), now() + interval '1 day')`, person, []byte{7, 7, 7, 7})
	assertDenied(t, "insert api_tokens", err)
}

func TestAppCannotDeleteUsers(t *testing.T) {
	owner, ctx := upOwner(t)
	appPool := pgtest.AppLogin(t, owner)
	seedOwner(t, owner, ctx, "ada@example.com")
	var memberID string
	mStored, mHash, mKeyID, err := sealer.NewEmail(t).Seal(t.Context(), "lee@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Lee', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`, mStored, mHash, mKeyID).Scan(&memberID); err != nil {
		t.Fatal(err)
	}

	var deleteGrant bool
	if err := owner.QueryRow(ctx, `SELECT has_table_privilege('upmo_app', 'identity.users', 'DELETE')`).Scan(&deleteGrant); err != nil {
		t.Fatal(err)
	}
	if deleteGrant {
		t.Fatalf("identity.users DELETE grant = true, want false")
	}

	_, err = appPool.Exec(ctx, `DELETE FROM identity.users WHERE id = $1`, memberID)
	assertDenied(t, "delete users", err)
}

func TestAppUpdatesAllowedColumns(t *testing.T) {
	owner, ctx := upOwner(t)
	appPool := pgtest.AppLogin(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	var sessionID, tokenID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour')
		RETURNING id::text`, person, []byte{1, 2, 3, 4}).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'probe', $2, ARRAY['task:view'], now(), now() + interval '1 day')
		RETURNING id::text`, person, []byte{2, 3, 4, 5}).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE identity.users SET password_hash = 'sealed', webauthn_handle = $2, joined_at = now(), passkey_offered_at = now() WHERE id = $1`, []any{person, []byte{9, 8, 7}}},
		{`UPDATE identity.sessions SET last_seen_at = now(), sign_in_method = 'password', signed_in_at = now() WHERE id = $1`, []any{sessionID}},
		{`UPDATE identity.api_tokens SET last_used_at = now() WHERE id = $1`, []any{tokenID}},
	} {
		if _, err := appPool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("%s: %v", stmt.sql, err)
		}
	}

	for table, allowed := range allowedWriteColumns {
		checkUpdateColumnGrants(t, owner, ctx, table, allowed)
	}
}

func checkUpdateColumnGrants(t *testing.T, pool *pgxpool.Pool, ctx context.Context, table string, allowed []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT column_name,
		       has_column_privilege('upmo_app', 'identity.' || table_name, column_name, 'UPDATE')
		FROM information_schema.columns
		WHERE table_schema = 'identity' AND table_name = $1`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var column string
		var granted bool
		if err := rows.Scan(&column, &granted); err != nil {
			t.Fatal(err)
		}
		seen[column] = true
		if granted != slices.Contains(allowed, column) {
			t.Fatalf("identity.%s.%s update grant %t, want %t", table, column, granted, slices.Contains(allowed, column))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, column := range allowed {
		if !seen[column] {
			t.Fatalf("identity.%s has no %s column", table, column)
		}
	}
}

func TestDefinerWriteFunctionsEnforceSessionAndHierarchy(t *testing.T) {
	owner, ctx := upOwner(t)
	appPool := pgtest.AppLogin(t, owner)
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Initial bootstrap succeeds with NULL actor hash only while users is empty.
	var firstOwnerID string
	if err := appPool.QueryRow(ctx, `
		SELECT identity.member_invite(NULL, $1, $2, $3, 'Ada', NULL, NULL, $4, $5, $6)::text`,
		append(inviteEmail(t, "ada@example.com"), []byte{1, 1, 1}, now, now.Add(time.Hour))...).Scan(&firstOwnerID); err != nil {
		t.Fatalf("initial bootstrap: %v", err)
	}
	// Once at least one user exists, NULL actor hash must fail.
	_, err := appPool.Exec(ctx, `
		SELECT identity.member_invite(NULL, $1, $2, $3, 'Second', NULL, NULL, $4, $5, $6)`,
		append(inviteEmail(t, "second@example.com"), []byte{2, 2, 2}, now, now.Add(time.Hour))...)
	assertDenied(t, "second bootstrap with NULL hash", err)

	var ownerRoleID, adminRoleID, memberRoleID, viewerRoleID string
	if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'owner'`).Scan(&ownerRoleID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'admin'`).Scan(&adminRoleID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRoleID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'viewer'`).Scan(&viewerRoleID); err != nil {
		t.Fatal(err)
	}
	var customMgrRoleID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.roles (name, permissions)
		VALUES ('Manager', ARRAY['members:manage', 'task:view'])
		RETURNING id::text`).Scan(&customMgrRoleID); err != nil {
		t.Fatal(err)
	}

	adminID := seedPerson(t, owner, ctx, "admin@example.com", adminRoleID, true, false)
	memberID := seedPerson(t, owner, ctx, "member@example.com", memberRoleID, true, false)
	viewerID := seedPerson(t, owner, ctx, "viewer@example.com", viewerRoleID, true, false)
	customMgrID := seedPerson(t, owner, ctx, "mgr@example.com", customMgrRoleID, true, false)
	pendingAdminID := seedPerson(t, owner, ctx, "pending@example.com", adminRoleID, false, false)
	disabledAdminID := seedPerson(t, owner, ctx, "gone@example.com", adminRoleID, true, true)

	ownerSess := []byte{10, 1}
	adminSess := []byte{10, 2}
	memberSess := []byte{10, 3}
	customMgrSess := []byte{10, 4}
	expiredSess := []byte{10, 5}
	pendingSess := []byte{10, 6}
	disabledSess := []byte{10, 7}
	unknownSess := []byte{99, 99}

	seedSess(t, owner, ctx, firstOwnerID, ownerSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, adminID, adminSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, memberID, memberSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, customMgrID, customMgrSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, firstOwnerID, expiredSess, now.Add(-time.Minute))
	seedSess(t, owner, ctx, pendingAdminID, pendingSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, disabledAdminID, disabledSess, now.Add(time.Hour))

	// Invalid, expired, pending, or disabled actor sessions are rejected across all four definer functions.
	for name, badHash := range map[string][]byte{
		"unknown":  unknownSess,
		"expired":  expiredSess,
		"pending":  pendingSess,
		"disabled": disabledSess,
	} {
		_, err := appPool.Exec(ctx, `SELECT * FROM identity.member_set_role($1, $2, $3)`, badHash, memberID, viewerRoleID)
		assertDenied(t, "member_set_role "+name, err)

		_, err = appPool.Exec(ctx, `SELECT identity.member_disable($1, $2, $3)`, badHash, viewerID, now)
		assertDenied(t, "member_disable "+name, err)

		_, err = appPool.Exec(ctx, `SELECT identity.member_invite($1, $2, $3, $4, 'X', $5, $6, $7, $8, $9)`,
			append(append([]any{badHash}, inviteEmail(t, "x@example.com")...), memberRoleID, []byte{1}, []byte{2}, now, now.Add(time.Hour))...)
		assertDenied(t, "member_invite "+name, err)

		_, err = appPool.Exec(ctx, `SELECT identity.token_create($1, 'ci', $2, ARRAY['task:view'], $3, $4)`,
			badHash, []byte{3, 4}, now, now.Add(time.Hour))
		assertDenied(t, "token_create "+name, err)
	}

	// Hierarchy and self checks for member_set_role.
	_, err = appPool.Exec(ctx, `SELECT * FROM identity.member_set_role($1, $2, $3)`, memberSess, viewerID, adminRoleID)
	assertDenied(t, "member cannot promote viewer", err)

	_, err = appPool.Exec(ctx, `SELECT * FROM identity.member_set_role($1, $2, $3)`, adminSess, memberID, ownerRoleID)
	assertDenied(t, "admin cannot promote member to owner", err)

	_, err = appPool.Exec(ctx, `SELECT * FROM identity.member_set_role($1, $2, $3)`, adminSess, firstOwnerID, memberRoleID)
	assertDenied(t, "admin cannot demote owner", err)

	_, err = appPool.Exec(ctx, `SELECT * FROM identity.member_set_role($1, $2, $3)`, customMgrSess, viewerID, memberRoleID)
	assertDenied(t, "custom manager cannot grant unheld permission", err)

	_, err = appPool.Exec(ctx, `SELECT * FROM identity.member_set_role($1, $2, $3)`, ownerSess, firstOwnerID, adminRoleID)
	assertDenied(t, "owner cannot change own role", err)

	var oldRole, newRole string
	if err := appPool.QueryRow(ctx, `SELECT old_role_id::text, new_role_id::text FROM identity.member_set_role($1, $2, $3)`,
		adminSess, viewerID, memberRoleID).Scan(&oldRole, &newRole); err != nil {
		t.Fatalf("admin promote viewer to member: %v", err)
	}
	if oldRole != viewerRoleID || newRole != memberRoleID {
		t.Fatalf("role change got (%s, %s), want (%s, %s)", oldRole, newRole, viewerRoleID, memberRoleID)
	}

	// Hierarchy and self checks for member_invite.
	_, err = appPool.Exec(ctx, `SELECT identity.member_invite($1, $2, $3, $4, 'New', $5, $6, $7, $8, $9)`,
		append(append([]any{memberSess}, inviteEmail(t, "new@example.com")...), viewerRoleID, []byte{20}, []byte{21}, now, now.Add(time.Hour))...)
	assertDenied(t, "member cannot invite", err)

	_, err = appPool.Exec(ctx, `SELECT identity.member_invite($1, $2, $3, $4, 'NewOwner', $5, $6, $7, $8, $9)`,
		append(append([]any{adminSess}, inviteEmail(t, "newowner@example.com")...), ownerRoleID, []byte{22}, []byte{23}, now, now.Add(time.Hour))...)
	assertDenied(t, "admin cannot invite owner", err)

	// Hierarchy and self checks for member_disable.
	_, err = appPool.Exec(ctx, `SELECT identity.member_disable($1, $2, $3)`, memberSess, viewerID, now)
	assertDenied(t, "member cannot disable", err)

	_, err = appPool.Exec(ctx, `SELECT identity.member_disable($1, $2, $3)`, adminSess, firstOwnerID, now)
	assertDenied(t, "admin cannot disable owner", err)

	_, err = appPool.Exec(ctx, `SELECT identity.member_disable($1, $2, $3)`, adminSess, adminID, now)
	assertDenied(t, "admin cannot disable self", err)

	// token_create derives user_id solely from actor_token_hash and validates scope.
	_, err = appPool.Exec(ctx, `SELECT identity.token_create($1, 'bad', $2, ARRAY['task:delete'], $3, $4)`,
		ownerSess, []byte{30, 1}, now, now.Add(time.Hour))
	assertDenied(t, "token cannot hold delete", err)

	_, err = appPool.Exec(ctx, `SELECT identity.token_create($1, 'bad', $2, ARRAY['members:manage'], $3, $4)`,
		ownerSess, []byte{30, 2}, now, now.Add(time.Hour))
	assertDenied(t, "token cannot hold members:manage", err)

	_, err = appPool.Exec(ctx, `SELECT identity.token_create($1, 'bad', $2, ARRAY['audit:view'], $3, $4)`,
		memberSess, []byte{30, 3}, now, now.Add(time.Hour))
	assertDenied(t, "member token cannot hold audit:view", err)

	_, err = appPool.Exec(ctx, `SELECT identity.token_create($1, 'dup', $2, ARRAY['task:view', 'task:view'], $3, $4)`,
		memberSess, []byte{30, 4}, now, now.Add(time.Hour))
	assertDenied(t, "token cannot hold duplicate scope", err)

	var createdTokenID string
	if err := appPool.QueryRow(ctx, `SELECT identity.token_create($1, 'audit-reader', $2, ARRAY['audit:view', 'task:view'], $3, $4)::text`,
		adminSess, []byte{30, 5}, now, now.Add(time.Hour)).Scan(&createdTokenID); err != nil {
		t.Fatalf("admin token_create: %v", err)
	}
	var tokenOwner string
	if err := owner.QueryRow(ctx, `SELECT user_id::text FROM identity.api_tokens WHERE id = $1`, createdTokenID).Scan(&tokenOwner); err != nil {
		t.Fatal(err)
	}
	if tokenOwner != adminID {
		t.Fatalf("token user_id = %s, want %s", tokenOwner, adminID)
	}
}

func assertDenied(t *testing.T, label string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("%s: err = %v, want SQLSTATE 42501", label, err)
	}
}

// inviteEmail returns the sealed address, its index hashes and the hash key id
// in the order member_invite takes them.
func inviteEmail(t *testing.T, email string) []any {
	t.Helper()
	stored, hash, keyID, err := sealer.NewEmail(t).Seal(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	return []any{stored, [][]byte{hash}, keyID}
}

func seedPerson(t *testing.T, pool *pgxpool.Pool, ctx context.Context, email, roleID string, joined, disabled bool) string {
	t.Helper()
	now := time.Now().UTC()
	var joinedAt, disabledAt any
	if joined {
		joinedAt = now
	}
	if disabled {
		disabledAt = now
	}
	stored, hash, keyID, err := sealer.NewEmail(t).Seal(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at, joined_at, disabled_at)
		VALUES ($1, $2, $3, 'Person', $4, $5, $6, $7)
		RETURNING id::text`, stored, hash, keyID, roleID, now, joinedAt, disabledAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedSess(t *testing.T, pool *pgxpool.Pool, ctx context.Context, userID string, hash []byte, expires time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, now(), now(), $3)`, userID, hash, expires); err != nil {
		t.Fatal(err)
	}
}

// roleWorld is the cast the role definer tests share: a bootstrapped owner,
// the system roles, a custom manager, and a live session per person.
type roleWorld struct {
	owner      *pgxpool.Pool
	app        *pgxpool.Pool
	ctx        context.Context
	now        time.Time
	ownerRole  string
	adminRole  string
	memberRole string
	viewerRole string
	mgrRole    string
	firstOwner string
	memberID   string
	mgrID      string
	ownerSess  []byte
	adminSess  []byte
	memberSess []byte
	viewerSess []byte
	mgrSess    []byte
}

func upRoleWorld(t *testing.T) *roleWorld {
	t.Helper()
	owner, ctx := upOwner(t)
	app := pgtest.AppLogin(t, owner)
	now := time.Now().UTC().Truncate(time.Microsecond)

	var firstOwnerID string
	if err := app.QueryRow(ctx, `
		SELECT identity.member_invite(NULL, $1, $2, $3, 'Ada', NULL, NULL, $4, $5, $6)::text`,
		append(inviteEmail(t, "ada@example.com"), []byte{1, 1, 1}, now, now.Add(time.Hour))...).Scan(&firstOwnerID); err != nil {
		t.Fatalf("initial bootstrap: %v", err)
	}

	w := &roleWorld{owner: owner, app: app, ctx: ctx, now: now, firstOwner: firstOwnerID}
	for _, system := range []struct {
		id  *string
		key string
	}{
		{&w.ownerRole, "owner"}, {&w.adminRole, "admin"}, {&w.memberRole, "member"}, {&w.viewerRole, "viewer"},
	} {
		if err := owner.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = $1`, system.key).Scan(system.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.roles (name, permissions)
		VALUES ('Manager', ARRAY['roles:manage', 'task:view'])
		RETURNING id::text`).Scan(&w.mgrRole); err != nil {
		t.Fatal(err)
	}

	w.memberID = seedPerson(t, owner, ctx, "member@example.com", w.memberRole, true, false)
	w.mgrID = seedPerson(t, owner, ctx, "mgr@example.com", w.mgrRole, true, false)
	viewerID := seedPerson(t, owner, ctx, "viewer@example.com", w.viewerRole, true, false)
	adminID := seedPerson(t, owner, ctx, "admin@example.com", w.adminRole, true, false)

	w.ownerSess = []byte{11, 1}
	w.adminSess = []byte{11, 2}
	w.memberSess = []byte{11, 3}
	w.viewerSess = []byte{11, 4}
	w.mgrSess = []byte{11, 5}
	seedSess(t, owner, ctx, firstOwnerID, w.ownerSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, adminID, w.adminSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, w.memberID, w.memberSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, viewerID, w.viewerSess, now.Add(time.Hour))
	seedSess(t, owner, ctx, w.mgrID, w.mgrSess, now.Add(time.Hour))
	return w
}

// seedCustomRole inserts a custom role through the owner pool and returns its id.
func (w *roleWorld) seedCustomRole(t *testing.T, name string, permissions []string) string {
	t.Helper()
	var id string
	if err := w.owner.QueryRow(w.ctx, `
		INSERT INTO identity.roles (name, permissions)
		VALUES ($1, $2)
		RETURNING id::text`, name, permissions).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAppCannotWriteRolesDirectly(t *testing.T) {
	owner, ctx := upOwner(t)
	appPool := pgtest.AppLogin(t, owner)
	var target string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.roles (name, permissions)
		VALUES ('Target', ARRAY['task:view'])
		RETURNING id::text`).Scan(&target); err != nil {
		t.Fatal(err)
	}

	_, err := appPool.Exec(ctx, `
		INSERT INTO identity.roles (name, permissions)
		VALUES ('Rogue', ARRAY['task:view'])`)
	assertDenied(t, "insert roles", err)

	_, err = appPool.Exec(ctx, `UPDATE identity.roles SET permissions = ARRAY['task:edit'] WHERE id = $1`, target)
	assertDenied(t, "update roles", err)

	_, err = appPool.Exec(ctx, `DELETE FROM identity.roles WHERE id = $1`, target)
	assertDenied(t, "delete roles", err)

	var insertGrant, updateGrant, deleteGrant, selectGrant bool
	if err := owner.QueryRow(ctx, `
		SELECT has_table_privilege('upmo_app', 'identity.roles', 'INSERT'),
		       has_table_privilege('upmo_app', 'identity.roles', 'UPDATE'),
		       has_table_privilege('upmo_app', 'identity.roles', 'DELETE'),
		       has_table_privilege('upmo_app', 'identity.roles', 'SELECT')`).
		Scan(&insertGrant, &updateGrant, &deleteGrant, &selectGrant); err != nil {
		t.Fatal(err)
	}
	if insertGrant || updateGrant || deleteGrant || !selectGrant {
		t.Fatalf("identity.roles grants insert=%t update=%t delete=%t select=%t, want only select",
			insertGrant, updateGrant, deleteGrant, selectGrant)
	}
}

func TestRoleFunctionsRefuseUnprovenOrUnauthorized(t *testing.T) {
	w := upRoleWorld(t)
	spare := w.seedCustomRole(t, "Spare", []string{"task:view", "task:edit"})

	pendingID := seedPerson(t, w.owner, w.ctx, "pending@example.com", w.adminRole, false, false)
	disabledID := seedPerson(t, w.owner, w.ctx, "gone@example.com", w.adminRole, true, true)
	expiredSess := []byte{11, 6}
	pendingSess := []byte{11, 7}
	disabledSess := []byte{11, 8}
	unknownSess := []byte{99, 99}
	seedSess(t, w.owner, w.ctx, w.firstOwner, expiredSess, w.now.Add(-time.Minute))
	seedSess(t, w.owner, w.ctx, pendingID, pendingSess, w.now.Add(time.Hour))
	seedSess(t, w.owner, w.ctx, disabledID, disabledSess, w.now.Add(time.Hour))

	calls := []struct {
		name string
		sql  string
		args func(hash []byte) []any
	}{
		{"role_create", `SELECT identity.role_create($1, 'Rogue', ARRAY['task:view'])`, func(hash []byte) []any { return []any{hash} }},
		{"role_change", `SELECT identity.role_change($1, $2, 'Spare', ARRAY['task:view'])`, func(hash []byte) []any { return []any{hash, spare} }},
		{"role_delete", `SELECT identity.role_delete($1, $2)`, func(hash []byte) []any { return []any{hash, spare} }},
	}
	// No session proof, no live session, no joined person: all three refuse.
	for _, call := range calls {
		for name, hash := range map[string][]byte{
			"nil":      nil,
			"unknown":  unknownSess,
			"expired":  expiredSess,
			"pending":  pendingSess,
			"disabled": disabledSess,
		} {
			_, err := w.app.Exec(w.ctx, call.sql, call.args(hash)...)
			assertDenied(t, call.name+" "+name, err)
		}
	}

	// A person without roles:manage cannot use any of the three functions.
	for name, hash := range map[string][]byte{"member": w.memberSess, "viewer": w.viewerSess} {
		for _, call := range calls {
			_, err := w.app.Exec(w.ctx, call.sql, call.args(hash)...)
			assertDenied(t, name+" "+call.name, err)
		}
	}

	// A custom manager may only grant permissions they hold.
	_, err := w.app.Exec(w.ctx, `SELECT identity.role_create($1, 'Beyond', ARRAY['task:delete'])`, w.mgrSess)
	assertDenied(t, "manager create beyond own", err)

	_, err = w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'Spare', ARRAY['task:view', 'task:delete'])`, w.mgrSess, spare)
	assertDenied(t, "manager change beyond own", err)

	if _, err := w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'Spare', ARRAY['task:view'])`, w.mgrSess, spare); err != nil {
		t.Fatalf("manager change removing a permission they do not hold: %v", err)
	}
	if _, err := w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'Spare', ARRAY['task:view', 'roles:manage'])`, w.mgrSess, spare); err != nil {
		t.Fatalf("manager change adding a permission they hold: %v", err)
	}

	// Nobody edits their own role or a system role.
	_, err = w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'Manager', ARRAY['roles:manage'])`, w.mgrSess, w.mgrRole)
	assertDenied(t, "manager change own role", err)

	_, err = w.app.Exec(w.ctx, `SELECT identity.role_delete($1, $2)`, w.mgrSess, w.mgrRole)
	assertDenied(t, "manager delete own role", err)

	_, err = w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'Owner', ARRAY['task:view'])`, w.ownerSess, w.ownerRole)
	assertDenied(t, "owner change system role", err)

	_, err = w.app.Exec(w.ctx, `SELECT identity.role_delete($1, $2)`, w.adminSess, w.viewerRole)
	assertDenied(t, "admin delete system role", err)
}

func TestRoleFunctionsRejectBadInput(t *testing.T) {
	w := upRoleWorld(t)
	clerk := w.seedCustomRole(t, "Clerk", []string{"task:view"})
	beta := w.seedCustomRole(t, "Beta", []string{"task:view"})

	cases := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"create owners:manage", `SELECT identity.role_create($1, 'Own', ARRAY['owners:manage'])`, []any{w.adminSess}, "23514"},
		{"create unknown permission", `SELECT identity.role_create($1, 'Own', ARRAY['task:fly'])`, []any{w.adminSess}, "23514"},
		{"create NULL element", `SELECT identity.role_create($1, 'Own', ARRAY['task:view', NULL])`, []any{w.adminSess}, "23514"},
		{"create duplicate element", `SELECT identity.role_create($1, 'Own', ARRAY['task:view', 'task:view'])`, []any{w.adminSess}, "23514"},
		{"create empty array", `SELECT identity.role_create($1, 'Own', ARRAY[]::text[])`, []any{w.adminSess}, "23514"},
		{"create NULL permissions", `SELECT identity.role_create($1, 'Own', NULL)`, []any{w.adminSess}, "23514"},
		{"create blank name", `SELECT identity.role_create($1, '  ', ARRAY['task:view'])`, []any{w.adminSess}, "23514"},
		{"create NULL name", `SELECT identity.role_create($1, NULL, ARRAY['task:view'])`, []any{w.adminSess}, "23514"},
		{"create 65-char name", `SELECT identity.role_create($1, $2, ARRAY['task:view'])`, []any{w.adminSess, strings.Repeat("x", 65)}, "23514"},
		{"create case-only duplicate", `SELECT identity.role_create($1, 'CLERK', ARRAY['task:view'])`, []any{w.adminSess}, "23505"},
		{"change unknown permission", `SELECT identity.role_change($1, $2, 'Clerk', ARRAY['task:fly'])`, []any{w.adminSess, clerk}, "23514"},
		{"change blank name", `SELECT identity.role_change($1, $2, '', ARRAY['task:view'])`, []any{w.adminSess, clerk}, "23514"},
		{"change NULL permissions", `SELECT identity.role_change($1, $2, 'Clerk', NULL)`, []any{w.adminSess, clerk}, "23514"},
		{"change unknown role", `SELECT identity.role_change($1, gen_random_uuid(), 'X', ARRAY['task:view'])`, []any{w.adminSess}, "P0002"},
		{"delete unknown role", `SELECT identity.role_delete($1, gen_random_uuid())`, []any{w.adminSess}, "P0002"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.app.Exec(w.ctx, tc.sql, tc.args...)
			assertPgCode(t, tc.name, err, tc.code)
		})
	}

	// Renaming to the same name in another case is not a duplicate; another
	// role's name is.
	if _, err := w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'BETA', ARRAY['task:view'])`, w.adminSess, beta); err != nil {
		t.Fatalf("change to own name in other case: %v", err)
	}
	_, err := w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'Clerk', ARRAY['task:view'])`, w.adminSess, beta)
	assertPgCode(t, "change to another role's name", err, "23505")

	// A holder, even a disabled one, keeps the role alive; the count comes back.
	held := w.seedCustomRole(t, "Held", []string{"task:view"})
	seedPerson(t, w.owner, w.ctx, "holder@example.com", held, true, true)
	var holders int
	if err := w.app.QueryRow(w.ctx, `SELECT identity.role_delete($1, $2)`, w.adminSess, held).Scan(&holders); err != nil {
		t.Fatalf("delete held role: %v", err)
	}
	if holders != 1 {
		t.Fatalf("holder count = %d, want 1", holders)
	}
	if rows := countRoles(t, w.owner, w.ctx, held); rows != 1 {
		t.Fatal("the held role was deleted")
	}

	// A share referencing the role refuses the delete.
	shared := w.seedCustomRole(t, "Shared", []string{"task:view"})
	if _, err := w.owner.Exec(w.ctx, `
		INSERT INTO identity.shares (object_type, object_id, grantee_role_id, level, granted_by)
		VALUES ('task', uuidv7(), $1, 'viewer', $2)`, shared, w.firstOwner); err != nil {
		t.Fatal(err)
	}
	_, err = w.app.Exec(w.ctx, `SELECT identity.role_delete($1, $2)`, w.adminSess, shared)
	assertPgCode(t, "delete shared role", err, "23503")
	if rows := countRoles(t, w.owner, w.ctx, shared); rows != 1 {
		t.Fatal("the shared role was deleted")
	}
}

func TestRoleFunctionsCreateChangeDelete(t *testing.T) {
	w := upRoleWorld(t)

	var id string
	if err := w.app.QueryRow(w.ctx, `
		SELECT identity.role_create($1, 'Planner', ARRAY['task:view', 'task:edit'])::text`, w.adminSess).Scan(&id); err != nil {
		t.Fatalf("create: %v", err)
	}
	var name string
	var stored []string
	if err := w.owner.QueryRow(w.ctx, `SELECT name, permissions FROM identity.roles WHERE id = $1`, id).Scan(&name, &stored); err != nil {
		t.Fatal(err)
	}
	if name != "Planner" || !slices.Equal(stored, []string{"task:view", "task:edit"}) {
		t.Fatalf("created role = %s %v", name, stored)
	}

	var before []string
	if err := w.app.QueryRow(w.ctx, `
		SELECT identity.role_change($1, $2, 'Planner II', ARRAY['task:view'])`, w.adminSess, id).Scan(&before); err != nil {
		t.Fatalf("change: %v", err)
	}
	if !slices.Equal(before, []string{"task:view", "task:edit"}) {
		t.Fatalf("permissions held before = %v", before)
	}
	if err := w.owner.QueryRow(w.ctx, `SELECT name, permissions FROM identity.roles WHERE id = $1`, id).Scan(&name, &stored); err != nil {
		t.Fatal(err)
	}
	if name != "Planner II" || !slices.Equal(stored, []string{"task:view"}) {
		t.Fatalf("changed role = %s %v", name, stored)
	}

	var deleted int
	if err := w.app.QueryRow(w.ctx, `SELECT identity.role_delete($1, $2)`, w.adminSess, id).Scan(&deleted); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("delete returned %d, want 0", deleted)
	}
	if rows := countRoles(t, w.owner, w.ctx, id); rows != 0 {
		t.Fatal("the role survived its delete")
	}
}

// TestRoleFunctionsMatchMayGrant pins the SQL grant rule to the domain rule:
// for every actor and catalog permission (owners:manage is refused as input
// before either rule runs), both functions and domain.MayGrant must agree.
func TestRoleFunctionsMatchMayGrant(t *testing.T) {
	w := upRoleWorld(t)
	parity := w.seedCustomRole(t, "Parity", []string{})

	systemSubject := func(role domain.Role) domain.Subject {
		permissions, err := domain.SystemPermissions(role)
		if err != nil {
			t.Fatal(err)
		}
		return domain.Subject{Kind: domain.KindUser, Joined: true, Permissions: permissions}
	}
	actors := []struct {
		name    string
		hash    []byte
		subject domain.Subject
	}{
		{"owner", w.ownerSess, systemSubject(domain.RoleOwner)},
		{"admin", w.adminSess, systemSubject(domain.RoleAdmin)},
		{"member", w.memberSess, systemSubject(domain.RoleMember)},
		{"viewer", w.viewerSess, systemSubject(domain.RoleViewer)},
		{"custom", w.mgrSess, domain.Subject{Kind: domain.KindUser, Joined: true, Permissions: []domain.Permission{
			domain.PermRolesManage, domain.ObjectPermission(domain.TypeTask, domain.ActionView),
		}}},
	}

	created := 0
	for _, actor := range actors {
		for _, permission := range domain.Permissions() {
			if permission == domain.PermOwnersManage {
				continue
			}
			want := domain.MayGrant(actor.subject, []domain.Permission{permission}) == nil
			label := actor.name + " " + string(permission)

			created++
			_, err := w.app.Exec(w.ctx, `SELECT identity.role_create($1, $2, ARRAY[$3]::text[])`,
				actor.hash, fmt.Sprintf("Parity %d", created), string(permission))
			assertGrantOutcome(t, "create "+label, want, err)

			_, err = w.app.Exec(w.ctx, `SELECT identity.role_change($1, $2, 'Parity', ARRAY[$3]::text[])`,
				actor.hash, parity, string(permission))
			assertGrantOutcome(t, "change "+label, want, err)

			// The change target starts empty again so every permission is added.
			if _, err := w.owner.Exec(w.ctx, `UPDATE identity.roles SET permissions = '{}' WHERE id = $1`, parity); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestRoleStoreMapsDefinerErrors calls the store methods as the app pool and
// checks every definer refusal maps to its domain error.
func TestRoleStoreMapsDefinerErrors(t *testing.T) {
	w := upRoleWorld(t)
	store := postgres.Passkeys{Pool: w.app}

	spare := w.seedCustomRole(t, "Spare", []string{"task:view"})
	spareID := parseRoleID(t, spare)
	mgrRoleID := parseRoleID(t, w.mgrRole)
	ownerRoleID := parseRoleID(t, w.ownerRole)
	viewerRoleID := parseRoleID(t, w.viewerRole)
	var unknownRole string
	if err := w.owner.QueryRow(w.ctx, `SELECT uuidv7()::text`).Scan(&unknownRole); err != nil {
		t.Fatal(err)
	}
	unknownID := parseRoleID(t, unknownRole)

	for name, hash := range map[string][]byte{"nil": nil, "unknown": {99, 99}} {
		if _, err := store.CreateRole(w.ctx, hash, "Nil", []string{"task:view"}); !errors.Is(err, app.ErrNotSignedIn) {
			t.Fatalf("%s create: %v, want not signed in", name, err)
		}
		if err := store.ChangeRole(w.ctx, hash, spareID, "Spare", []string{"task:view"}); !errors.Is(err, app.ErrNotSignedIn) {
			t.Fatalf("%s change: %v, want not signed in", name, err)
		}
		if err := store.DeleteRole(w.ctx, hash, spareID); !errors.Is(err, app.ErrNotSignedIn) {
			t.Fatalf("%s delete: %v, want not signed in", name, err)
		}
	}

	if _, err := store.CreateRole(w.ctx, w.memberSess, "Member made", []string{"task:view"}); !errors.Is(err, domain.ErrRolesOnly) {
		t.Fatalf("member create: %v, want roles only", err)
	}
	if err := store.ChangeRole(w.ctx, w.memberSess, spareID, "Spare", []string{"task:view"}); !errors.Is(err, domain.ErrRolesOnly) {
		t.Fatalf("member change: %v, want roles only", err)
	}
	if err := store.DeleteRole(w.ctx, w.memberSess, spareID); !errors.Is(err, domain.ErrRolesOnly) {
		t.Fatalf("member delete: %v, want roles only", err)
	}

	if _, err := store.CreateRole(w.ctx, w.adminSess, "", []string{"task:view"}); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("blank name: %v, want bad role", err)
	}
	if _, err := store.CreateRole(w.ctx, w.adminSess, "Bad", []string{"owners:manage"}); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("owners:manage: %v, want bad role", err)
	}
	if _, err := store.CreateRole(w.ctx, w.adminSess, "Bad", nil); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("nil permissions: %v, want bad role", err)
	}
	if err := store.ChangeRole(w.ctx, w.adminSess, spareID, "Spare", []string{"task:fly"}); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("unknown permission change: %v, want bad role", err)
	}

	if err := store.ChangeRole(w.ctx, w.adminSess, unknownID, "X", []string{"task:view"}); !errors.Is(err, domain.ErrNoSuchRole) {
		t.Fatalf("change unknown role: %v, want no such role", err)
	}
	if err := store.DeleteRole(w.ctx, w.adminSess, unknownID); !errors.Is(err, domain.ErrNoSuchRole) {
		t.Fatalf("delete unknown role: %v, want no such role", err)
	}

	if err := store.ChangeRole(w.ctx, w.adminSess, ownerRoleID, "Owner", []string{"task:view"}); !errors.Is(err, domain.ErrSystemRole) {
		t.Fatalf("change system role: %v, want system role", err)
	}
	if err := store.DeleteRole(w.ctx, w.adminSess, viewerRoleID); !errors.Is(err, domain.ErrSystemRole) {
		t.Fatalf("delete system role: %v, want system role", err)
	}

	if err := store.ChangeRole(w.ctx, w.mgrSess, mgrRoleID, "Manager", []string{"task:view"}); !errors.Is(err, domain.ErrSelf) {
		t.Fatalf("change own role: %v, want self", err)
	}
	if err := store.DeleteRole(w.ctx, w.mgrSess, mgrRoleID); !errors.Is(err, domain.ErrSelf) {
		t.Fatalf("delete own role: %v, want self", err)
	}

	if _, err := store.CreateRole(w.ctx, w.mgrSess, "Beyond", []string{"task:delete"}); !errors.Is(err, domain.ErrBeyondOwn) {
		t.Fatalf("create beyond own: %v, want beyond own", err)
	}
	if err := store.ChangeRole(w.ctx, w.mgrSess, spareID, "Spare", []string{"task:view", "task:delete"}); !errors.Is(err, domain.ErrBeyondOwn) {
		t.Fatalf("change beyond own: %v, want beyond own", err)
	}

	if _, err := store.CreateRole(w.ctx, w.adminSess, "Taken", []string{"task:view"}); err != nil {
		t.Fatalf("create taken: %v", err)
	}
	if _, err := store.CreateRole(w.ctx, w.adminSess, "Taken", []string{"task:edit"}); !errors.Is(err, domain.ErrRoleNameTaken) {
		t.Fatalf("create duplicate name: %v, want name taken", err)
	}

	held := w.seedCustomRole(t, "Held", []string{"task:view"})
	seedPerson(t, w.owner, w.ctx, "holder@example.com", held, true, false)
	var holders *domain.RoleHolders
	if err := store.DeleteRole(w.ctx, w.adminSess, parseRoleID(t, held)); !errors.As(err, &holders) || holders.Count != 1 {
		t.Fatalf("delete held role: %v, want one holder", err)
	}

	shared := w.seedCustomRole(t, "Shared", []string{"task:view"})
	if _, err := w.owner.Exec(w.ctx, `
		INSERT INTO identity.shares (object_type, object_id, grantee_role_id, level, granted_by)
		VALUES ('task', uuidv7(), $1, 'viewer', $2)`, shared, w.firstOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRole(w.ctx, w.adminSess, parseRoleID(t, shared)); !errors.Is(err, domain.ErrRoleShared) {
		t.Fatalf("delete shared role: %v, want shared", err)
	}

	if err := store.ChangeRole(w.ctx, w.adminSess, spareID, "Spare", []string{"task:view", "task:edit"}); err != nil {
		t.Fatalf("change: %v", err)
	}
	if err := store.DeleteRole(w.ctx, w.adminSess, spareID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if rows := countRoles(t, w.owner, w.ctx, spare); rows != 0 {
		t.Fatal("the role survived its delete")
	}
}

func assertPgCode(t *testing.T, label string, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("%s: err = %v, want SQLSTATE %s", label, err, code)
	}
}

func assertGrantOutcome(t *testing.T, label string, want bool, err error) {
	t.Helper()
	if want && err != nil {
		t.Fatalf("%s: %v, want success", label, err)
	}
	if !want {
		assertDenied(t, label, err)
	}
}

func parseRoleID(t *testing.T, id string) domain.RoleID {
	t.Helper()
	parsed, err := domain.ParseRoleID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func countRoles(t *testing.T, pool *pgxpool.Pool, ctx context.Context, id string) int {
	t.Helper()
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.roles WHERE id = $1`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}
