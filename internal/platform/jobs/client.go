package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

// RescueStuckAfter is how long a job may sit in the running state before River
// takes it back and runs it again, which is what rescues a job whose process
// died. It is above the longest job timeout of any worker: the chat reply
// worker's five minutes; every other worker keeps River's one minute.
const RescueStuckAfter = 10 * time.Minute

// Client wraps River for transactional enqueue and in-process workers.
type Client struct {
	river *river.Client[pgx.Tx]
}

// Option configures Open.
type Option func(*openConfig)

type openConfig struct {
	workers  *river.Workers
	queues   map[string]river.QueueConfig
	periodic []*river.PeriodicJob
}

// WithWorkers uses w for job handlers. Open always registers the test-only ping
// worker and the outbox drain worker.
func WithWorkers(w *river.Workers) Option {
	return func(c *openConfig) {
		c.workers = w
	}
}

// WithQueue runs queue with its own pool of maxWorkers workers, so its jobs
// never wait behind those of another queue.
func WithQueue(queue string, maxWorkers int) Option {
	return func(c *openConfig) {
		c.queues[queue] = river.QueueConfig{MaxWorkers: maxWorkers}
	}
}

// WithPeriodic adds jobs that River inserts on a schedule, next to the outbox
// drain.
func WithPeriodic(jobs ...*river.PeriodicJob) Option {
	return func(c *openConfig) {
		c.periodic = append(c.periodic, jobs...)
	}
}

// Open builds a River client on pool. It registers the test-only ping worker
// and the outbox drain worker; product kinds are registered in cmd/brain.
func Open(pool *pgxpool.Pool, opts ...Option) (*Client, error) {
	cfg := &openConfig{
		workers: river.NewWorkers(),
		queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 2}},
	}
	for _, opt := range opts {
		opt(cfg)
	}
	workers := cfg.workers
	river.AddWorker(workers, pingWorker{})
	river.AddWorker(workers, &outboxDrainWorker{pool: pool})
	driver := riverpgxv5.New(pool)
	inner, err := river.NewClient(driver, &river.Config{
		Schema:               Schema,
		Workers:              workers,
		RescueStuckJobsAfter: RescueStuckAfter,
		Queues:               cfg.queues,
		PeriodicJobs:         append([]*river.PeriodicJob{outboxDrainPeriodic()}, cfg.periodic...),
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: client: %w", err)
	}
	return &Client{river: inner}, nil
}

// InsertTx enqueues a job in the same transaction as the caller's write.
func (c *Client) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return c.river.InsertTx(ctx, tx, args, opts)
}

// Start runs job workers until Stop or ctx cancellation.
func (c *Client) Start(ctx context.Context) error {
	return c.river.Start(ctx)
}

// Stop waits for workers to finish after a soft stop.
func (c *Client) Stop(ctx context.Context) error {
	return c.river.Stop(ctx)
}
