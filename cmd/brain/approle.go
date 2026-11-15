package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// enableAppLogin makes the app role a real login. The migration creates
// upmo_app NOLOGIN, because tests run it as the owner through a session role
// switch; the owner pool sets the password from the app URL instead, so the
// request pool can log in as upmo_app itself. A password cannot be a bind
// parameter in ALTER ROLE, so the statement is built server-side with format
// and neither it nor the password is ever logged. It runs on every start, so
// rotating the password in config is enough.
func enableAppLogin(ctx context.Context, owner *pgxpool.Pool, appURL string) error {
	cfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		// The parse error names the URL, which holds the password.
		return errors.New("brain: UPMO_DATABASE_APP_URL is not a Postgres URL")
	}
	if cfg.ConnConfig.User != "upmo_app" {
		return fmt.Errorf("brain: UPMO_DATABASE_APP_URL must log in as upmo_app, not %s", cfg.ConnConfig.User)
	}
	if cfg.ConnConfig.Password == "" {
		return fmt.Errorf("brain: UPMO_DATABASE_APP_URL needs a password")
	}
	var stmt string
	if err := owner.QueryRow(ctx,
		"SELECT format('ALTER ROLE upmo_app WITH LOGIN PASSWORD %L', $1::text)", cfg.ConnConfig.Password,
	).Scan(&stmt); err != nil {
		return fmt.Errorf("brain: app login: %w", err)
	}
	if _, err := owner.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("brain: app login: %w", err)
	}
	return nil
}

// openAppPool opens the request pool and refuses any credential that is not
// the app role, so the process never serves with a pool a role reset could
// walk back to the owner.
func openAppPool(ctx context.Context, owner *pgxpool.Pool, appURL string) (*pgxpool.Pool, error) {
	pool, err := requestPool(ctx, appURL)
	if err != nil {
		return nil, err
	}
	if err := checkAppRole(ctx, owner, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// checkAppRole refuses a request pool that did not log in as the app role:
// session_user must be upmo_app, hold no role- or database-wide rights, and
// not reach the owner role. It refuses in every environment; there is no
// switch that weakens it.
func checkAppRole(ctx context.Context, owner, app *pgxpool.Pool) error {
	var ownerRole string
	if err := owner.QueryRow(ctx, "SELECT current_user").Scan(&ownerRole); err != nil {
		return fmt.Errorf("brain: app role check: %w", err)
	}
	var (
		session    string
		privileged bool
		member     bool
	)
	err := app.QueryRow(ctx, `
		SELECT session_user,
		       rolsuper OR rolcreaterole OR rolcreatedb OR rolbypassrls,
		       pg_has_role(session_user, $1::name, 'MEMBER')
		FROM pg_roles
		WHERE rolname = session_user`, ownerRole).Scan(&session, &privileged, &member)
	if err != nil {
		return fmt.Errorf("brain: app role check: %w", err)
	}
	switch {
	case session != "upmo_app":
		return fmt.Errorf("brain: request pool logged in as %s, not upmo_app", session)
	case privileged:
		return fmt.Errorf("brain: upmo_app is a superuser or can create roles or databases or bypass row security")
	case member:
		return fmt.Errorf("brain: upmo_app is a member of the owner role %s", ownerRole)
	}
	return nil
}
