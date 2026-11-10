//go:build integration

package identity_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// upmo_app is cluster-wide while migrations are serialised per database only,
// so parallel test packages migrate fresh databases of one cluster at once.
func TestParallelMigrationsShareOneAppRole(t *testing.T) {
	const databases = 8
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	// An existing role would let the migrations pass without racing.
	probe := pgtest.New(t)
	var existing int
	if err := probe.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = 'upmo_app'`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing == 1 {
		t.Skip("the race is only exercised on a cluster where the upmo_app role does not exist yet")
	}

	pools := make([]*pgxpool.Pool, databases)
	for i := range pools {
		pools[i] = pgtest.New(t)
	}

	start := make(chan struct{})
	errs := make([]error, databases)
	var wg sync.WaitGroup
	for i, pool := range pools {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = migrate.Up(ctx, pool, identity.Schema, identity.Migrations())
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("database %d: %v", i, err)
		}
	}

	var roles int
	if err := pools[0].QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname = 'upmo_app'`).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if roles != 1 {
		t.Fatalf("upmo_app roles = %d, want 1", roles)
	}
}
