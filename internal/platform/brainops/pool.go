package brainops

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
)

// OpenOwnerPool opens the owner pool with MaxConns=10, dials it once, and
// scrubs the URL from parse or connect errors.
func OpenOwnerPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	ownerConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, PoolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	ownerConfig.MaxConns = 10
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		return nil, PoolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	return pingOwner(ctx, owner)
}

// OpenCommandPool opens the owner pool for operator commands without exposing
// UPMO_DATABASE_URL in parse or connect errors. It dials once, so an
// unreachable database fails here.
func OpenCommandPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, PoolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	return pingOwner(ctx, pool)
}

// pingOwner dials the pool once so a URL that parses but cannot connect fails
// here. pgx names the user and host in a connect error, so the error is
// replaced, not wrapped.
func pingOwner(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Pool, error) {
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("brain: UPMO_DATABASE_URL could not be opened")
	}
	return pool, nil
}

// ParseListenConfig parses the database URL in envName for stream LISTEN
// connections and scrubs the URL from parse errors.
func ParseListenConfig(envName, databaseURL string) (*pgx.ConnConfig, error) {
	listenConn, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, PoolURLError(envName, databaseURL)
	}
	return listenConn, nil
}

// RequestPool opens connections that log in as upmo_app, which owns nothing,
// and sets that role again on every checkout.
func RequestPool(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	return rolePool(ctx, "UPMO_DATABASE_APP_URL", rawURL, "upmo_app")
}

// rolePool opens a pool on rawURL that sets role again on every checkout, or
// none when role is empty. It does not dial.
func rolePool(ctx context.Context, envName, rawURL, role string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		return nil, PoolURLError(envName, rawURL)
	}
	if role != "" {
		setRole := func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
			return err
		}
		cfg.AfterConnect = setRole
		cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
			err := setRole(ctx, conn)
			return err == nil, err
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, PoolURLError(envName, rawURL)
	}
	return pool, nil
}

// OpenServingPool opens the pool a serving process runs on, from the URL in
// envName, sets role on every checkout when role is set, and refuses it
// unless CheckServingRole passes. want, when set, is the only role it accepts.
func OpenServingPool(ctx context.Context, envName, rawURL, role, want string) (*pgxpool.Pool, error) {
	pool, err := rolePool(ctx, envName, rawURL, role)
	if err != nil {
		return nil, err
	}
	// pgx names the user and host in a connect error, so it is replaced.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("brain: %s could not be opened", envName)
	}
	if err := CheckServingRole(ctx, pool, envName, want); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// upmoSchemas are the schemas whose tables a serving role must neither own
// nor be able to act as the owner of.
var upmoSchemas = append(slices.Clone(migrate.KnownSchemas), jobs.WorkerSchema)

// CheckServingRole refuses a pool a request could use to step around row
// security: its login (session_user) or its current role is a superuser, has
// BYPASSRLS or may create roles or databases, or either owns, or may act as the owner of, a table in an
// Upmo schema. want, when set, is the only current role accepted. Brain runs
// it on both of its pools, gateway and sandboxd on theirs. No setting
// weakens it.
func CheckServingRole(ctx context.Context, pool *pgxpool.Pool, envName, want string) error {
	var (
		current, session string
		bypass, owns     bool
	)
	err := pool.QueryRow(ctx, `
		SELECT current_user, session_user,
		       EXISTS (
		         SELECT 1 FROM pg_roles AS r
		         WHERE r.rolname IN (current_user, session_user)
		           AND (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb)
		       ),
		       EXISTS (
		         SELECT 1
		         FROM pg_class AS c
		         JOIN pg_namespace AS n ON n.oid = c.relnamespace
		         WHERE n.nspname = ANY ($1)
		           AND c.relkind IN ('r', 'p')
		           AND (pg_has_role(current_user, c.relowner, 'MEMBER')
		             OR pg_has_role(session_user, c.relowner, 'MEMBER'))
		       )`, upmoSchemas).Scan(&current, &session, &bypass, &owns)
	if err != nil {
		return fmt.Errorf("brain: %s role check failed", envName)
	}
	switch {
	case bypass:
		return fmt.Errorf("brain: %s logs in as a superuser, a BYPASSRLS role, or a role that can create roles or databases", envName)
	case owns:
		return fmt.Errorf("brain: %s logs in as a role that owns Upmo tables or may act as their owner", envName)
	case want != "" && current != want:
		return fmt.Errorf("brain: %s must log in as %s, not %s", envName, want, current)
	}
	return nil
}

// AppPoolError returns a scrubbed error for UPMO_DATABASE_APP_URL.
func AppPoolError(appURL string) error {
	return PoolURLError("UPMO_DATABASE_APP_URL", appURL)
}

// PoolURLError reports a Postgres URL that could not be opened or parsed
// without exposing userinfo or password.
func PoolURLError(envName, rawURL string) error {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return fmt.Errorf("brain: %s could not be opened (host %s)", envName, u.Host)
	}
	return fmt.Errorf("brain: %s could not be opened", envName)
}

// EnableLogin makes role a real login with the password in the URL envName
// names. The migrations create every serving role NOLOGIN, because tests run
// them as the owner through a session role switch; the worker sets each
// password from its URL instead, so that process can log in as the role
// itself. A password cannot be a bind parameter in ALTER ROLE, so the
// statement is built server-side with format and neither it nor the password
// is ever logged. It runs on every worker start, so rotating the password in
// config is enough.
func EnableLogin(ctx context.Context, owner *pgxpool.Pool, envName, role, rawURL string) error {
	cfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		// The parse error names the URL, which holds the password.
		return fmt.Errorf("brain: %s is not a Postgres URL", envName)
	}
	if cfg.ConnConfig.User != role {
		return fmt.Errorf("brain: %s must log in as %s, not %s", envName, role, cfg.ConnConfig.User)
	}
	if cfg.ConnConfig.Password == "" {
		return fmt.Errorf("brain: %s needs a password", envName)
	}
	var stmt string
	if err := owner.QueryRow(ctx,
		"SELECT format('ALTER ROLE %I WITH LOGIN PASSWORD %L', $1::text, $2::text)", role, cfg.ConnConfig.Password,
	).Scan(&stmt); err != nil {
		return fmt.Errorf("brain: %s login: %w", role, err)
	}
	if _, err := owner.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("brain: %s login: %w", role, err)
	}
	return nil
}
