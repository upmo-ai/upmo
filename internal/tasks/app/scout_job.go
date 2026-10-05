package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/tasks"
)

type scoutWorker struct {
	river.WorkerDefaults[tasks.ScoutJobArgs]
	deps ScoutDeps
}

// AddScoutWorker registers the proposal scout worker on workers.
func AddScoutWorker(workers *river.Workers, deps ScoutDeps) {
	river.AddWorker(workers, &scoutWorker{deps: deps})
}

func (w *scoutWorker) Work(ctx context.Context, job *river.Job[tasks.ScoutJobArgs]) error {
	userID, err := uuid.Parse(job.Args.UserID)
	if err != nil {
		return fmt.Errorf("tasks: scout job: user: %w", err)
	}
	kept, audit, err := RunScout(ctx, w.deps, ScoutArgs{
		UserID:   userID,
		Trigger:  job.Args.Trigger,
		SourceID: job.Args.SourceID,
	})
	if len(audit) > 0 {
		slog.InfoContext(ctx, "proposal scout sources",
			"user_id", job.Args.UserID,
			"trigger", job.Args.Trigger,
			"source_id", job.Args.SourceID,
			"kept", kept,
			"read", audit,
		)
	}
	if IsRetry(err) {
		return err
	}
	return err
}
