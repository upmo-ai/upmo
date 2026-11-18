//go:build integration

package identity_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// secretColumns lists the columns upmo_app cannot read; every other column of
// the same table stays granted.
var secretColumns = map[string][]string{
	"sessions":            {"token_hash"},
	"api_tokens":          {"token_hash"},
	"sign_in_links":       {"token_hash"},
	"webauthn_challenges": {"session", "token_hash"},
}

func TestAppCannotReadSecretColumns(t *testing.T) {
	owner, ctx := upOwner(t)
	pool := pgtest.App(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	hash := []byte{1, 2, 3, 4}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour')`, person, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'probe', $2, '{}', now(), now() + interval '1 day')`, person, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'recover', now() + interval '1 hour')`, hash, person); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.webauthn_challenges (token_hash, session, purpose, user_id, expires_at)
		VALUES ($1, '{}', 'signin', $2, now() + interval '10 minutes')`, hash, person); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`SELECT token_hash FROM identity.sessions`,
		`SELECT token_hash FROM identity.api_tokens`,
		`SELECT token_hash FROM identity.sign_in_links`,
		`SELECT token_hash FROM identity.webauthn_challenges`,
		`SELECT session FROM identity.webauthn_challenges`,
	}
	for _, stmt := range statements {
		_, err := pool.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func TestAppReadsNonSecretColumns(t *testing.T) {
	owner, ctx := upOwner(t)
	pool := pgtest.App(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	hash := []byte{1, 2, 3, 4}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method, signed_in_at, user_agent)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour', 'passkey', now(), 'probe')`, person, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at, last_used_at)
		VALUES ($1, 'probe', $2, ARRAY['task:view'], now(), now() + interval '1 day', now())`, person, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at, used_at)
		VALUES ($1, $2, 'recover', now() + interval '1 hour', now())`, hash, person); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.webauthn_challenges (token_hash, session, purpose, user_id, expires_at)
		VALUES ($1, '{}', 'signin', $2, now() + interval '10 minutes')`, hash, person); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`SELECT id, user_id, created_at, last_seen_at, expires_at, sign_in_method, signed_in_at, user_agent FROM identity.sessions`,
		`SELECT id, user_id, name, scope, created_at, expires_at, last_used_at FROM identity.api_tokens`,
		`SELECT id, user_id, purpose, expires_at, used_at FROM identity.sign_in_links`,
		`SELECT purpose, user_id, expires_at FROM identity.webauthn_challenges`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func TestColumnGrantsCoverOnlyNonSecretColumns(t *testing.T) {
	owner, ctx := upOwner(t)
	for table, secrets := range secretColumns {
		checkColumnGrants(t, owner, ctx, table, secrets)
	}
}

// checkColumnGrants runs in its own scope so rows closes before the t.Fatal
// cleanups: an open rows pins a pool connection and pool.Close never returns.
func checkColumnGrants(t *testing.T, pool *pgxpool.Pool, ctx context.Context, table string, secrets []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT column_name,
		       has_column_privilege('upmo_app', 'identity.' || table_name, column_name, 'SELECT')
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
		if granted == slices.Contains(secrets, column) {
			t.Fatalf("identity.%s.%s select grant %t", table, column, granted)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, column := range secrets {
		if !seen[column] {
			t.Fatalf("identity.%s has no %s column", table, column)
		}
	}
}

func TestNewTablesGetNoSelectByDefault(t *testing.T) {
	owner, ctx := upOwner(t)
	pool := pgtest.App(t, owner)
	if _, err := owner.Exec(ctx, `CREATE TABLE identity.zz_probe (id int)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), `DROP TABLE identity.zz_probe`)
	})
	var selectGrant, insertGrant, updateGrant, deleteGrant bool
	if err := pool.QueryRow(ctx, `
		SELECT has_table_privilege('upmo_app', 'identity.zz_probe', 'SELECT'),
		       has_table_privilege('upmo_app', 'identity.zz_probe', 'INSERT'),
		       has_table_privilege('upmo_app', 'identity.zz_probe', 'UPDATE'),
		       has_table_privilege('upmo_app', 'identity.zz_probe', 'DELETE')`).Scan(&selectGrant, &insertGrant, &updateGrant, &deleteGrant); err != nil {
		t.Fatal(err)
	}
	if selectGrant || insertGrant || updateGrant || deleteGrant {
		t.Fatalf("probe privileges select=%t insert=%t update=%t delete=%t, want all false", selectGrant, insertGrant, updateGrant, deleteGrant)
	}
}

