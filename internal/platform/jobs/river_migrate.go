package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

func migrateRiver(ctx context.Context, pool *pgxpool.Pool) error {
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: Schema})
	if err != nil {
		return err
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
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
