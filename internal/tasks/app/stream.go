package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

var (
	// ErrStreamPerson is a person who already holds the most streams open.
	ErrStreamPerson = errors.New("tasks: too many open streams")
	// ErrStreamFull is a process that already holds the most streams open.
	ErrStreamFull = errors.New("tasks: no room for another stream")
	// ErrStreamClosed is a hub that has shut down.
	ErrStreamClosed = errors.New("tasks: streams are closed")
)

// Why a stream ended. Finished is sent to the client as the end event, and so
// is Access. The others close the stream without an end event, so the browser
// reconnects and resumes.
const (
	EndFinished = "finished"
	EndAccess   = "access"

	closeSlow     = "slow subscriber"
	closeShutdown = "shutdown"
	closeRefresh  = "access refresh failed"
)

// Tail is what a task gained after an event id, read in one transaction.
type Tail struct {
	// Events are the task's events and chat lines with an id after the cursor,
	// in id order, at most the limit asked for, mapped as the detail read model
	// maps them.
	Events []Event
	// State is the stored status, Status what a person sees and Phase the phase
	// shown. State is read before the events, so a Done or Failed State comes
	// with all of the task's events.
	State  domain.Status
	Status domain.Shown
	Phase  domain.Status
	// More reports events beyond the limit.
	More bool
}

// StreamRows is what the hub reads. Find is the access check; Tail is the
// one read per task and notification. Both answer ErrGone for a task the
// access cannot see.
type StreamRows interface {
	Finder
	Tail(ctx context.Context, access Access, taskID, after string, limit int) (Tail, error)
}

// StreamFeed announces that a task has new events. Listen blocks until ctx
// ends. It calls notify with the task id of every notification, and resync
// after every (re)connect, because a notification is not durable.
type StreamFeed interface {
	Listen(ctx context.Context, notify func(taskID string), resync func())
}

// Renewer resolves a person's access again: fresh access, false when the
// person has lost it for good (the session ended, the role went), and an
// error when that could not be found out.
type Renewer func(ctx context.Context) (Access, bool, error)