func TestLookupFunctionsServeTheApp(t *testing.T) {
	owner, ctx := upOwner(t)
	pool := pgtest.App(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	sessionHash := []byte{1, 1, 1, 1}
	tokenHash := []byte{2, 2, 2, 2}
	linkHash := []byte{3, 3, 3, 3}
	challengeHash := []byte{4, 4, 4, 4}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method, signed_in_at, user_agent)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour', 'passkey', now(), 'probe')`, person, sessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'probe', $2, ARRAY['task:view'], now(), now() + interval '1 day')`, person, tokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'recover', now() + interval '1 hour')`, linkHash, person); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.webauthn_challenges (token_hash, session, purpose, user_id, expires_at)
		VALUES ($1, '{}', 'signin', $2, now() + interval '10 minutes')`, challengeHash, person); err != nil {
		t.Fatal(err)
	}

	var sessionUser, roleID string
	if err := pool.QueryRow(ctx, `
		SELECT user_id::text, role_id::text FROM identity.session_find($1)`, sessionHash).Scan(&sessionUser, &roleID); err != nil {
		t.Fatal(err)
	}
	if sessionUser != person || roleID == "" {
		t.Fatalf("session_find user %s role %s", sessionUser, roleID)
	}
	if err := pool.QueryRow(ctx, `
		SELECT user_id FROM identity.session_find($1)`, []byte{9, 9, 9, 9}).Scan(&sessionUser); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown session hash: %v", err)
	}

	var tokenName, tokenUser, roleKey string
	var disabled, joined bool
	var permissions []string
	if err := pool.QueryRow(ctx, `
		SELECT name, person_id::text, disabled, joined, role_key, permissions
		FROM identity.token_find($1)`, tokenHash).Scan(&tokenName, &tokenUser, &disabled, &joined, &roleKey, &permissions); err != nil {
		t.Fatal(err)
	}
	if tokenName != "probe" || tokenUser != person || disabled || !joined || roleKey != "owner" || len(permissions) != 0 {
		t.Fatalf("token_find name %s user %s disabled %t joined %t role %s permissions %v", tokenName, tokenUser, disabled, joined, roleKey, permissions)
	}
	if err := pool.QueryRow(ctx, `
		SELECT name FROM identity.token_find($1)`, []byte{9, 9, 9, 9}).Scan(&tokenName); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown token hash: %v", err)
	}

	var linkUser, purpose string
	if err := pool.QueryRow(ctx, `
		SELECT user_id::text, purpose FROM identity.link_find($1, now())`, linkHash).Scan(&linkUser, &purpose); err != nil {
		t.Fatal(err)
	}
	if linkUser != person || purpose != "recover" {
		t.Fatalf("link_find user %s purpose %s", linkUser, purpose)
	}
	if err := pool.QueryRow(ctx, `
		SELECT user_id FROM identity.link_find($1, now())`, []byte{9, 9, 9, 9}).Scan(&linkUser); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown link hash: %v", err)
	}

	var spent bool
	if err := pool.QueryRow(ctx, `SELECT identity.link_spend($1, now())`, linkHash).Scan(&spent); err != nil || !spent {
		t.Fatalf("first link_spend %t %v", spent, err)
	}
	if err := pool.QueryRow(ctx, `SELECT identity.link_spend($1, now())`, linkHash).Scan(&spent); err != nil || spent {
		t.Fatalf("second link_spend %t %v", spent, err)
	}

	var challengeSession []byte
	var challengeUser string
	if err := pool.QueryRow(ctx, `
		SELECT session, purpose, user_id::text FROM identity.challenge_take($1, now())`, challengeHash).Scan(&challengeSession, &purpose, &challengeUser); err != nil {
		t.Fatal(err)
	}
	if string(challengeSession) != `{}` || purpose != "signin" || challengeUser != person {
		t.Fatalf("challenge_take session %s purpose %s user %s", challengeSession, purpose, challengeUser)
	}
	if err := pool.QueryRow(ctx, `
		SELECT session FROM identity.challenge_take($1, now())`, challengeHash).Scan(&challengeSession); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second challenge_take: %v", err)
	}

	// Expired rows: a link past its expiry is not spent and a challenge past
	// its expiry is not taken.
	expiredLink := []byte{5, 5, 5, 5}
	expiredChallenge := []byte{6, 6, 6, 6}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'recover', now() - interval '1 minute')`, expiredLink, person); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.webauthn_challenges (token_hash, session, purpose, user_id, expires_at)
		VALUES ($1, '{}', 'signin', $2, now() - interval '1 minute')`, expiredChallenge, person); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT identity.link_spend($1, now())`, expiredLink).Scan(&spent); err != nil || spent {
		t.Fatalf("expired link_spend %t %v", spent, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT user_id FROM identity.link_find($1, now())`, expiredLink).Scan(&linkUser); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired link_find: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT session FROM identity.challenge_take($1, now())`, expiredChallenge).Scan(&challengeSession); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired challenge_take: %v", err)
	}
}

