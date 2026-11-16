package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// schemaName is a Postgres identifier we interpolate ourselves, so it has to
// be a plain name. Product SQL stays schema-qualified and does not use
// search_path.
var schemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// KnownSchemas is the fixed allowlist of schemas whose Goose migration heads
// SchemaVersions reports when present.
var KnownSchemas = []string{
	"audit",
	"identity",
	"memory",
	"knowledge",
	"tasks",
	"chat",
	"decisions",
	"artifacts",
	"runtime",
	"gateway",
	"notices",
	"agents",
	"platform",
}

// SchemaVersions returns the applied head migration version for each schema in
// KnownSchemas whose goose_db_version table exists.
func SchemaVersions(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, error) {
	return schemaVersions(ctx, pool, KnownSchemas)
}

func schemaVersions(ctx context.Context, pool *pgxpool.Pool, allow []string) (map[string]int64, error) {
	allowed := make(map[string]bool, len(allow))
	for _, s := range allow {
		if !schemaName.MatchString(s) {
			return nil, fmt.Errorf("migrate: schema name %q", s)
		}
		allowed[s] = true
	}
	rows, err := pool.Query(ctx, `
		SELECT table_schema
		FROM information_schema.tables
		WHERE table_name = 'goose_db_version'
		  AND table_schema = ANY($1)
		ORDER BY table_schema`, allow)
	if err != nil {
		return nil, fmt.Errorf("migrate: list version tables: %w", err)
	}
	var present []string
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			rows.Close()
			return nil, fmt.Errorf("migrate: scan schema: %w", err)
		}
		if allowed[schema] {
			present = append(present, schema)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("migrate: list version tables: %w", err)
	}
	rows.Close()

	versions := make(map[string]int64, len(present))
	for _, schema := range present {
		var version int64
		// schema is checked against the fixed allowlist and sanitized as an identifier.
		query := "SELECT COALESCE(max(version_id), 0) FROM " + pgx.Identifier{schema}.Sanitize() + ".goose_db_version WHERE is_applied AND version_id > 0"
		if err := pool.QueryRow(ctx, query).Scan(&version); err != nil {
			return nil, fmt.Errorf("migrate: version %s: %w", schema, err)
		}
		versions[schema] = version
	}
	return versions, nil
}

// Up creates the schema, then applies that context's migrations and nothing
// else. The version table is <schema>.goose_db_version.
func Up(ctx context.Context, pool *pgxpool.Pool, schema string, files fs.FS) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", int64(7480197)); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", int64(7480197)) }()
	if err := createSchema(ctx, conn, schema); err != nil {
		return err
	}

	provider, db, err := open(ctx, pool, schema, files)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: up %s: %w", schema, err)
	}
	return nil
}

// Down runs the migrations back to zero, then drops the schema so the version
// table goes with it.
func Down(ctx context.Context, pool *pgxpool.Pool, schema string, files fs.FS) error {
	provider, db, err := open(ctx, pool, schema, files)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := provider.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("migrate: down %s: %w", schema, err)
	}
	if _, err := pool.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		return fmt.Errorf("migrate: drop schema %s: %w", schema, err)
	}
	return nil
}

func open(ctx context.Context, pool *pgxpool.Pool, schema string, files fs.FS) (*goose.Provider, *sql.DB, error) {
	if !schemaName.MatchString(schema) {
		return nil, nil, fmt.Errorf("migrate: schema name %q", schema)
	}
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		files,
		goose.WithTableName(schema+".goose_db_version"),
	)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("migrate: provider %s: %w", schema, err)
	}
	return provider, db, nil
}

func createSchema(ctx context.Context, conn *pgxpool.Conn, schema string) error {
	if !schemaName.MatchString(schema) {
		return fmt.Errorf("migrate: schema name %q", schema)
	}
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil {
		return fmt.Errorf("migrate: create schema %s: %w", schema, err)
	}
	return nil
}
