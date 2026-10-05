package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

// Client wraps River for transactional enqueue and in-process workers.
type Client struct {
	river *river.Client[pgx.Tx]
}

// Open builds a River client on pool. It registers the test-only ping worker;
// product kinds are registered in cmd/brain later.
func Open(pool *pgxpool.Pool) (*Client, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, pingWorker{})
	driver := riverpgxv5.New(pool)
	inner, err := river.NewClient(driver, &river.Config{
		Schema:  Schema,
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 2},
		},
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
