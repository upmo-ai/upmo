// Package stream fans a key's events out to the open streams of one process.
// It is platform code, not a bounded context, and knows nothing of what a
// key, an event or an access is.
package stream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrPerson is a person who already holds the most streams open.
	ErrPerson = errors.New("stream: too many open streams")
	// ErrFull is a process that already holds the most streams open.
	ErrFull = errors.New("stream: no room for another stream")
	// ErrClosed is a hub that has shut down.
	ErrClosed = errors.New("stream: streams are closed")
	// ErrDenied is a key this access may not read. Rows answer with it, or
	// with an error wrapping it, and the hub holds the stream for an access
	// check.
	ErrDenied = errors.New("stream: the access may not read the key")
)

// Why a stream ended. EndFinished is sent to the client as the end event, and
// so is EndAccess. The others close the stream without an end event, so the
// browser reconnects and resumes.
const (
	EndFinished = "finished"
	EndAccess   = "access"

	closeSlow     = "slow subscriber"
	closeShutdown = "shutdown"
	closeRefresh  = "access refresh failed"
)

// Tail is what a key gained after an event id, in one read.
type Tail[E, S any] struct {
	// Events are the key's events with an id after the cursor, in id order,
	// at most the limit asked for.
	Events []E
	// Summary is what the events belong to once they are read. It is read
	// before the events, so a finished one comes with all of the key's
	// events, and it is sent only when HasSummary reports one.
	Summary S
	// HasSummary reports whether the read has a summary at all.
	HasSummary bool
	// Finished reports that the key gains no more events, which ends its
	// streams.
	Finished bool
	// More reports events beyond the limit.
	More bool
}

// Rows is what the hub reads. Check is the access check the hub makes on
// every refresh; Tail is the one read per key and notification. Both answer
// ErrDenied, wrapped, for a key the access may not read.
type Rows[A, E, S any] interface {
	Check(ctx context.Context, access A, key string) error
	Tail(ctx context.Context, access A, key, after string, limit int) (Tail[E, S], error)
}

// Feed announces that a key has new events. Listen blocks until ctx ends. It
// calls notify with the key of every notification, and resync after every
// (re)connect, because a notification is not durable.
type Feed interface {
	Listen(ctx context.Context, notify func(key string), resync func())
}

// Renewer resolves an access again: fresh access, false when it is lost for
// good, and an error when that could not be found out.
type Renewer[A any] func(ctx context.Context) (A, bool, error)

// Config holds the limits of the hub. A zero field takes its default.
type Config struct {
	// PerPerson is the most streams one person holds open. Default 8.
	PerPerson int
	// PerProcess is the most streams the process holds open. Default 500.
	PerProcess int
	// Lifetime is the longest a stream stays open. Default 30 minutes.
	Lifetime time.Duration
	// Buffer is the messages a stream holds before its writer takes them.
	// Default 256, at least 4.
	Buffer int
	// Batch is the most events the hub reads for a key at once. Default 500.
	Batch int
	// Replay is how often the hub reads every key with a stream again, to
	// repair a notification that was lost. Default 5 seconds.
	Replay time.Duration
	// Refresh is how often a stream's access is checked again. Default 60
	// seconds.
	Refresh time.Duration
	// Stall is how long a stream may make no progress while events wait for it
	// before it is closed as slow. Default 10 seconds.
	Stall time.Duration
	// Heartbeat is the gap between comment lines on a quiet stream. Default 15
	// seconds.
	Heartbeat time.Duration
}

func (c Config) withDefaults() Config {
	if c.PerPerson <= 0 {
		c.PerPerson = 8
	}
	if c.PerProcess <= 0 {
		c.PerProcess = 500
	}
	if c.Lifetime <= 0 {
		c.Lifetime = 30 * time.Minute
	}
	if c.Buffer < 4 {
		c.Buffer = 256
	}
	if c.Batch <= 0 {
		c.Batch = 500
	}
	if c.Replay <= 0 {
		c.Replay = 5 * time.Second
	}
	if c.Refresh <= 0 {
		c.Refresh = 60 * time.Second
	}
	if c.Stall <= 0 {
		c.Stall = 10 * time.Second
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = 15 * time.Second
	}
	return c
}

// Kind tells the messages of a stream apart.
type Kind int

const (
	// KindEvent is one event.
	KindEvent Kind = iota + 1
	// KindSummary is the summary after the events of a change.
	KindSummary
	// KindEnd is the last message of a stream whose key is finished.
	KindEnd
)

