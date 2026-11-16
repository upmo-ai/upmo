package gateway

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Provisioning types and errors exposed on the gateway facade.
type (
	HouseProviderInput = postgres.HouseProviderInput
	HouseProvider      = postgres.HouseProvider
	UsageTotals        = postgres.UsageTotals
	UsageRow           = postgres.UsageRow
	MonthlyUsage       = postgres.MonthlyUsage
	InvalidError       = domain.Invalid
)

var (
	ErrHouseProviderConflict = postgres.ErrHouseProviderConflict
	ErrNoMasterKey           = domain.ErrNoMasterKey
)

// UpsertHouseProviders upserts managed house provider accounts using the
// owner pool and seals their API keys with keys.
func UpsertHouseProviders(ctx context.Context, owner *pgxpool.Pool, keys Keyring, inputs []HouseProviderInput, keyID string) ([]HouseProvider, error) {
	table := adapters()
	known := func(name string) bool {
		_, ok := table[name]
		return ok
	}
	return postgres.UpsertHouseProviders(ctx, owner, keys, known, inputs, keyID)
}

// QueryMonthlyUsage returns monthly token and micro-USD cost totals from
// gateway.ledger without any user_id or content fields.
func QueryMonthlyUsage(ctx context.Context, owner *pgxpool.Pool, month string, start, end time.Time) (MonthlyUsage, error) {
	return postgres.QueryMonthlyUsage(ctx, owner, month, start, end)
}