// StreamConfig holds the limits of the hub. A zero field takes its default.
type StreamConfig struct {
	// PerPerson is the most streams one person holds open. Default 8.
	PerPerson int
	// PerProcess is the most streams the process holds open. Default 500.
	PerProcess int
	// Lifetime is the longest a stream stays open. Default 30 minutes.
	Lifetime time.Duration
	// Buffer is the messages a stream holds before its writer takes them.
	// Default 256, at least 4.
	Buffer int
	// Batch is the most events the hub reads for a task at once. Default 500.
	Batch int
	// Replay is how often the hub reads every task with a stream from the
	// tables, to repair a notification that was lost. Default 5 seconds.
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

func (c StreamConfig) withDefaults() StreamConfig {
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

// StreamKind tells the messages of a stream apart.
type StreamKind int

const (
	// StreamLine is one event.
	StreamLine StreamKind = iota + 1
	// StreamStatus is the status and phase after the events of a change.
	StreamStatus
	// StreamEnd is the last message of a task that is Done or Failed.
	StreamEnd
)

// StreamMessage is one message for a stream's writer.
type StreamMessage struct {
	Kind   StreamKind
	Event  Event
	Status domain.Shown
	Phase  domain.Status
}

// StreamSpec opens a stream. After is the id of the last event the client has;
// the hub sends everything newer. Renew may be nil, and then the access given
// is kept.
type StreamSpec struct {
	TaskID string
	Person string
	Access Access
	Renew  Renewer
	After  string
}

// StreamHub fans task events out to the open streams of one process. It reads
// the rows of a task once for all of that task's streams. Access is checked
// when a stream opens and again every Refresh.
type StreamHub struct {
	rows StreamRows
	feed StreamFeed
	cfg  StreamConfig
	log  *slog.Logger

	mu       sync.Mutex
	byTask   map[string]map[*Stream]struct{}
	byPerson map[string]int
	total    int
	dirty    map[string]struct{}
	closed   bool

	wake   chan struct{}
	probe  chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewStreamHub returns a hub that reads rows and hears of new events from
// feed. A nil feed hears of none and relies on the replay timer. A nil log
// logs nothing.
func NewStreamHub(rows StreamRows, feed StreamFeed, log *slog.Logger, cfg StreamConfig) *StreamHub {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &StreamHub{
		rows:     rows,
		feed:     feed,
		cfg:      cfg.withDefaults(),
		log:      log,
		byTask:   map[string]map[*Stream]struct{}{},
		byPerson: map[string]int{},
		dirty:    map[string]struct{}{},
		wake:     make(chan struct{}, 1),
		probe:    make(chan struct{}, 1),
	}
}

// Heartbeat is the gap between comment lines on a quiet stream.
func (h *StreamHub) Heartbeat() time.Duration { return h.cfg.Heartbeat }

// Lifetime is the longest a stream stays open.
func (h *StreamHub) Lifetime() time.Duration { return h.cfg.Lifetime }

// Open is the number of open streams.
func (h *StreamHub) Open() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.total
}

// Start runs the hub until ctx ends or Close is called.
func (h *StreamHub) Start(ctx context.Context) {
	h.mu.Lock()
	if h.closed || h.cancel != nil {
		h.mu.Unlock()
		return
	}
	ctx, h.cancel = context.WithCancel(ctx)
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
func (h *StreamHub) Close() {
	h.mu.Lock()
	h.closed = true
	for _, set := range h.byTask {
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

// Subscribe opens a stream on a task the caller has already checked access
// to. The stream is sent every event after spec.After, then each new one.
func (h *StreamHub) Subscribe(spec StreamSpec) (*Stream, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrStreamClosed
	}
	if h.byPerson[spec.Person] >= h.cfg.PerPerson {
		return nil, ErrStreamPerson
	}
	if h.total >= h.cfg.PerProcess {
		return nil, ErrStreamFull
	}
	now := time.Now()
	s := &Stream{
		hub:       h,
		task:      spec.TaskID,
		person:    spec.Person,
		renew:     spec.Renew,
		out:       make(chan StreamMessage, h.cfg.Buffer),
		closed:    make(chan struct{}),
		access:    spec.Access,
		cursor:    spec.After,
		opened:    now,
		progress:  now,
		nextCheck: now.Add(h.cfg.Refresh),
	}
	set := h.byTask[s.task]
	if set == nil {
		set = map[*Stream]struct{}{}
		h.byTask[s.task] = set
	}
	set[s] = struct{}{}
	h.byPerson[s.person]++
	h.total++
	h.dirty[s.task] = struct{}{}
	h.signal(h.wake)
	h.log.Info("task stream opened", "task", s.task, "person", s.person)
	return s, nil
}

// Stream is one open stream. Its writer takes messages from Out until Closed
// is closed or an end message arrives, then calls Release.
type Stream struct {
	hub    *StreamHub
	task   string
	person string
	renew  Renewer
	out    chan StreamMessage
	closed chan struct{}
	opened time.Time
	behind atomic.Bool

	// Guarded by hub.mu.
	access    Access
	cursor    string
	progress  time.Time
	nextCheck time.Time
	suspect   bool
	gone      bool
	reason    string
}

// Out carries the messages in order.
func (s *Stream) Out() <-chan StreamMessage { return s.out }

// Closed is closed when the hub closes the stream. Reason then says why.
func (s *Stream) Closed() <-chan struct{} { return s.closed }

// Reason is why the hub closed the stream. EndAccess means the writer owes
// the client an end event; any other reason closes without one.
func (s *Stream) Reason() string {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.reason
}

// Taken tells the hub that the writer has taken a message. A stream that was
// left behind because its buffer was full gets the next events once there is
// room again.
func (s *Stream) Taken() {
	if s.behind.Load() && len(s.out) <= cap(s.out)/2 {
		s.hub.kick(s.task)
	}
}

// Release removes the stream. reason is logged when the hub has not already
// closed it.
func (s *Stream) Release(reason string) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s, reason)
}

func (h *StreamHub) signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func (h *StreamHub) kick(task string) {
	h.mu.Lock()
	if _, ok := h.byTask[task]; ok {
		h.dirty[task] = struct{}{}
	}
	h.mu.Unlock()
	h.signal(h.wake)
}

// notify marks a task whose streams must read new rows.
func (h *StreamHub) notify(task string) { h.kick(task) }

// markAll marks every task that has a stream.
func (h *StreamHub) markAll() {
	h.mu.Lock()
	for task := range h.byTask {
		h.dirty[task] = struct{}{}
	}
	h.mu.Unlock()
	h.signal(h.wake)
}

// removeLocked takes the stream out of the hub. Only the first call counts.
func (h *StreamHub) removeLocked(s *Stream, reason string) {
	if s.gone {
		return
	}
	s.gone = true
	s.reason = reason
	if set := h.byTask[s.task]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.byTask, s.task)
		}
	}
	if h.byPerson[s.person]--; h.byPerson[s.person] <= 0 {
		delete(h.byPerson, s.person)
	}
	h.total--
	h.log.Info("task stream closed", "task", s.task, "person", s.person, "reason", reason,
		"open_for", time.Since(s.opened).Round(time.Millisecond).String())
}

