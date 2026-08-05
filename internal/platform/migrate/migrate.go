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
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil {
		return nil, nil, fmt.Errorf("migrate: create schema %s: %w", schema, err)
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
