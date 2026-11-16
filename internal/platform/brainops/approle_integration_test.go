//go:build integration

package brainops

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// dbURL rewrites the owner pool's URL onto the test database. With user set it
// is a login for that user; without it, it is the owner's own credential.
func dbURL(t *testing.T, owner *pgxpool.Pool, user *url.Userinfo) string {
	t.Helper()
	u, err := url.Parse(owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	if user != nil {
		u.User = user
	}
	u.Path = "/" + owner.Config().ConnConfig.Database
	return u.String()
}

// A genuine upmo_app login owns nothing: RESET ROLE gives nothing back, an
// owner-only object stays closed, and the owner role is out of reach. That is
// what the request pool depends on.
func TestAppLoginCannotReachOwner(t *testing.T) {
	owner := pgtest.New(t)
	app := pgtest.AppLogin(t, owner)
	ctx := context.Background()

	var session string
	if err := app.QueryRow(ctx, "SELECT session_user").Scan(&session); err != nil {
		t.Fatal(err)
	}
	if session != "upmo_app" {
		t.Fatalf("session_user = %s, want upmo_app", session)
	}

	if _, err := app.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
	var who string
	if err := app.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil {
		t.Fatal(err)
	}
	if who != "upmo_app" {
		t.Fatalf("current_user after RESET ROLE = %s, want upmo_app", who)
	}
	if _, err := app.Exec(ctx, "SELECT * FROM identity.goose_db_version"); err == nil {
		t.Fatal("upmo_app read identity.goose_db_version, which has no grant for it")
	}

	var ownerRole string
	if err := owner.QueryRow(ctx, "SELECT current_user").Scan(&ownerRole); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Exec(ctx, "SET ROLE "+ownerRole); err == nil {
		t.Fatalf("upmo_app could SET ROLE %s", ownerRole)
	}
}

// checkAppRole takes the genuine login and refuses the two credentials that
// would hand the request pool back to the owner: the owner's own login, and
// an upmo_app that has been made a member of the owner role.
func TestCheckAppRole(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	var ownerRole string
	if err := owner.QueryRow(ctx, "SELECT current_user").Scan(&ownerRole); err != nil {
		t.Fatal(err)
	}
	if err := checkAppRole(ctx, owner, pgtest.AppLogin(t, owner)); err != nil {
		t.Fatalf("genuine upmo_app login refused: %v", err)
	}
	// The planted bug: the request URL pointed at the owner credential.
	if err := checkAppRole(ctx, owner, owner); err == nil {
		t.Fatal("owner pool passed the app role check")
	}
	// The grant is cluster-wide, so it is taken back before the test ends.
	if _, err := owner.Exec(ctx, "GRANT "+ownerRole+" TO upmo_app"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), "REVOKE "+ownerRole+" FROM upmo_app"); err != nil {
			t.Errorf("revoke %s from upmo_app: %v", ownerRole, err)
		}
	})
	err := checkAppRole(ctx, owner, pgtest.AppLogin(t, owner))
	if err == nil || !strings.Contains(err.Error(), "member") {
		t.Fatalf("member of the owner role passed: %v", err)
	}
}

// An upmo_app holding a cluster-wide right is refused even though it is the
// right role and not a member of the owner: the right is restored before the
// test ends, the same way the member grant above is taken back.
func TestCheckAppRoleRefusesPrivilegedAppRole(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, "ALTER ROLE upmo_app CREATEROLE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), "ALTER ROLE upmo_app NOCREATEROLE"); err != nil {
			t.Errorf("restore upmo_app NOCREATEROLE: %v", err)
		}
	})
	err := checkAppRole(ctx, owner, pgtest.AppLogin(t, owner))
	if err == nil || !strings.Contains(err.Error(), "superuser or can create roles") {
		t.Fatalf("upmo_app with CREATEROLE passed the app role check: %v", err)
	}
}

