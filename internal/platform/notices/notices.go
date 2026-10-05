// Package notices persists bell notifications. It is platform code, not a
// bounded context.
package notices

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/migrate"
)

// Schema is the Postgres schema for notification rows.
const Schema = "notices"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this package's Goose migration files.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("notices: embed migrations: " + err.Error())
	}
	return files
}

// Up applies Goose migrations in schema notices.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		return fmt.Errorf("notices: goose: %w", err)
	}
	return nil
}
