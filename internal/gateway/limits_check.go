package gateway

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
)

// SpendMismatch describes one discrepancy between usage_spend and ledger sums.
type SpendMismatch = postgres.SpendMismatch

// SpendCheckArgs is the River payload that runs one nightly spend-vs-ledger check.
type SpendCheckArgs struct{}

func (SpendCheckArgs) Kind() string { return "gateway_spend_check" }

type spendCheckWorker struct {
	river.WorkerDefaults[SpendCheckArgs]
	owner    *pgxpool.Pool
	timeZone func(context.Context) (string, error)
	metrics  *metrics.Registry
	log      *slog.Logger
}

// AddSpendChecker registers the worker that compares gateway.usage_spend against
// charged_to_limit sums from gateway.ledger. It runs on owner, the pool of the
// role that owns the gateway tables.
func AddSpendChecker(workers *river.Workers, owner *pgxpool.Pool, timeZone func(context.Context) (string, error), reg *metrics.Registry, log *slog.Logger) {
	river.AddWorker(workers, &spendCheckWorker{
		owner:    owner,
		timeZone: timeZone,
		metrics:  reg,
		log:      log,
	})
}

func (w *spendCheckWorker) Work(ctx context.Context, _ *river.Job[SpendCheckArgs]) error {
	_, err := CheckSpendOnce(ctx, w.owner, w.timeZone, w.metrics, w.log)
	return err
}

// SpendCheckPeriodic runs the spend check at startup and then once a day.
func SpendCheckPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return SpendCheckArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// CheckSpendOnce runs one spend-vs-ledger check, logging and counting each
// mismatch without mutating usage_spend.
func CheckSpendOnce(ctx context.Context, owner *pgxpool.Pool, timeZone func(context.Context) (string, error), reg *metrics.Registry, log *slog.Logger) ([]SpendMismatch, error) {
	tz := "UTC"
	if timeZone != nil {
		loaded, err := timeZone(ctx)
		if err != nil {
			return nil, err
		}
		if loaded != "" {
			tz = loaded
		}
	}
	mismatches, err := postgres.CheckSpend(ctx, owner, tz)
	if err != nil {
		return nil, err
	}
	logger := log
	if logger == nil {
		logger = slog.Default()
	}
	for _, m := range mismatches {
		if m.SpendMicroUSD != m.LedgerMicroUSD {
			logger.ErrorContext(ctx, "gateway spend counter mismatch",
				"kind", m.Kind,
				"subject_id", m.SubjectID,
				"period_start", m.PeriodStart.Format("2006-01-02"),
				"spent_micro_usd", m.SpendMicroUSD,
				"ledger_micro_usd", m.LedgerMicroUSD,
			)
			reg.IncGatewaySpendMismatch(m.Kind)
		}
		if m.HeldMicroUSD > 0 {
			logger.ErrorContext(ctx, "gateway stuck hold on ended period",
				"kind", m.Kind,
				"subject_id", m.SubjectID,
				"period_start", m.PeriodStart.Format("2006-01-02"),
				"held_micro_usd", m.HeldMicroUSD,
			)
			reg.IncGatewaySpendMismatch(m.Kind)
		}
	}
	return mismatches, nil
}
