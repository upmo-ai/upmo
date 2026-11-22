package app

import (
	"context"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Statuses hears how each provider attempt ended, by credential ref, with the
// time the attempt started so the store can drop an outcome that an edit made
// stale while it ran. Note must return at once: a status never holds up a
// model call.
type Statuses interface {
	Note(ref string, class domain.FailureClass, started time.Time)
}

// ClassStatus is the account status an attempt outcome leaves behind. It is
// empty for an outcome that says nothing about the account, such as a caller
// fault or a canceled call.
func ClassStatus(class domain.FailureClass) string {
	switch class {
	case domain.ClassOK:
		return domain.StatusConnected
	case domain.ClassCredential:
		return domain.StatusKeyRejected
	case domain.ClassRateLimit:
		return domain.StatusRateLimited
	case domain.ClassTransient, domain.ClassTimeout, domain.ClassOverloaded:
		return domain.StatusOffline
	default:
		return ""
	}
}

// StatusRecorder stores account statuses off the calling path. It queues what
// it hears and drops it when the queue is full. Whether a status is worth
// writing is the store's call, made where the account's row is known: the
// store repeats an unchanged status only after a minute, so a busy account
// costs a write a minute, and an edit that resets the account is heard at
// once. A cache here could not do either, since the account is edited in
// another process.
type StatusRecorder struct {
	write func(ctx context.Context, ref, status string, started time.Time) error
	queue chan statusNote
	done  chan struct{}
	once  sync.Once
}

type statusNote struct {
	ref, status string
	started     time.Time
}

// NewStatusRecorder starts a recorder that stores through write. Stop it when
// the process ends.
func NewStatusRecorder(write func(ctx context.Context, ref, status string, started time.Time) error) *StatusRecorder {
	r := &StatusRecorder{
		write: write,
		queue: make(chan statusNote, 64),
		done:  make(chan struct{}),
	}
	go r.run()
	return r
}

// Stop ends the writer. Notes after Stop are dropped.
func (r *StatusRecorder) Stop() { r.once.Do(func() { close(r.done) }) }

// Note queues the status the outcome implies, with the time the attempt
// started. It never blocks.
func (r *StatusRecorder) Note(ref string, class domain.FailureClass, started time.Time) {
	status := ClassStatus(class)
	if r == nil || ref == "" || status == "" {
		return
	}
	select {
	case r.queue <- statusNote{ref: ref, status: status, started: started}:
	default:
	}
}

func (r *StatusRecorder) run() {
	for {
		select {
		case <-r.done:
			return
		case n := <-r.queue:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// Best-effort: an outcome that fails to store is lost, and the
			// next one is not held back for it.
			_ = r.write(ctx, n.ref, n.status, n.started)
			cancel()
		}
	}
}
