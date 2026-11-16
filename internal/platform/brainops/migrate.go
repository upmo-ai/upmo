// Package brainops provides internal database, migration, mail, and sealing
// helpers shared between cmd/brain and pkg/brain without exporting internal
// types on pkg/brain's public API.
package brainops

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"
)

// MigrationStep pairs a schema name with its Goose migration filesystem.
type MigrationStep struct {
	Schema string
	Files  fs.FS
}

// MigrationSteps returns the ordered schema migrations for brain.
func MigrationSteps() []MigrationStep {
	return []MigrationStep{
		{audit.Schema, audit.Migrations()},
		{identity.Schema, identity.Migrations()},
		{memory.Schema, memory.Migrations()},
		{knowledge.Schema, knowledge.Migrations()},
		{tasks.Schema, tasks.Migrations()},
		{chat.Schema, chat.Migrations()},
		{decisions.Schema, decisions.Migrations()},
		{artifacts.Schema, artifacts.Migrations()},
		{runtime.Schema, runtime.Migrations()},
		{gateway.Schema, gateway.Migrations()},
		{agents.Schema, agents.Migrations()},
	}
}

// MigrateAll applies every schema migration and records the startup system.migrated event.
func MigrateAll(ctx context.Context, owner *pgxpool.Pool) error {
	for _, step := range MigrationSteps() {
		if err := migrate.Up(ctx, owner, step.Schema, step.Files); err != nil {
			return err
		}
	}
	if err := jobs.Up(ctx, owner); err != nil {
		return err
	}
	if err := notices.Up(ctx, owner); err != nil {
		return err
	}
	return audit.Migrated(ctx, owner)
}

// SupportedSchemaVersions returns the maximum migration version supported by each schema.
func SupportedSchemaVersions() (map[string]int64, error) {
	steps := append(MigrationSteps(),
		MigrationStep{notices.Schema, notices.Migrations()},
		MigrationStep{jobs.Schema, jobs.Migrations()},
	)
	out := make(map[string]int64, len(steps))
	for _, step := range steps {
		entries, err := fs.ReadDir(step.Files, ".")
		if err != nil {
			return nil, fmt.Errorf("brain: read migrations for %s: %w", step.Schema, err)
		}
		var maxVer int64
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			prefix, _, ok := strings.Cut(entry.Name(), "_")
			if !ok {
				continue
			}
			ver, err := strconv.ParseInt(prefix, 10, 64)
			if err != nil {
				continue
			}
			if ver > maxVer {
				maxVer = ver
			}
		}
		out[step.Schema] = maxVer
	}
	return out, nil
}

var validSchemaIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// PrepareSchemasAt migrates schemas to target versions during backup restore.
func PrepareSchemasAt(ctx context.Context, owner *pgxpool.Pool, target map[string]int64) error {
	steps := append(MigrationSteps(), MigrationStep{notices.Schema, notices.Migrations()})
	for _, step := range steps {
		ver, ok := target[step.Schema]
		if !ok || ver <= 0 {
			continue
		}
		if err := migrateUpTo(ctx, owner, step.Schema, step.Files, ver); err != nil {
			return err
		}
	}
	if ver, ok := target[jobs.Schema]; ok && ver > 0 {
		if err := migrateUpTo(ctx, owner, jobs.Schema, jobs.Migrations(), ver); err != nil {
			return err
		}
		migrator, err := rivermigrate.New(riverpgxv5.New(owner), &rivermigrate.Config{Schema: jobs.Schema})
		if err != nil {
			return fmt.Errorf("backup: river migrator: %w", err)
		}
		if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
			return fmt.Errorf("backup: river migrate: %w", err)
		}
		if _, err := owner.Exec(ctx, `
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA platform TO upmo_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA platform TO upmo_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA platform GRANT USAGE, SELECT ON SEQUENCES TO upmo_app;
REVOKE ALL ON platform.goose_db_version FROM upmo_app;
REVOKE ALL ON platform.river_migration FROM upmo_app;
`); err != nil {
			return fmt.Errorf("backup: river grants: %w", err)
		}
	}
	return nil
}

func migrateUpTo(ctx context.Context, pool *pgxpool.Pool, schema string, files fs.FS, targetVersion int64) error {
	if !validSchemaIdent.MatchString(schema) {
		return fmt.Errorf("backup: invalid schema name %q", schema)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("backup: create schema %s: %w", schema, err)
	}
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		files,
		goose.WithTableName(schema+".goose_db_version"),
	)
	if err != nil {
		return fmt.Errorf("backup: goose provider %s: %w", schema, err)
	}
	if _, err := provider.UpTo(ctx, targetVersion); err != nil {
		return fmt.Errorf("backup: migrate %s to %d: %w", schema, targetVersion, err)
	}
	return nil
}
