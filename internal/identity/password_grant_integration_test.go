//go:build integration

package identity_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Invariant 8: upmo_app, logged in as itself, reads no stored password, not
// even whether one is set through a WHERE. It asks has_password_of, which
// answers a boolean for an enabled person and refuses anyone else.
func TestAppCannotReadAPasswordHash(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.AppLogin(t, owner)
	ada := seedOwner(t, owner, ctx, "ada@example.com")
	bob := seedOwner(t, owner, ctx, "bob@example.com")
	gone := seedOwner(t, owner, ctx, "gone@example.com")
	if _, err := owner.Exec(ctx, `UPDATE identity.users SET password_hash = 'k1:AAAA:BBBB:CCCC:DDDD' WHERE id = ANY($1::uuid[])`, []string{ada, gone}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE identity.users SET role_id = (SELECT id FROM identity.roles WHERE key = 'member') WHERE id = ANY($1::uuid[])`, []string{bob, gone}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`SELECT password_hash FROM identity.users`,
		`SELECT 1 FROM identity.users WHERE password_hash IS NOT NULL`,
		`SELECT * FROM identity.users`,
		`SELECT count(*) FROM identity.users WHERE password_hash LIKE 'k1:%'`,
	} {
		if _, err := app.Exec(ctx, stmt); sqlState(err) != "42501" {
			t.Errorf("%s = %v, want 42501", stmt, err)
		}
	}
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM (SELECT id, email_hash, name FROM identity.users) AS u`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("SELECT id, email_hash, name = %d %v", n, err)
	}
	for id, want := range map[string]bool{ada: true, bob: false} {
		var has bool
		if err := app.QueryRow(ctx, `SELECT identity.has_password_of($1)`, id).Scan(&has); err != nil || has != want {
			t.Errorf("has_password_of(%s) = %t %v, want %t", id, has, err, want)
		}
	}
	for name, id := range map[string]string{"disabled": gone, "unknown": "018f3b2c-7a10-7c4a-8e11-999999999999"} {
		var got any
		if err := app.QueryRow(ctx, `SELECT identity.has_password_of($1)`, id).Scan(&got); sqlState(err) != "42501" {
			t.Errorf("has_password_of for a %s person = %v %v, want 42501", name, got, err)
		}
	}
	// The definer returns a boolean only.
	var kind string
	if err := owner.QueryRow(ctx, `SELECT pg_get_function_result('identity.has_password_of(uuid)'::regprocedure)`).Scan(&kind); err != nil || kind != "boolean" {
		t.Fatalf("has_password_of returns %q %v", kind, err)
	}
}

// Invariant 5, the grants: no serving role touches the password check
// windows, which only the worker writes as the owner.
func TestPasswordWindowsBelongToTheOwner(t *testing.T) {
	owner, ctx := upOwner(t)
	subject := make([]byte, 32)
	if _, err := owner.Exec(ctx, `INSERT INTO identity.password_verify_window (subject, window_start, attempts) VALUES ($1, now(), 6)`, subject); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"upmo_app", "upmo_gateway", "upmo_sandbox", "upmo_runner"} {
		pool := pgtest.Login(t, owner, role)
		for _, stmt := range []string{
			`SELECT attempts FROM identity.password_verify_window`,
			`DELETE FROM identity.password_verify_window`,
			`UPDATE identity.password_verify_window SET attempts = 1`,
			`INSERT INTO identity.password_verify_window (subject, window_start, attempts) VALUES (decode(repeat('01', 32), 'hex'), now(), 1)`,
		} {
			if _, err := pool.Exec(ctx, stmt); sqlState(err) != "42501" {
				t.Errorf("%s: %s = %v, want 42501", role, stmt, err)
			}
		}
	}
	var attempts int
	if err := owner.QueryRow(ctx, `SELECT attempts FROM identity.password_verify_window WHERE subject = $1`, subject).Scan(&attempts); err != nil || attempts != 6 {
		t.Fatalf("attempts = %d %v, want 6 unchanged", attempts, err)
	}
	// The table checks its own shape.
	for _, stmt := range []string{
		`INSERT INTO identity.password_verify_window (subject, window_start, attempts) VALUES ('\x01', now(), 1)`,
		`INSERT INTO identity.password_verify_window (subject, window_start, attempts) VALUES (decode(repeat('02', 32), 'hex'), now(), 7)`,
		`INSERT INTO identity.password_verify_window (subject, window_start, attempts) VALUES (decode(repeat('03', 32), 'hex'), now(), 0)`,
	} {
		if _, err := owner.Exec(ctx, stmt); sqlState(err) != "23514" {
			t.Errorf("%s = %v, want 23514", stmt, err)
		}
	}
}

// Invariant 8 (4d2): upmo_app, logged in as itself, reads no stored
// address, not even through a WHERE. It still counts and names people, and
// a session setting does not narrow what it sees.
func TestAppCannotReadAnEmail(t *testing.T) {
	owner, ctx := upOwner(t)
	app := pgtest.AppLogin(t, owner)
	ada := seedOwner(t, owner, ctx, "ada@example.com")
	bob := seedOwner(t, owner, ctx, "bob@example.com")
	for _, stmt := range []string{
		`SELECT email FROM identity.users`,
		`SELECT password_hash FROM identity.users`,
		`SELECT id FROM identity.users WHERE email = 'ada@example.com'`,
		`SELECT count(*) FROM identity.users WHERE email LIKE '%@%'`,
		`SELECT * FROM identity.users`,
	} {
		if _, err := app.Exec(ctx, stmt); sqlState(err) != "42501" {
			t.Errorf("%s = %v, want 42501", stmt, err)
		}
	}
	var n int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM identity.users`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("count(*) = %d %v, want 2", n, err)
	}
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, bob); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.users`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("count(*) as %s = %d %v, want 2", bob, n, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var id, name string
	if err := app.QueryRow(ctx, `SELECT id::text, name FROM identity.users WHERE id = $1`, ada).Scan(&id, &name); err != nil || id != ada || name != "Ada" {
		t.Fatalf("SELECT id, name = %q %q %v, want %s Ada", id, name, err, ada)
	}
}