// Message is one message for a stream's writer.
type Message[E, S any] struct {
	Kind    Kind
	Event   E
	Summary S
}

// Spec opens a stream. After is the id of the last event the client has; the
// hub sends everything newer. Renew may be nil, and then the access given is
// kept.
type Spec[A any] struct {
	Key    string
	Person string
	Access A
	Renew  Renewer[A]
	After  string
}

// Hub fans a key's events out to the open streams of one process. It reads
// the rows of a key once for all of that key's streams. Access is checked
// when a stream opens and again every Refresh.
type Hub[A, E, S any] struct {
	name string
	id   func(E) string
	rows Rows[A, E, S]
	feed Feed
	cfg  Config
	log  *slog.Logger

	mu       sync.Mutex
	byKey    map[string]map[*Stream[A, E, S]]struct{}
	byPerson map[string]int
	total    int
	dirty    map[string]struct{}
	closed   bool

	wake   chan struct{}
	probe  chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHub returns a hub that reads rows and hears of new events from feed.
// name is what the keys are called in log lines, and id returns an event's
// id, the cursor a stream keeps. A nil feed hears of none and relies on the
// replay timer. A nil log logs nothing.
func NewHub[A, E, S any](name string, id func(E) string, rows Rows[A, E, S], feed Feed, log *slog.Logger, cfg Config) *Hub[A, E, S] {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Hub[A, E, S]{
		name:     name,
		id:       id,
		rows:     rows,
		feed:     feed,
		cfg:      cfg.withDefaults(),
		log:      log,
		byKey:    map[string]map[*Stream[A, E, S]]struct{}{},
		byPerson: map[string]int{},
		dirty:    map[string]struct{}{},
		wake:     make(chan struct{}, 1),
		probe:    make(chan struct{}, 1),
	}
}

// Heartbeat is the gap between comment lines on a quiet stream.
func (h *Hub[A, E, S]) Heartbeat() time.Duration { return h.cfg.Heartbeat }

// Lifetime is the longest a stream stays open.
func (h *Hub[A, E, S]) Lifetime() time.Duration { return h.cfg.Lifetime }

// Open is the number of open streams.
func (h *Hub[A, E, S]) Open() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

// Start runs the hub until ctx ends or Close is called.
func (h *Hub[A, E, S]) Start(ctx context.Context) {
	h.mu.Lock()
	if h.closed || h.cancel != nil {
		h.mu.Unlock()
		return
	}
	ctx, h.cancel = context.WithCancel(ctx) //nolint:gosec // Close calls this cancel
	h.mu.Unlock()

	h.wg.Add(2)
	go func() {
		defer h.wg.Done()
		h.loop(ctx)
	}()
	go func() {
		defer h.wg.Done()
		h.refreshLoop(ctx)
	}()
	if h.feed != nil {
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			h.feed.Listen(ctx, h.notify, h.markAll)
		}()
	}
}

// Close closes every open stream without an end event, so each browser
// reconnects, and stops the hub. It is safe to call more than once.
func (h *Hub[A, E, S]) Close() {
	h.mu.Lock()
	h.closed = true
	for _, set := range h.byKey {
		for s := range set {
			h.dropLocked(s, closeShutdown)
		}
	}
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
}

// Subscribe opens a stream on a key the caller has already checked access
// to. The stream is sent every event after spec.After, then each new one.
func (h *Hub[A, E, S]) Subscribe(spec Spec[A]) (*Stream[A, E, S], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if h.byPerson[spec.Person] >= h.cfg.PerPerson {
		return nil, ErrPerson
	}
	if h.total >= h.cfg.PerProcess {
		return nil, ErrFull
	}
	now := time.Now()
	s := &Stream[A, E, S]{
		hub:       h,
		key:       spec.Key,
		person:    spec.Person,
		renew:     spec.Renew,
		out:       make(chan Message[E, S], h.cfg.Buffer),
		closed:    make(chan struct{}),
		access:    spec.Access,
		cursor:    spec.After,
		opened:    now,
		progress:  now,
		nextCheck: now.Add(h.cfg.Refresh),
	}
	set := h.byKey[s.key]
	if set == nil {
		set = map[*Stream[A, E, S]]struct{}{}
		h.byKey[s.key] = set
	}
	set[s] = struct{}{}
	h.byPerson[s.person]++
	h.total++
	h.dirty[s.key] = struct{}{}
	h.signal(h.wake)
	h.log.Info(h.name+" stream opened", h.name, s.key, "person", s.person)
	return s, nil
}

