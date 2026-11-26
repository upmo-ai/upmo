package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

func riverUp(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Schema: schema})
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// migrateWorkerRiver creates WorkerSchema and River's tables in it. No serving
// role gets a privilege there: only the owner's worker runs those jobs.
func migrateWorkerRiver(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+WorkerSchema); err != nil {
		return err
	}
	return riverUp(ctx, pool, WorkerSchema)
}

func migrateRiver(ctx context.Context, pool *pgxpool.Pool) error {
	if err := riverUp(ctx, pool, Schema); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA platform TO upmo_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA platform TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT USAGE, SELECT ON SEQUENCES TO upmo_app;
REVOKE ALL ON platform.goose_db_version FROM upmo_app;
REVOKE ALL ON platform.river_migration FROM upmo_app;
`); err != nil {
		return fmt.Errorf("river grants: %w", err)
	}
	return nil
}
