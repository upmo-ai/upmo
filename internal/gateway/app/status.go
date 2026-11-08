package app

import (
	"context"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Statuses hears how each provider attempt ended, by credential ref. Note must
// return at once: a status never holds up a model call.
type Statuses interface {
	Note(ref string, class domain.FailureClass)
}

// ClassStatus is the account status an attempt outcome leaves behind. It is
// empty for an outcome that says nothing about the account, such as a caller
// fault or a cancelled call.
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
// it hears, drops it when the queue is full, and writes an unchanged status
// again only after Refresh, so a busy account costs a write a minute.
type StatusRecorder struct {
	// Refresh is how long an unchanged status is trusted. Zero means a minute.
	Refresh time.Duration
	// Now is the clock. Nil reads the wall clock.
	Now func() time.Time

	write func(ctx context.Context, ref, status string) error
	queue chan statusNote
	done  chan struct{}
	once  sync.Once

	mu   sync.Mutex
	last map[string]statusMark
}

type statusNote struct{ ref, status string }

type statusMark struct {
	status string
	at     time.Time
}

// NewStatusRecorder starts a recorder that stores through write. Stop it when
// the process ends.
func NewStatusRecorder(write func(ctx context.Context, ref, status string) error) *StatusRecorder {
	r := &StatusRecorder{
		write: write,
		queue: make(chan statusNote, 64),
		done:  make(chan struct{}),
		last:  map[string]statusMark{},
	}
	go r.run()
	return r
}

// Stop ends the writer. Notes after Stop are dropped.
func (r *StatusRecorder) Stop() { r.once.Do(func() { close(r.done) }) }

// Note queues the status the outcome implies. It never blocks.
func (r *StatusRecorder) Note(ref string, class domain.FailureClass) {
	status := ClassStatus(class)
	if r == nil || ref == "" || status == "" {
		return
	}
	now := r.clock()
	refresh := r.Refresh
	if refresh <= 0 {
		refresh = time.Minute
	}
	r.mu.Lock()
	mark, seen := r.last[ref]
	if seen && mark.status == status && now.Sub(mark.at) < refresh {
		r.mu.Unlock()
		return
	}
	r.last[ref] = statusMark{status: status, at: now}
	r.mu.Unlock()
	select {
	case r.queue <- statusNote{ref: ref, status: status}:
	default:
		r.forget(ref)
	}
}

func (r *StatusRecorder) clock() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *StatusRecorder) forget(ref string) {
	r.mu.Lock()
	delete(r.last, ref)
	r.mu.Unlock()
}

func (r *StatusRecorder) run() {
	for {
		select {
		case <-r.done:
			return
		case n := <-r.queue:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := r.write(ctx, n.ref, n.status); err != nil {
				r.forget(n.ref)
			}
			cancel()
		}
	}
}
