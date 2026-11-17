package provision

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/identity"
)

// NonceSweepArgs is the River payload that runs one provision-nonce sweep.
type NonceSweepArgs struct{}

func (NonceSweepArgs) Kind() string { return "provision_nonce_sweep" }

type nonceSweepWorker struct {
	river.WorkerDefaults[NonceSweepArgs]
	store identity.ProvisionStore
}

// AddNonceSweep registers the worker that deletes consumed provision nonces
// past the replay window. It runs on the identity schema owner's store.
func AddNonceSweep(workers *river.Workers, store identity.ProvisionStore) {
	river.AddWorker(workers, &nonceSweepWorker{store: store})
}

func (w *nonceSweepWorker) Work(ctx context.Context, _ *river.Job[NonceSweepArgs]) error {
	_, err := w.store.SweepProvisionNonces(ctx, time.Now().UTC())
	return err
}

// NonceSweepPeriodic runs the nonce sweep at startup and then every
// 10 minutes.
func NonceSweepPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(10*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return NonceSweepArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}
