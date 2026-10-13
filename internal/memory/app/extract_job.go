package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/riverqueue/river"
)

type extractWorker struct {
	river.WorkerDefaults[ExtractJobArgs]
	store   Store
	propose func(userID string) Proposer
	embed   func(userID string) Embedder
}

// AddExtractWorker registers the memory extract worker on workers.
func AddExtractWorker(workers *river.Workers, store Store, propose func(userID string) Proposer, embed func(userID string) Embedder) {
	river.AddWorker(workers, &extractWorker{store: store, propose: propose, embed: embed})
}

func (w *extractWorker) Work(ctx context.Context, job *river.Job[ExtractJobArgs]) error {
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

// ExtractJobArgs is the River payload for one memory extract run.
type ExtractJobArgs struct {
	UserID     string `json:"user_id"`
	SourceKind string `json:"source_kind"`
	ID         string `json:"id"`
	Seq        int    `json:"seq"`
	Text       string `json:"text"`
	AtUnix     int64  `json:"at_unix"`
}

// Kind is the River job kind.
func (ExtractJobArgs) Kind() string { return "memory_extract" }