// Stream is one open stream. Its writer takes messages from Out until Closed
// is closed or an end message arrives, then calls Release.
type Stream[A, E, S any] struct {
	hub    *Hub[A, E, S]
	key    string
	person string
	renew  Renewer[A]
	out    chan Message[E, S]
	closed chan struct{}
	opened time.Time
	behind atomic.Bool

	// Guarded by hub.mu.
	access    A
	cursor    string
	progress  time.Time
	nextCheck time.Time
	suspect   bool
	gone      bool
	reason    string
}

// Out carries the messages in order.
func (s *Stream[A, E, S]) Out() <-chan Message[E, S] { return s.out }

// Closed is closed when the hub closes the stream. Reason then says why.
func (s *Stream[A, E, S]) Closed() <-chan struct{} { return s.closed }

// Reason is why the hub closed the stream. EndAccess means the writer owes
// the client an end event; any other reason closes without one.
func (s *Stream[A, E, S]) Reason() string {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.reason
}

// Taken tells the hub that the writer has taken a message. A stream that was
// left behind because its buffer was full gets the next events once there is
// room again.
func (s *Stream[A, E, S]) Taken() {
	if s.behind.Load() && len(s.out) <= cap(s.out)/2 {
		s.hub.kick(s.key)
	}
}

// Release removes the stream. reason is logged when the hub has not already
// closed it.
func (s *Stream[A, E, S]) Release(reason string) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s, reason)
}

func (h *Hub[A, E, S]) signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func (h *Hub[A, E, S]) kick(key string) {
	h.mu.Lock()
	if _, ok := h.byKey[key]; ok {
		h.dirty[key] = struct{}{}
	}
	h.mu.Unlock()
	h.signal(h.wake)
}

// notify marks a key whose streams must read new rows.
func (h *Hub[A, E, S]) notify(key string) { h.kick(key) }

// markAll marks every key that has a stream.
func (h *Hub[A, E, S]) markAll() {
	h.mu.Lock()
	for key := range h.byKey {
		h.dirty[key] = struct{}{}
	}
	h.mu.Unlock()
	h.signal(h.wake)
}

// removeLocked takes the stream out of the hub. Only the first call counts.
func (h *Hub[A, E, S]) removeLocked(s *Stream[A, E, S], reason string) {
	if s.gone {
		return
	}
	s.gone = true
	s.reason = reason
	if set := h.byKey[s.key]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.byKey, s.key)
		}
	}
	if h.byPerson[s.person]--; h.byPerson[s.person] <= 0 {
		delete(h.byPerson, s.person)
	}
	h.total--
	h.log.Info(h.name+" stream closed", h.name, s.key, "person", s.person, "reason", reason,
		"open_for", time.Since(s.opened).Round(time.Millisecond).String())
}

// dropLocked removes the stream and tells its writer to stop.
func (h *Hub[A, E, S]) dropLocked(s *Stream[A, E, S], reason string) {
	if s.gone {
		return
	}
	h.removeLocked(s, reason)
	close(s.closed)
}

func (h *Hub[A, E, S]) loop(ctx context.Context) {
	timer := time.NewTicker(h.cfg.Replay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			h.markAll()
		case <-h.wake:
		}
		h.mu.Lock()
		keys := make([]string, 0, len(h.dirty))
		for key := range h.dirty {
			keys = append(keys, key)
		}
		clear(h.dirty)
		h.mu.Unlock()
		for _, key := range keys {
			if ctx.Err() != nil {
				return
			}
			h.sync(ctx, key)
		}
	}
}

// sync reads what a key gained since its slowest stream, once, and hands it
// to every stream of the key. The read runs with the access of one stream. A
// stream whose access the rows refuse is held back until its access is
// checked again, and another stream's access reads instead.
func (h *Hub[A, E, S]) sync(ctx context.Context, key string) {
	for {
		h.mu.Lock()
		var reader *Stream[A, E, S]
		var access A
		from := ""
		for s := range h.byKey[key] {
			if s.suspect {
				continue
			}
			if reader == nil || s.cursor < from {
				from = s.cursor
			}
			if reader == nil {
				reader, access = s, s.access
			}
		}
		h.mu.Unlock()
		if reader == nil {
			return
		}

		tail, err := h.rows.Tail(ctx, access, key, from, h.cfg.Batch)
		if errors.Is(err, ErrDenied) {
			h.hold(reader)
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				h.log.Warn(h.name+" stream read failed", h.name, key, "err", err)
			}
			return
		}
		h.deliver(key, from, tail)
		return
	}
}

