//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestPricesContextWindowMigrationAndCatalogRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: pool}
	inRate := 3.0
	outRate := 15.0
	rows := []domain.CatalogRow{
		{
			Model:         "claude-sonnet",
			Version:       "v7",
			Caps:          []string{domain.CapTools},
			ContextWindow: 200000,
			Price: domain.Price{
				Version:       "v7",
				InputPerMTok:  &inRate,
				OutputPerMTok: &outRate,
				ContextWindow: 200000,
			},
		},
		{
			Model:   "fallback-window",
			Version: "v7",
			Price: domain.Price{
				Version:       "v7",
				InputPerMTok:  &inRate,
				OutputPerMTok: &outRate,
			},
		},
	}
	if err := store.ReplaceCatalog(ctx, "v7", rows); err != nil {
		t.Fatal(err)
	}

	got, ok, err := store.Price(ctx, "claude-sonnet")
	if err != nil || !ok {
		t.Fatalf("Price(claude-sonnet) ok=%v err=%v", ok, err)
	}
	if got.ContextWindow != 200000 {
		t.Fatalf("claude-sonnet ContextWindow = %d, want 200000", got.ContextWindow)
	}

	def, ok, err := store.Price(ctx, "fallback-window")
	if err != nil || !ok {
		t.Fatalf("Price(fallback-window) ok=%v err=%v", ok, err)
	}
	if def.ContextWindow != domain.DefaultContextWindow {
		t.Fatalf("fallback ContextWindow = %d, want %d", def.ContextWindow, domain.DefaultContextWindow)
	}
}