// A login that is not upmo_app at all — not a superuser, not a member of the
// owner role — is refused on the session user alone, before the role's rights
// are looked at. The throwaway role is cluster-wide, so it is named after this
// test's own database and dropped before the test ends.
func TestCheckAppRoleRefusesStrangerLogin(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	role := "upmo_stranger_" + owner.Config().ConnConfig.Database
	var stmt string
	if err := owner.QueryRow(ctx,
		"SELECT format('CREATE ROLE %I LOGIN PASSWORD %L', $1::text, $2::text)", role, "stranger-pass",
	).Scan(&stmt); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, stmt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(), "DROP ROLE "+role); err != nil {
			t.Errorf("drop role %s: %v", role, err)
		}
	})
	pool, err := pgxpool.New(ctx, dbURL(t, owner, url.UserPassword(role, "stranger-pass")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	err = checkAppRole(ctx, owner, pool)
	if err == nil || !strings.Contains(err.Error(), "not upmo_app") {
		t.Fatalf("stranger login passed the app role check: %v", err)
	}
}

// EnableAppLogin turns the migration's NOLOGIN role into a working login, is
// safe to run on every start, and refuses a URL that is not a password login
// for upmo_app without naming the URL.
func TestEnableAppLogin(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	// Start from the migration's state, with the role unable to log in.
	if _, err := owner.Exec(ctx, "ALTER ROLE upmo_app NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	appURL := dbURL(t, owner, url.UserPassword("upmo_app", "upmo-app-test"))
	if err := EnableAppLogin(ctx, owner, appURL); err != nil {
		t.Fatal(err)
	}
	if err := EnableAppLogin(ctx, owner, appURL); err != nil {
		t.Fatalf("second EnableAppLogin: %v", err)
	}
	pool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var session string
	if err := pool.QueryRow(ctx, "SELECT session_user").Scan(&session); err != nil {
		t.Fatal(err)
	}
	if session != "upmo_app" {
		t.Fatalf("session_user = %s, want upmo_app", session)
	}

	if err := EnableAppLogin(ctx, owner, dbURL(t, owner, url.User("upmo_app"))); err == nil {
		t.Fatal("app URL without a password was accepted")
	}
	ownerURL := dbURL(t, owner, nil)
	err = EnableAppLogin(ctx, owner, ownerURL)
	if err == nil || strings.Contains(err.Error(), ownerURL) {
		t.Fatalf("owner credential as app URL: %v", err)
	}
}

// OpenAppPool is what run starts the request pool with: the owner credential
// in the app URL is refused, in every environment, and the app login serves.
func TestOpenAppPoolRefusesOwnerCredential(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()

	// The planted bug: UPMO_DATABASE_APP_URL set to the owner credential.
	// The pool opens and SET ROLE upmo_app even works, but the check refuses
	// it, so the brain stops instead of serving as the owner.
	if pool, err := OpenAppPool(ctx, owner, dbURL(t, owner, nil)); err == nil {
		pool.Close()
		t.Fatal("request pool on the owner credential was accepted")
	}

	pool, err := OpenAppPool(ctx, owner, pgtest.AppLoginURL(t, owner))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var who string
	if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil {
		t.Fatal(err)
	}
	if who != "upmo_app" {
		t.Fatalf("request pool current_user = %s, want upmo_app", who)
	}
}

// A request pool that does not open fails without naming the URL: its parse
// error carries the URL, password included, and run logs whatever it is
// given. The port below cannot parse, and the marker rides in the password.
func TestOpenAppPoolFailureOmitsPassword(t *testing.T) {
	owner := pgtest.New(t)
	const marker = "open-marker-sesame"
	bad := "postgres://upmo_app:" + marker + "@127.0.0.1:notaport/upmo"
	_, err := RequestPool(context.Background(), bad)
	if err == nil {
		t.Fatal("malformed app URL was accepted")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("request pool error names the app URL password: %v", err)
	}
	if _, err := OpenAppPool(context.Background(), owner, bad); err == nil {
		t.Fatal("malformed app URL was accepted by OpenAppPool")
	} else if strings.Contains(err.Error(), marker) {
		t.Fatalf("open app pool error names the app URL password: %v", err)
	}
}
