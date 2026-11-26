// Package jobs runs async work on River and persists cross-context events in
// platform.outbox.
package jobs

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/migrate"
)

// Schema is the Postgres schema for River tables and the outbox.
const Schema = "platform"

// WorkerSchema holds the River tables of the worker command's own client, so
// its leader election and periodic jobs never mix with brain's.
const WorkerSchema = "worker"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this package's Goose migration files.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("jobs: embed migrations: " + err.Error())
	}
	return files
}

// Up applies Goose migrations and River schema migrations in schema
// platform, then River's own migrations in WorkerSchema.
func Up(ctx context.Context, pool *pgxpool.Pool) error {
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		return fmt.Errorf("jobs: goose: %w", err)
	}
	if err := migrateRiver(ctx, pool); err != nil {
		return fmt.Errorf("jobs: river: %w", err)
	}
	if err := migrateWorkerRiver(ctx, pool); err != nil {
		return fmt.Errorf("jobs: worker river: %w", err)
	}
	return nil
}
