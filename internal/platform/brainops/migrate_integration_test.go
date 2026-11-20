//go:build integration

package brainops

import (
	"context"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/snapshot"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestFeature_UPM354_MigrateAllSeedsSnapshot(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.New(t)
	if err := MigrateAll(ctx, owner); err != nil {
		t.Fatalf("MigrateAll: %v", err)
	}
	_, snapAsOf, snapRows, err := snapshot.Load()
	if err != nil {
		t.Fatal(err)
	}
	var storedAsOf time.Time
	if err := owner.QueryRow(ctx, `SELECT as_of FROM gateway.catalog_meta WHERE id = 1`).Scan(&storedAsOf); err != nil {
		t.Fatal(err)
	}
	if !storedAsOf.UTC().Equal(snapAsOf) {
		t.Fatalf("catalog_meta.as_of = %v, want %v", storedAsOf, snapAsOf)
	}
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM gateway.prices`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(snapRows) {
		t.Fatalf("gateway.prices row count = %d, want %d", count, len(snapRows))
	}
}
