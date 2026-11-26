//go:build integration

package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func pgState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// The identity definers a serving role calls before a session exists, or
// instead of trusting upmo.user_id. None returns email or password_hash.
func TestIdentityDefiners(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	app := pgtest.AppLogin(t, owner)
	hashOf := func(s string) []byte { sum := sha256.Sum256([]byte(s)); return sum[:] }
	const sealed = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"
	var ada, bob string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at, webauthn_handle, password_hash)
		VALUES ('sealed-ada', $1, 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now(), 'ada-handle', $2)
		RETURNING id::text`, hashOf("ada@example.com"), sealed).Scan(&ada); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at, webauthn_handle)
		VALUES ('sealed-bob', $1, 'k1', 'Bob', (SELECT id FROM identity.roles WHERE key = 'member'), now(), 'bob-handle')
		RETURNING id::text`, hashOf("bob@example.com")).Scan(&bob); err != nil {
		t.Fatal(err)
	}

	// password_account: A's id and name for A's hashes, no sealed password.
	rows, err := app.Query(ctx, `SELECT * FROM identity.password_account($1::bytea[])`, [][]byte{hashOf("ada@example.com")})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range rows.FieldDescriptions() {
		names = append(names, f.Name)
	}
	var id, name string
	var found bool
	n := 0
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			if s, ok := v.(string); ok && strings.Contains(s, "$argon2id$") {
				t.Fatalf("password_account row holds the sealed password: %v", vals)
			}
		}
		if err := rows.Scan(&id, &name, &found); err != nil {
			t.Fatal(err)
		}
		n++
	}
	rows.Close()
	if rows.Err() != nil || n != 1 || id != ada || name != "Ada" || !found || strings.Join(names, ",") != "id,name,found" {
		t.Fatalf("password_account(A) = %d rows %s %s %t columns %v %v", n, id, name, found, names, rows.Err())
	}
	var none int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM identity.password_account($1::bytea[])`, [][]byte{hashOf("nobody@example.com")}).Scan(&none); err != nil || none != 0 {
		t.Fatalf("unknown hash = %d rows %v", none, err)
	}
	if err := app.QueryRow(ctx, `SELECT count(*) FROM identity.password_account($1::bytea[]) WHERE id = $2::uuid`, [][]byte{hashOf("ada@example.com")}, bob).Scan(&none); err != nil || none != 0 {
		t.Fatalf("password_account(A) returned B: %d %v", none, err)
	}
	if _, err := app.Exec(ctx, `SELECT * FROM identity.password_account($1::bytea)`, hashOf("ada@example.com")); pgState(err) != "42883" {
		t.Fatalf("one bytea argument = %v, want 42883 function not found", err)
	}

	// account_by_credential: A's id for A's credential and handle.
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.passkeys (user_id, credential_id, public_key, sign_count, aaguid, transports, backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1::uuid, 'ada-cred', 'pk', 0, '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', 'Laptop', now())`, ada); err != nil {
		t.Fatal(err)
	}
	var byCred string
	if err := app.QueryRow(ctx, `SELECT id::text FROM identity.account_by_credential('ada-cred', 'ada-handle')`).Scan(&byCred); err != nil || byCred != ada {
		t.Fatalf("account_by_credential = %q %v, want %s", byCred, err, ada)
	}
	if err := app.QueryRow(ctx, `SELECT count(*) FROM identity.account_by_credential('ada-cred', 'bob-handle')`).Scan(&none); err != nil || none != 0 {
		t.Fatalf("wrong handle = %d rows %v", none, err)
	}

	// has_password: the session's own person, found by the session hash.
	adaToken := hashOf("ada-session")
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1::uuid, $2, now(), now(), now() + interval '1 hour')`, ada, adaToken); err != nil {
		t.Fatal(err)
	}
	var has bool
	if err := app.QueryRow(ctx, `SELECT identity.has_password($1)`, adaToken).Scan(&has); err != nil || !has {
		t.Fatalf("has_password(A's session) = %t %v, want true", has, err)
	}
	if err := app.QueryRow(ctx, `SELECT identity.has_password($1)`, hashOf("no-session")).Scan(&has); pgState(err) != "42501" {
		t.Fatalf("has_password(unknown session) = %t %v, want 42501", has, err)
	}
	// upmo.user_id is not a session: B's id with A's hash still answers for A.
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, bob); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT identity.has_password($1)`, adaToken).Scan(&has); err != nil || !has {
		t.Fatalf("has_password with upmo.user_id = B: %t %v", has, err)
	}
	_ = tx.Rollback(ctx)

	// The time zone and the settings managers, as ids and one zone.
	var zone string
	if err := app.QueryRow(ctx, `SELECT identity.workspace_time_zone()`).Scan(&zone); err != nil || zone != "UTC" {
		t.Fatalf("workspace_time_zone = %q %v", zone, err)
	}
	managers, err := identity.SettingsManagers(ctx, app)
	if err != nil || len(managers) != 1 || managers[0] != ada {
		t.Fatalf("settings managers = %v %v, want [%s]", managers, err, ada)
	}
}