// hold stops sending to a stream until its access is checked, and asks for
// that check now.
func (h *Hub[A, E, S]) hold(s *Stream[A, E, S]) {
	h.mu.Lock()
	if !s.gone {
		s.suspect = true
		s.nextCheck = time.Time{}
	}
	h.mu.Unlock()
	h.signal(h.probe)
}

// deliver puts what a read found into the buffer of each stream of the key,
// as far as the buffer has room, and ends the streams of a finished key.
func (h *Hub[A, E, S]) deliver(key, from string, tail Tail[E, S]) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	progressed := false
	skipped := false
	for s := range h.byKey[key] {
		if s.suspect {
			continue
		}
		if s.cursor < from {
			// The stream opened after the read began and needs rows from
			// before it. The next read has them.
			skipped = true
			continue
		}
		room := cap(s.out) - len(s.out) - 2 // keep room for a summary and an end
		sent := 0
		behind := false
		for _, event := range tail.Events {
			id := h.id(event)
			if id <= s.cursor {
				continue
			}
			if room <= 0 {
				behind = true
				break
			}
			s.out <- Message[E, S]{Kind: KindEvent, Event: event}
			s.cursor = id
			room--
			sent++
		}
		s.behind.Store(behind)
		if sent > 0 {
			s.progress = now
			progressed = true
		}
		if behind {
			if sent == 0 && now.Sub(s.progress) > h.cfg.Stall {
				h.dropLocked(s, closeSlow)
			}
			continue
		}
		if tail.More {
			continue
		}
		if sent > 0 && tail.HasSummary {
			s.out <- Message[E, S]{Kind: KindSummary, Summary: tail.Summary}
		}
		if tail.Finished {
			s.out <- Message[E, S]{Kind: KindEnd}
			h.removeLocked(s, EndFinished)
		}
	}
	if skipped || (tail.More && progressed) {
		h.dirty[key] = struct{}{}
		h.signal(h.wake)
	}
}

func (h *Hub[A, E, S]) refreshLoop(ctx context.Context) {
	tick := min(max(h.cfg.Refresh/10, 20*time.Millisecond), time.Second)
	timer := time.NewTicker(tick)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-h.probe:
		}
		now := time.Now()
		h.mu.Lock()
		var due []*Stream[A, E, S]
		for _, set := range h.byKey {
			for s := range set {
				if !now.Before(s.nextCheck) {
					due = append(due, s)
				}
			}
		}
		h.mu.Unlock()
		for _, s := range due {
			if ctx.Err() != nil {
				return
			}
			h.check(ctx, s)
		}
	}
}

// check asks again whether the stream's person may still read the key: the
// person's access, then the key under the rows. A person who may not is sent
// an end event with reason access. A check that could not be made closes the
// stream without one, and the browser reconnects.
func (h *Hub[A, E, S]) check(ctx context.Context, s *Stream[A, E, S]) {
	h.mu.Lock()
	access := s.access
	h.mu.Unlock()

	if s.renew != nil {
		fresh, ok, err := s.renew(ctx)
		if err != nil {
			h.closeStream(ctx, s, closeRefresh)
			return
		}
		if !ok {
			h.closeStream(ctx, s, EndAccess)
			return
		}
		access = fresh
	}
	if err := h.rows.Check(ctx, access, s.key); err != nil {
		if errors.Is(err, ErrDenied) {
			h.closeStream(ctx, s, EndAccess)
		} else {
			h.closeStream(ctx, s, closeRefresh)
		}
		return
	}
	h.mu.Lock()
	wasSuspect := s.suspect
	if !s.gone {
		s.access = access
		s.suspect = false
		s.nextCheck = time.Now().Add(h.cfg.Refresh)
	}
	h.mu.Unlock()
	if wasSuspect {
		h.kick(s.key)
	}
}

func (h *Hub[A, E, S]) closeStream(ctx context.Context, s *Stream[A, E, S], reason string) {
	if ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	h.dropLocked(s, reason)
	h.mu.Unlock()
}