// dropLocked removes the stream and tells its writer to stop.
func (h *StreamHub) dropLocked(s *Stream, reason string) {
	if s.gone {
		return
	}
	h.removeLocked(s, reason)
	close(s.closed)
}

func (h *StreamHub) loop(ctx context.Context) {
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
		tasks := make([]string, 0, len(h.dirty))
		for task := range h.dirty {
			tasks = append(tasks, task)
		}
		clear(h.dirty)
		h.mu.Unlock()
		for _, task := range tasks {
			if ctx.Err() != nil {
				return
			}
			h.sync(ctx, task)
		}
	}
}

// sync reads what a task gained since its slowest stream, once, and hands it to
// every stream of the task. The read runs with the access of one stream. A
// stream the database no longer lets read the task is held back until its
// access is checked again, and another stream's access reads instead.
func (h *StreamHub) sync(ctx context.Context, task string) {
	for {
		h.mu.Lock()
		var reader *Stream
		var access Access
		from := ""
		for s := range h.byTask[task] {
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

		tail, err := h.rows.Tail(ctx, access, task, from, h.cfg.Batch)
		if errors.Is(err, ErrGone) || errors.Is(err, ErrAccess) {
			h.hold(reader)
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				h.log.Warn("task stream read failed", "task", task, "err", err)
			}
			return
		}
		h.deliver(task, from, tail)
		return
	}
}

// hold stops sending to a stream until its access is checked, and asks for
// that check now.
func (h *StreamHub) hold(s *Stream) {
	h.mu.Lock()
	if !s.gone {
		s.suspect = true
		s.nextCheck = time.Time{}
	}
	h.mu.Unlock()
	h.signal(h.probe)
}

func finished(state domain.Status) bool {
	return state == domain.Done || state == domain.Failed
}

// deliver puts what a read found into the buffer of each stream of the task,
// as far as the buffer has room, and ends the streams of a finished task.
func (h *StreamHub) deliver(task, from string, tail Tail) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	progressed := false
	skipped := false
	for s := range h.byTask[task] {
		if s.suspect {
			continue
		}
		if s.cursor < from {
			// The stream opened after the read began and needs rows from
			// before it. The next read has them.
			skipped = true
			continue
		}
		room := cap(s.out) - len(s.out) - 2 // keep room for a status and an end
		sent := 0
		behind := false
		for _, event := range tail.Events {
			if event.ID <= s.cursor {
				continue
			}
			if room <= 0 {
				behind = true
				break
			}
			s.out <- StreamMessage{Kind: StreamLine, Event: event}
			s.cursor = event.ID
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
		if sent > 0 {
			s.out <- StreamMessage{Kind: StreamStatus, Status: tail.Status, Phase: tail.Phase}
		}
		if finished(tail.State) {
			s.out <- StreamMessage{Kind: StreamEnd}
			h.removeLocked(s, EndFinished)
		}
	}
	if skipped || (tail.More && progressed) {
		h.dirty[task] = struct{}{}
		h.signal(h.wake)
	}
}

func (h *StreamHub) refreshLoop(ctx context.Context) {
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
		var due []*Stream
		for _, set := range h.byTask {
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

// check asks again whether the stream's person may still see the task: the
// session and the person's access, then the task under row security. A person
// who may not is sent an end event with reason access. A check that could not
// be made closes the stream without one, and the browser reconnects.
func (h *StreamHub) check(ctx context.Context, s *Stream) {
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
	if _, err := FindTask(ctx, h.rows, access, s.task); err != nil {
		if errors.Is(err, ErrGone) || errors.Is(err, ErrAccess) {
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
		h.kick(s.task)
	}
}

func (h *StreamHub) closeStream(ctx context.Context, s *Stream, reason string) {
	if ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	h.dropLocked(s, reason)
	h.mu.Unlock()
}
