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

// CheckServingRole takes the genuine login and refuses every credential that
// would let a request step around row security: the owner's own login, a
// superuser session that switched to upmo_app, an upmo_app that has been made
// a member of the table owner, and a login that is not the expected role.
func TestCheckServingRole(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	var ownerRole string
	if err := owner.QueryRow(ctx, "SELECT current_user").Scan(&ownerRole); err != nil {
		t.Fatal(err)
	}
	if err := CheckServingRole(ctx, pgtest.AppLogin(t, owner), "UPMO_DATABASE_APP_URL", "upmo_app"); err != nil {
		t.Fatalf("genuine upmo_app login refused: %v", err)
	}
	// The planted bug: the request URL pointed at the owner credential.
	const bypass = "brain: UPMO_DATABASE_APP_URL logs in as a superuser, a BYPASSRLS role, or a role that can create roles or databases"
	if err := CheckServingRole(ctx, owner, "UPMO_DATABASE_APP_URL", "upmo_app"); err == nil || err.Error() != bypass {
		t.Fatalf("owner pool: %v, want %q", err, bypass)
	}
	// A superuser login that switched to upmo_app is still the superuser.
	if err := CheckServingRole(ctx, pgtest.App(t, owner), "UPMO_DATABASE_APP_URL", "upmo_app"); err == nil || err.Error() != bypass {
		t.Fatalf("SET ROLE pool: %v, want %q", err, bypass)
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
	const owns = "brain: UPMO_DATABASE_APP_URL logs in as a role that owns Upmo tables or may act as their owner"
	if err := CheckServingRole(ctx, pgtest.AppLogin(t, owner), "UPMO_DATABASE_APP_URL", "upmo_app"); err == nil || err.Error() != owns {
		t.Fatalf("member of the owner role: %v, want %q", err, owns)
	}
}

// An upmo_app holding a cluster-wide right is refused even though it is the
// right role and not a member of the owner: the right is restored before the
// test ends, the same way the member grant above is taken back.
func TestCheckServingRoleRefusesPrivilegedAppRole(t *testing.T) {
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
	err := CheckServingRole(ctx, pgtest.AppLogin(t, owner), "UPMO_DATABASE_APP_URL", "upmo_app")
	if err == nil || !strings.Contains(err.Error(), "can create roles or databases") {
		t.Fatalf("upmo_app with CREATEROLE passed the role check: %v", err)
	}
}

// A login that is not the expected role at all is refused on its name. The
// throwaway role is cluster-wide, so it is named after this test's own
// database and dropped before the test ends.
func TestCheckServingRoleRefusesStrangerLogin(t *testing.T) {
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
	err = CheckServingRole(ctx, pool, "UPMO_DATABASE_SANDBOX_URL", "upmo_sandbox")
	want := "brain: UPMO_DATABASE_SANDBOX_URL must log in as upmo_sandbox, not " + role
	if err == nil || err.Error() != want {
		t.Fatalf("stranger login: %v, want %q", err, want)
	}
}

// EnableLogin turns a migration's NOLOGIN role into a working login, is safe
// to run on every start, and refuses a URL that is not a password login for
// that role without naming the URL.
func TestEnableLogin(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for _, r := range []struct{ env, role string }{
		{"UPMO_DATABASE_APP_URL", "upmo_app"},
		{"UPMO_DATABASE_RUNNER_URL", "upmo_runner"},
		{"UPMO_DATABASE_GATEWAY_URL", "upmo_gateway"},
		{"UPMO_DATABASE_SANDBOX_URL", "upmo_sandbox"},
	} {
		// Start from the migration's state, with the role unable to log in.
		if _, err := owner.Exec(ctx, "ALTER ROLE "+r.role+" NOLOGIN"); err != nil {
			t.Fatal(err)
		}
		roleURL := dbURL(t, owner, url.UserPassword(r.role, r.role+"-test"))
		if err := EnableLogin(ctx, owner, r.env, r.role, roleURL); err != nil {
			t.Fatal(err)
		}
		if err := EnableLogin(ctx, owner, r.env, r.role, roleURL); err != nil {
			t.Fatalf("second EnableLogin %s: %v", r.role, err)
		}
		pool, err := pgxpool.New(ctx, roleURL)
		if err != nil {
			t.Fatal(err)
		}
		var session string
		err = pool.QueryRow(ctx, "SELECT session_user").Scan(&session)
		pool.Close()
		if err != nil || session != r.role {
			t.Fatalf("session_user = %s %v, want %s", session, err, r.role)
		}
		if err := EnableLogin(ctx, owner, r.env, r.role, dbURL(t, owner, url.User(r.role))); err == nil {
			t.Fatalf("%s URL without a password was accepted", r.env)
		}
		ownerURL := dbURL(t, owner, nil)
		err = EnableLogin(ctx, owner, r.env, r.role, ownerURL)
		if err == nil || strings.Contains(err.Error(), ownerURL) || !strings.Contains(err.Error(), r.env) {
			t.Fatalf("owner credential as %s: %v", r.env, err)
		}
	}
}

// OpenServingPool is what brain starts its pools with: the owner credential
// is refused, in every environment, and the app login serves.
func TestOpenServingPoolRefusesOwnerCredential(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()

	// The planted bug: UPMO_DATABASE_APP_URL set to the owner credential.
	// The pool opens and SET ROLE upmo_app even works, but the check refuses
	// it, so the brain stops instead of serving as the owner.
	if pool, err := OpenServingPool(ctx, "UPMO_DATABASE_APP_URL", dbURL(t, owner, nil), "upmo_app", "upmo_app"); err == nil {
		pool.Close()
		t.Fatal("request pool on the owner credential was accepted")
	} else if !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("owner credential: %v, want it to name BYPASSRLS", err)
	}

	pool, err := OpenServingPool(ctx, "UPMO_DATABASE_APP_URL", pgtest.AppLoginURL(t, owner), "upmo_app", "upmo_app")
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

// A pool that does not open fails without naming the URL: its parse error
// carries the URL, password included, and run logs whatever it is given. The
// port below cannot parse, and the marker rides in the password.
func TestOpenServingPoolFailureOmitsPassword(t *testing.T) {
	const marker = "open-marker-sesame"
	bad := "postgres://upmo_app:" + marker + "@127.0.0.1:notaport/upmo"
	_, err := RequestPool(context.Background(), bad)
	if err == nil {
		t.Fatal("malformed app URL was accepted")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("request pool error names the app URL password: %v", err)
	}
	if _, err := OpenServingPool(context.Background(), "UPMO_DATABASE_APP_URL", bad, "upmo_app", "upmo_app"); err == nil {
		t.Fatal("malformed app URL was accepted by OpenServingPool")
	} else if strings.Contains(err.Error(), marker) {
		t.Fatalf("open serving pool error names the app URL password: %v", err)
	}
}
