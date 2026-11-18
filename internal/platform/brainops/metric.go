package brainops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
)

// RegisterUnknownKeyMetric wires the sealed-rows gauge to the three stores:
// every scrape re-counts the rows whose master key id the ring does not hold,
// within a 2-second budget, and sets the gauge for all three stores, zero
// included. A counting error leaves the last scrape's values in place.
func RegisterUnknownKeyMetric(reg *metrics.Registry, owner *pgxpool.Pool, ring gateway.Keyring) {
	reg.AddCollector(func(ctx context.Context) {
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		counts, err := UnknownKeyRows(qctx, owner, ring)
		if err != nil {
			return
		}
		for _, count := range counts {
			reg.SetSealedRowsUnknownKey(count.Store, float64(count.Unknown))
		}
	})
}
