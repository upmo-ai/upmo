package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/memory"
)

type extractWorker struct {
	river.WorkerDefaults[memory.ExtractJobArgs]
	store   Store
	propose func(userID string) Proposer
	embed   func(userID string) Embedder
}

// AddExtractWorker registers the memory extract worker on workers.
func AddExtractWorker(workers *river.Workers, store Store, propose func(userID string) Proposer, embed func(userID string) Embedder) {
	river.AddWorker(workers, &extractWorker{store: store, propose: propose, embed: embed})
}

func (w *extractWorker) Work(ctx context.Context, job *river.Job[memory.ExtractJobArgs]) error {
	if w.store == nil || w.propose == nil || w.embed == nil {
		return fmt.Errorf("memory: extract worker deps")
	}
	args := job.Args
	at := time.Unix(args.AtUnix, 0).UTC()
	if args.AtUnix == 0 {
		at = time.Now().UTC()
	}
	proposer := w.propose(args.UserID)
	embedder := w.embed(args.UserID)
	if proposer == nil || embedder == nil {
		return fmt.Errorf("memory: extract worker gateway")
	}
	_, err := Extract(ctx, w.store, proposer, embedder, Source{
		UserID: args.UserID,
		Kind:   args.SourceKind,
		ID:     args.ID,
		Seq:    args.Seq,
		Text:   args.Text,
		At:     at,
	})
	if errors.Is(err, ErrRetry) {
		return err
	}
	return err
}
