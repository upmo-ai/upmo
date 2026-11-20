package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// AnonymousUserID is the user id an erased person's usage rows carry.
const AnonymousUserID = domain.AnonymousUserID

// UsageRollupArgs is the River payload that runs one daily usage roll-up.
type UsageRollupArgs struct{}

func (UsageRollupArgs) Kind() string { return "gateway_usage_rollup" }

type usageRollupWorker struct {
	river.WorkerDefaults[UsageRollupArgs]
	owner    *pgxpool.Pool
	timeZone func(context.Context) (string, error)
	log      *slog.Logger
}

// AddUsageRollup registers the worker that writes gateway.usage_daily from
// gateway.ledger. It runs on owner, the pool of the role that owns the
// gateway tables.
func AddUsageRollup(workers *river.Workers, owner *pgxpool.Pool, timeZone func(context.Context) (string, error), log *slog.Logger) {
	river.AddWorker(workers, &usageRollupWorker{owner: owner, timeZone: timeZone, log: log})
}

func (w *usageRollupWorker) Work(ctx context.Context, _ *river.Job[UsageRollupArgs]) error {
	return RollupUsageOnce(ctx, w.owner, w.timeZone, time.Now(), w.log)
}

// UsageRollupPeriodic runs the usage roll-up at startup and then once a day.
func UsageRollupPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return UsageRollupArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// RollupUsageOnce writes totals for closed days in the workspace time zone.
// It recomputes recent days and fills missing days after the watermark.
func RollupUsageOnce(ctx context.Context, owner *pgxpool.Pool, timeZone func(context.Context) (string, error), now time.Time, log *slog.Logger) error {
	tz, err := usageTimeZone(ctx, timeZone, log)
	if err != nil {
		return err
	}
	return postgres.RollupUsage(ctx, owner, tz, now)
}

// AnonymiseUser replaces userID with AnonymousUserID in the ledger and the
// daily totals in one transaction, keeping workspace totals unchanged. It
// refuses an empty or invalid id and the anonymous id itself, and is safe to
// run twice. It runs on owner, the pool of the role that owns the gateway
// tables. Spend counters and limits are left as they are.
func AnonymiseUser(ctx context.Context, owner *pgxpool.Pool, userID string) error {
	return postgres.AnonymiseUser(ctx, owner, userID)
}

// usageTimeZone returns the workspace time zone, or UTC when it is unset or
// not a zone this process knows.
func usageTimeZone(ctx context.Context, timeZone func(context.Context) (string, error), log *slog.Logger) (string, error) {
	tz := "UTC"
	if timeZone != nil {
		loaded, err := timeZone(ctx)
		if err != nil {
			return "", err
		}
		if loaded != "" {
			tz = loaded
		}
	}
	if _, err := time.LoadLocation(tz); err != nil {
		loggerOrDefault(log).WarnContext(ctx, "gateway usage falls back to UTC for an invalid workspace time zone", "time_zone", tz)
		return "UTC", nil
	}
	return tz, nil
}

func loggerOrDefault(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}
