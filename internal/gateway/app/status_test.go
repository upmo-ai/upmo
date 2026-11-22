package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func TestClassStatus(t *testing.T) {
	tests := map[domain.FailureClass]string{
		domain.ClassOK:          domain.StatusConnected,
		domain.ClassCredential:  domain.StatusKeyRejected,
		domain.ClassRateLimit:   domain.StatusRateLimited,
		domain.ClassTransient:   domain.StatusOffline,
		domain.ClassTimeout:     domain.StatusOffline,
		domain.ClassOverloaded:  domain.StatusOffline,
		domain.ClassCaller:      "",
		domain.ClassCanceled:    "",
		domain.ClassModelAccess: "",
	}
	for class, want := range tests {
		if got := ClassStatus(class); got != want {
			t.Errorf("ClassStatus(%q) = %q, want %q", class, got, want)
		}
	}
}

type written struct{ ref, status string }

// sink collects writes and lets a test wait for the next one. tried marks a
// write attempt, landed or not.
type sink struct {
	mu    sync.Mutex
	got   []written
	fail  bool
	gate  chan struct{}
	seen  chan written
	tried chan struct{}
}

func newSink() *sink {
	return &sink{seen: make(chan written, 100), tried: make(chan struct{}, 100)}
}

func (s *sink) write(_ context.Context, ref, status string, _ time.Time) error {
	select {
	case s.tried <- struct{}{}:
	default:
	}
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("db down")
	}
	s.got = append(s.got, written{ref, status})
	s.seen <- written{ref, status}
	return nil
}

func (s *sink) next(t *testing.T) written {
	t.Helper()
	select {
	case w := <-s.seen:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("no status written")
		return written{}
	}
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func TestRecorderHandsEveryOutcomeToTheStore(t *testing.T) {
	out := newSink()
	rec := NewStatusRecorder(out.write)
	t.Cleanup(rec.Stop)

	// Every outcome that says something about the account reaches the store,
	// the same one twice included: whether a repeat is worth writing is the
	// store's call, made where the account's row is known.
	rec.Note("r1", domain.ClassOK, time.Now())
	if w := out.next(t); w != (written{"r1", domain.StatusConnected}) {
		t.Fatalf("write = %+v", w)
	}
	rec.Note("r1", domain.ClassOK, time.Now())
	if w := out.next(t); w != (written{"r1", domain.StatusConnected}) {
		t.Fatalf("write = %+v", w)
	}
	rec.Note("r1", domain.ClassCredential, time.Now())
	if w := out.next(t); w != (written{"r1", domain.StatusKeyRejected}) {
		t.Fatalf("write = %+v", w)
	}
	// An outcome that says nothing about the account writes nothing, and
	// neither does one without a ref.
	rec.Note("r1", domain.ClassCaller, time.Now())
	rec.Note("", domain.ClassOK, time.Now())
	time.Sleep(50 * time.Millisecond)
	if out.count() != 3 {
		t.Fatalf("writes = %d", out.count())
	}
}

func TestAFailedWriteDoesNotHoldTheNextOneBack(t *testing.T) {
	out := newSink()
	out.fail = true
	rec := NewStatusRecorder(out.write)
	t.Cleanup(rec.Stop)

	rec.Note("r1", domain.ClassOK, time.Now())
	<-out.tried
	// The failed outcome is lost and nothing remembers it as done, so the
	// next one is written.
	out.mu.Lock()
	out.fail = false
	out.mu.Unlock()
	rec.Note("r1", domain.ClassOK, time.Now())
	if w := out.next(t); w.ref != "r1" {
		t.Fatalf("write = %+v", w)
	}
}

func TestRecorderNeverBlocksTheCaller(t *testing.T) {
	out := newSink()
	out.gate = make(chan struct{})
	rec := NewStatusRecorder(out.write)
	t.Cleanup(func() { close(out.gate); rec.Stop() })

	done := make(chan struct{})
	go func() {
		for i := range 500 {
			rec.Note("ref"+string(rune('a'+i%26))+string(rune('a'+i/26%26)), domain.ClassOK, time.Now())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Note blocked while the writer was stuck")
	}
}

func TestTestLimiter(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	l := NewTestLimiter(10, time.Minute)
	l.Now = func() time.Time { return now }
	for i := range 10 {
		if !l.Allow("ada") {
			t.Fatalf("test %d refused", i+1)
		}
	}
	if l.Allow("ada") {
		t.Fatal("the 11th test inside a minute was allowed")
	}
	if !l.Allow("bob") {
		t.Fatal("one person's tests used up another's")
	}
	now = now.Add(59 * time.Second)
	if l.Allow("ada") {
		t.Fatal("allowed before the window passed")
	}
	now = now.Add(2 * time.Second)
	if !l.Allow("ada") {
		t.Fatal("still refused after the window passed")
	}
}