func TestLookupFunctionsSkipDisabledPeople(t *testing.T) {
	owner, ctx := upOwner(t)
	pool := pgtest.App(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")
	seedOwner(t, owner, ctx, "grace@example.com")
	if _, err := owner.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, person); err != nil {
		t.Fatal(err)
	}
	sessionHash := []byte{5, 5, 5, 5}
	linkHash := []byte{6, 6, 6, 6}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method, signed_in_at, user_agent)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour', 'passkey', now(), 'probe')`, person, sessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'recover', now() + interval '1 hour')`, linkHash, person); err != nil {
		t.Fatal(err)
	}
	var found string
	if err := pool.QueryRow(ctx, `SELECT user_id::text FROM identity.session_find($1)`, sessionHash).Scan(&found); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("session_find for a disabled person: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT user_id::text FROM identity.link_find($1, now())`, linkHash).Scan(&found); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("link_find for a disabled person: %v", err)
	}
}

func TestLookupFunctionsRunAsDefiner(t *testing.T) {
	pool, ctx := up(t)
	for _, signature := range []string{
		"identity.session_find(bytea)",
		"identity.token_find(bytea)",
		"identity.link_find(bytea, timestamptz)",
		"identity.link_spend(bytea, timestamptz)",
		"identity.challenge_take(bytea, timestamptz)",
		"identity.member_set_role(bytea, uuid, uuid)",
		"identity.member_disable(bytea, uuid, timestamptz)",
		"identity.member_invite(bytea, text, bytea[], text, text, uuid, bytea, bytea, timestamptz, timestamptz)",
		"identity.token_create(bytea, text, bytea, text[], timestamptz, timestamptz)",
		"identity.role_create(bytea, text, text[])",
		"identity.role_change(bytea, uuid, text, text[])",
		"identity.role_delete(bytea, uuid)",
		"identity.sso_sign_in_state(uuid)",
	} {
		var definer, publicExec, appExec bool
		var config []string
		if err := pool.QueryRow(ctx, `
			SELECT prosecdef,
			       proconfig,
			       has_function_privilege('public', oid, 'EXECUTE'),
			       has_function_privilege('upmo_app', oid, 'EXECUTE')
			FROM pg_proc
			WHERE oid = $1::regprocedure`, signature).Scan(&definer, &config, &publicExec, &appExec); err != nil {
			t.Fatal(err)
		}
		if !definer || !slices.Contains(config, "search_path=pg_catalog") || publicExec || !appExec {
			t.Fatalf("%s definer=%t config=%v publicExec=%t appExec=%t", signature, definer, config, publicExec, appExec)
		}
	}
}
