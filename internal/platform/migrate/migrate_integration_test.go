//go:build integration

package migrate_test

import (
	"context"
	"embed"
	"io/fs"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

//go:embed testdata/one/*.sql
var oneFiles embed.FS

//go:embed testdata/two/*.sql
var twoFiles embed.FS

func TestOneProviderAppliesOnlyItsFiles(t *testing.T) {
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	one := mustSub(t, oneFiles, "testdata/one")
	two := mustSub(t, twoFiles, "testdata/two")
	if _, err := fs.Stat(two, "00001_mark.sql"); err != nil {
		t.Fatalf("second context fixture: %v", err)
	}
	if err := migrate.Up(ctx, pool, "alpha", one); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM one_mark`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	err := pool.QueryRow(ctx, `SELECT count(*) FROM two_mark`).Scan(&count)
	if err == nil {
		t.Fatal("two_mark exists; the second context's files were applied")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM alpha.goose_db_version`).Scan(&count); err != nil {
		t.Fatalf("version table: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'beta'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("beta schema was created")
	}

	if err := migrate.Down(ctx, pool, "alpha", one); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'alpha'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("alpha schema remains after down")
	}
}

func TestSchemaVersionsSkipsMissingAndReadsAppliedHead(t *testing.T) {
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	empty, err := migrate.SchemaVersions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty versions = %v, want none", empty)
	}

	one := mustSub(t, oneFiles, "testdata/one")
	if err := migrate.Up(ctx, pool, "audit", one); err != nil {
		t.Fatal(err)
	}
	versions, err := migrate.SchemaVersions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions["audit"] != 1 {
		t.Fatalf("versions = %v, want map[audit:1]", versions)
	}

	if err := migrate.Down(ctx, pool, "audit", one); err != nil {
		t.Fatal(err)
	}
	afterDown, err := migrate.SchemaVersions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterDown) != 0 {
		t.Fatalf("after down versions = %v, want none", afterDown)
	}
}

func mustSub(t *testing.T, files embed.FS, dir string) fs.FS {
	t.Helper()
	sub, err := fs.Sub(files, dir)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}
