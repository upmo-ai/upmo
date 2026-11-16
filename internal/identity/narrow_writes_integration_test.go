//go:build integration

package identity_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
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

	_, err := appPool.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('rogue@example.com', 'Rogue', $1, now())`, memberRoleID)
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
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('lee@example.com', 'Lee', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}

	var deleteGrant bool
	if err := owner.QueryRow(ctx, `SELECT has_table_privilege('upmo_app', 'identity.users', 'DELETE')`).Scan(&deleteGrant); err != nil {
		t.Fatal(err)
	}
	if deleteGrant {
		t.Fatalf("identity.users DELETE grant = true, want false")
	}

	_, err := appPool.Exec(ctx, `DELETE FROM identity.users WHERE id = $1`, memberID)
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
		SELECT identity.member_invite(NULL, 'ada@example.com', 'Ada', NULL, NULL, $1, $2, $3)::text`,
		[]byte{1, 1, 1}, now, now.Add(time.Hour)).Scan(&firstOwnerID); err != nil {
		t.Fatalf("initial bootstrap: %v", err)
	}
	// Once at least one user exists, NULL actor hash must fail.
	_, err := appPool.Exec(ctx, `
		SELECT identity.member_invite(NULL, 'second@example.com', 'Second', NULL, NULL, $1, $2, $3)`,
		[]byte{2, 2, 2}, now, now.Add(time.Hour))
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

	seedPerson := func(email, roleID string, joined, disabled bool) string {
		var joinedAt, disabledAt any
		if joined {
			joinedAt = now
		}
		if disabled {
			disabledAt = now
		}
		var id string
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, name, role_id, invited_at, joined_at, disabled_at)
			VALUES ($1, 'Person', $2, $3, $4, $5)
			RETURNING id::text`, email, roleID, now, joinedAt, disabledAt).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	seedSess := func(userID string, hash []byte, expires time.Time) {
		if _, err := owner.Exec(ctx, `
			INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
			VALUES ($1, $2, now(), now(), $3)`, userID, hash, expires); err != nil {
			t.Fatal(err)
		}
	}

	adminID := seedPerson("admin@example.com", adminRoleID, true, false)
	memberID := seedPerson("member@example.com", memberRoleID, true, false)
	viewerID := seedPerson("viewer@example.com", viewerRoleID, true, false)
	customMgrID := seedPerson("mgr@example.com", customMgrRoleID, true, false)
	pendingAdminID := seedPerson("pending@example.com", adminRoleID, false, false)
	disabledAdminID := seedPerson("gone@example.com", adminRoleID, true, true)

	ownerSess := []byte{10, 1}
	adminSess := []byte{10, 2}
	memberSess := []byte{10, 3}
	customMgrSess := []byte{10, 4}
	expiredSess := []byte{10, 5}
	pendingSess := []byte{10, 6}
	disabledSess := []byte{10, 7}
	unknownSess := []byte{99, 99}

	seedSess(firstOwnerID, ownerSess, now.Add(time.Hour))
	seedSess(adminID, adminSess, now.Add(time.Hour))
	seedSess(memberID, memberSess, now.Add(time.Hour))
	seedSess(customMgrID, customMgrSess, now.Add(time.Hour))
	seedSess(firstOwnerID, expiredSess, now.Add(-time.Minute))
	seedSess(pendingAdminID, pendingSess, now.Add(time.Hour))
	seedSess(disabledAdminID, disabledSess, now.Add(time.Hour))

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

		_, err = appPool.Exec(ctx, `SELECT identity.member_invite($1, 'x@example.com', 'X', $2, $3, $4, $5, $6)`,
			badHash, memberRoleID, []byte{1}, []byte{2}, now, now.Add(time.Hour))
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
	_, err = appPool.Exec(ctx, `SELECT identity.member_invite($1, 'new@example.com', 'New', $2, $3, $4, $5, $6)`,
		memberSess, viewerRoleID, []byte{20}, []byte{21}, now, now.Add(time.Hour))
	assertDenied(t, "member cannot invite", err)

	_, err = appPool.Exec(ctx, `SELECT identity.member_invite($1, 'newowner@example.com', 'NewOwner', $2, $3, $4, $5, $6)`,
		adminSess, ownerRoleID, []byte{22}, []byte{23}, now, now.Add(time.Hour))
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
