//go:build integration

package gateway

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// An account shared with a team points at identity's teams, so every test
// database starts with identity migrated, as it is where brain runs. The
// provider store records audit events, so the audit schema comes up first.
func TestMain(m *testing.M) {
	os.Exit(pgtest.RunSeeded(m, func(ctx context.Context, pool *pgxpool.Pool) error {
		if err := audit.Up(ctx, pool); err != nil {
			return err
		}
		return migrate.Up(ctx, pool, identity.Schema, identity.Migrations())
	}))
}
