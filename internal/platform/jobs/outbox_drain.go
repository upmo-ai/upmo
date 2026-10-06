package jobs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

const outboxDrainKind = "platform_outbox_drain"

// OutboxDrainArgs is the River payload that runs one outbox drain pass.
type OutboxDrainArgs struct{}

func (OutboxDrainArgs) Kind() string { return outboxDrainKind }

type outboxDrainWorker struct {
	river.WorkerDefaults[OutboxDrainArgs]
	pool *pgxpool.Pool
}

func (w *outboxDrainWorker) Work(ctx context.Context, _ *river.Job[OutboxDrainArgs]) error {
	_, err := DrainOnce(ctx, w.pool, defaultDrainLimit)
	return err
}

func outboxDrainPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(15*time.Second),
		func() (river.JobArgs, *river.InsertOpts) {
			return OutboxDrainArgs{}, nil
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}
