package agents

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/agents/domain"
)

// InstalledPack summarizes an installed upmo.pack/v1 bundle.
type InstalledPack = postgres.InstalledPack

// InvalidError is a plain-language validation error from agents domain.
type InvalidError = domain.Invalid

// ErrPackDowngrade reports an attempt to install an older pack version over a newer one.
var ErrPackDowngrade = domain.ErrPackDowngrade

// InstallPack installs or updates a verified upmo.pack/v1 bundle idempotently
// using the schema owner pool.
func InstallPack(ctx context.Context, owner *pgxpool.Pool, pack Pack, keyID string, now time.Time) (InstalledPack, error) {
	return postgres.InstallPack(ctx, owner, pack, keyID, now)
}
