package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
)

// StaleHoldAge is how long an unsettled hold may sit before the sweeper releases it.
const StaleHoldAge = 15 * time.Minute

// HoldSweepArgs is the River payload that releases stale usage holds once.
type HoldSweepArgs struct{}

func (HoldSweepArgs) Kind() string { return "gateway_hold_sweep" }

type holdSweepWorker struct {
	river.WorkerDefaults[HoldSweepArgs]
	owner *pgxpool.Pool
	log   *slog.Logger
}

// AddHoldSweeper registers the worker that releases stale gateway.usage_holds rows
// through postgres.Store.Release on the owner pool.
func AddHoldSweeper(workers *river.Workers, owner *pgxpool.Pool, log *slog.Logger) {
	river.AddWorker(workers, &holdSweepWorker{owner: owner, log: log})
}

func (w *holdSweepWorker) Work(ctx context.Context, _ *river.Job[HoldSweepArgs]) error {
	_, err := ReleaseStaleHoldsOnce(ctx, w.owner, time.Time{}, w.log)
	return err
}

// HoldSweepPeriodic runs the stale-hold sweep every five minutes, including at startup.
func HoldSweepPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(5*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return HoldSweepArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// ReleaseStaleHoldsOnce releases every hold older than StaleHoldAge and logs when any were removed.
func ReleaseStaleHoldsOnce(ctx context.Context, owner *pgxpool.Pool, now time.Time, log *slog.Logger) (int, error) {
	released, err := postgres.ReleaseStaleHolds(ctx, owner, now, StaleHoldAge)
	if err != nil {
		return 0, err
	}
	if released > 0 {
		logger := log
		if logger == nil {
			logger = slog.Default()
		}
		logger.InfoContext(ctx, "gateway stale holds released", "count", released)
	}
	return released, nil
}
