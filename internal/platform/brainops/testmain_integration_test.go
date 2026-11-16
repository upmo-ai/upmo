//go:build integration

package brainops

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.RunSeeded(m, func(ctx context.Context, pool *pgxpool.Pool) error {
		return migrate.Up(ctx, pool, identity.Schema, identity.Migrations())
	}))
}
