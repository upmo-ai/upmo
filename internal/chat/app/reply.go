package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// ReplyQueue is the River queue of reply jobs. It has workers of its own, so
// a stream never holds up the scout or the extract.
const ReplyQueue = "chat_reply"

// Defaults of a reply.
const (
	// DefaultHistoryChars is how much of a chat a reply sends to the model.
	DefaultHistoryChars = 48000
	// DefaultMaxOutputTokens caps one reply.
	DefaultMaxOutputTokens = 4096
	// DefaultReplyTimeout is how long one reply job may run.
	DefaultReplyTimeout = 5 * time.Minute
	// DefaultFlushEvery and DefaultFlushBytes are how often text becomes a
	// delta row: after the time, or at the size, whichever comes first.
	DefaultFlushEvery = 75 * time.Millisecond
	DefaultFlushBytes = 2048
	// DefaultWait is how long a reply waits before it looks again when the
	// chat is busy with another.
	DefaultWait = 2 * time.Second
	// DefaultAccessEvery is how often a streaming reply looks its author's
	// access up again.
	DefaultAccessEvery = 30 * time.Second
)

// LiveFor is how long a reply that wrote nothing new still counts as running.
// It is longer than the job timeout, so a reply whose process died stops
// holding up the chat.
const LiveFor = DefaultReplyTimeout + time.Minute

// finishTimeout bounds the write of a line after the reply's own context has
// ended.
const finishTimeout = 15 * time.Second

// ReplyJobArgs is the River payload for the reply to one user line. LineID is
// that user line. ReplyID is the id the assistant line will get; it is chosen
// when the job is enqueued, so a rerun finds the deltas of the run before it.
// A job is unique by LineID while one is waiting or running, so a line has one
// reply on its way at a time. PinAccount and PinModel, both or neither, are
// the model pinned for this reply; AgentID is the agent picked. The agent is
// loaded when the reply is written: the agent adds its instructions and, where
// the composer picked none, its route and its model. Route is the composer's
// pick when RoutePicked, and the install's default route otherwise.
type ReplyJobArgs struct {
	ChatID      string `json:"chat_id"`
	LineID      string `json:"line_id" river:"unique"`
	ReplyID     string `json:"reply_id"`
	UserID      string `json:"user_id"`
	Route       string `json:"route"`
	RoutePicked bool   `json:"route_picked,omitempty"`
	PinAccount  string `json:"pin_account,omitempty"`
	PinModel    string `json:"pin_model,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
}

// Kind is the River job kind.
func (ReplyJobArgs) Kind() string { return "chat_reply" }

// InsertOpts puts the job on its own queue and gives it a second attempt, so
// a crash of the process is rescued.
func (ReplyJobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       ReplyQueue,
		MaxAttempts: 2,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateRetryable,
				rivertype.JobStateScheduled,
			},
		},
	}
}

// ReplyState is what Begin found.
type ReplyState int

const (
	// ReplyGo means the reply may start; the turn carries the lines to send.
	ReplyGo ReplyState = iota + 1
	// ReplyWait means another reply is running in the chat, or an earlier line
	// has none yet.
	ReplyWait
	// ReplyDone means the reply's line is already stored.
	ReplyDone
	// ReplyRescue means an earlier run began this reply and did not finish it.
	ReplyRescue
)

// ReplyTurn is what a reply reads before it starts.
type ReplyTurn struct {
	State ReplyState
	// Lines are the lines to send, oldest first, up to and including the user
	// line: no failed reply and no reply that a later one replaced.
	Lines []Line
	// Partial, Attempts and Model are what an earlier run left in deltas, for
	// ReplyRescue.
	Partial  string
	Attempts []domain.Attempt
	Model    string
}

// Reply is the assistant line to store.
type Reply struct {
	ChatID       string
	LineID       string
	ReplyTo      string
	Status       string
	Body         string
	Model        string
	Tokens       int
	CostMicroUSD *int64
	Attempts     []domain.Attempt
}

// Notice is the body of a status delta: a model failed before any text and
// the reply goes on with the next.
type Notice struct {
	FailedModel string `json:"failed_model"`
	Class       string `json:"class"`
	NextModel   string `json:"next_model"`
}

// ReplyRows is what the reply job reads and writes, as the person who wrote
// the line.
type ReplyRows interface {
	// Begin checks the chat and the line, finds what an earlier run left, and
	// otherwise marks the reply as started. ErrGone and ErrSay mean the person
	// can no longer write to the chat; ErrNoLine that the line is not there.
	Begin(ctx context.Context, access Access, job ReplyJobArgs) (ReplyTurn, error)
	// PutDelta stores one row of the reply as it streams.
	PutDelta(ctx context.Context, access Access, chatID, replyID string, seq int, kind, body string) error
	// Finish stores the assistant line and deletes the reply's deltas in one
	// transaction.
	Finish(ctx context.Context, access Access, reply Reply) error
	// Sweep deletes deltas older than age whose line never arrived.
	Sweep(ctx context.Context) (int64, error)
	// FailReply stores the failed reply to a user line for an author who can
	// no longer write to the chat, or for a job that could not start, and
	// deletes the reply's deltas. why is access or unwritten. It runs without
	// the author's access.
	FailReply(ctx context.Context, userLine, replyID, why string) (bool, error)
	// CanWrite reports ErrGone or ErrSay when the person can no longer write
	// to the chat.
	CanWrite(ctx context.Context, access Access, chatID string) error
	// Agent returns the agent a reply is for, as the person who wrote the
	// line: one they can use that is switched on. Anything else is
	// ErrAgentNotFound.
	Agent(ctx context.Context, access Access, id string) (Agent, error)
}

// ModelMessage is one turn sent to the model.
type ModelMessage struct {
	Role string
	Text string
}

// ModelRequest is one reply's call to the gateway. The pin is a provider
// account and a model, or a provider name and a model that the gateway
// resolves for the person (PinProvider), never both.
type ModelRequest struct {
	Route           string
	CredentialRef   string
	UserID          string
	System          string
	Messages        []ModelMessage
	MaxOutputTokens int
	PinAccount      string
	PinProvider     string
	PinModel        string
	AgentID         string
}

// ModelAttempt is a model that failed before any text, and the next one.
type ModelAttempt struct {
	FailedModel string
	Class       string
	NextModel   string
}

// ModelDone is the successful end of a reply.
type ModelDone struct {
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      *float64
}

// ModelError is a reply that broke off: the failure class and the model that
// was writing.
type ModelError struct {
	Class string
	Model string
}

func (e *ModelError) Error() string {
	return fmt.Sprintf("chat: model %s on %s", e.Class, e.Model)
}

// Model streams a reply. onDelta gets each piece of text in order and
// onAttempt each failover; both may be nil. A failure comes back as a
// *ModelError.
type Model interface {
	Stream(ctx context.Context, req ModelRequest, onDelta func(string), onAttempt func(ModelAttempt)) (ModelDone, error)
}

// ReplyConfig holds the caps of a reply. A zero field takes its default.
type ReplyConfig struct {
	HistoryChars    int
	MaxOutputTokens int
	Timeout         time.Duration
	FlushEvery      time.Duration
	FlushBytes      int
	Wait            time.Duration
	AccessEvery     time.Duration
}

func (c ReplyConfig) withDefaults() ReplyConfig {
	if c.HistoryChars <= 0 {
		c.HistoryChars = DefaultHistoryChars
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultReplyTimeout
	}
	if c.FlushEvery <= 0 {
		c.FlushEvery = DefaultFlushEvery
	}
	if c.FlushBytes <= 0 {
		c.FlushBytes = DefaultFlushBytes
	}
	if c.AccessEvery <= 0 {
		c.AccessEvery = DefaultAccessEvery
	}
	if c.Wait <= 0 {
		c.Wait = DefaultWait
	}
	return c
}

// AgentLookup resolves an agent a reply is for, as the person who wrote the
// line.
type AgentLookup interface {
	Agent(ctx context.Context, access Access, id string) (Agent, error)
}

// ReplyDeps is what the reply worker needs. Access resolves the person who
// wrote the line, as a request would; false means the person may no longer
// use chats. Agents, when set, overrides Rows.Agent for looking up agents.
// Log may be nil.
type ReplyDeps struct {
	Rows   ReplyRows
	Agents AgentLookup
	Model  Model
	Access func(ctx context.Context, userID string) (Access, bool, error)
	Config ReplyConfig
	Log    *slog.Logger
}

type replyWorker struct {
	river.WorkerDefaults[ReplyJobArgs]
	deps ReplyDeps
	cfg  ReplyConfig
	log  *slog.Logger
}

// AddReplyWorker registers the chat reply worker on workers.
func AddReplyWorker(workers *river.Workers, deps ReplyDeps) {
	river.AddWorker(workers, NewReplyWorker(deps))
}

// NewReplyWorker returns the chat reply worker.
func NewReplyWorker(deps ReplyDeps) river.Worker[ReplyJobArgs] {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &replyWorker{deps: deps, cfg: deps.Config.withDefaults(), log: log}
}

func (w *replyWorker) Timeout(*river.Job[ReplyJobArgs]) time.Duration { return w.cfg.Timeout }

func (w *replyWorker) Work(ctx context.Context, job *river.Job[ReplyJobArgs]) error {
	args := job.Args
	last := job.Attempt >= job.MaxAttempts
	if _, err := uuid.Parse(args.UserID); err != nil {
		return river.JobCancel(fmt.Errorf("chat: reply job: user: %w", err))
	}
	access, ok, err := w.deps.Access(ctx, args.UserID)
	if err != nil {
		return w.giveUp(ctx, args, last, err)
	}
	if !ok {
		return w.refuse(ctx, args)
	}
	turn, err := w.deps.Rows.Begin(ctx, access, args)
	switch {
	case errors.Is(err, ErrGone), errors.Is(err, ErrSay):
		return w.refuse(ctx, args)
	case errors.Is(err, ErrNoLine):
		return river.JobCancel(err)
	case err != nil:
		return w.giveUp(ctx, args, last, err)
	}
	switch turn.State {
	case ReplyDone:
		return nil
	case ReplyWait:
		return river.JobSnooze(w.cfg.Wait)
	case ReplyRescue:
		return w.store(ctx, access, args, Reply{
			Status:   domain.StatusFailed,
			Body:     turn.Partial,
			Model:    turn.Model,
			Attempts: closeAttempts(turn.Attempts, &ModelError{Class: "cancelled", Model: turn.Model}),
		})
	}
	return w.stream(ctx, access, args, turn.Lines)
}

// refuse ends a reply whose author may no longer write to the chat, so the
// line is not left without one: a failed reply that keeps what the deltas
// hold. Row security would refuse their own write, so it goes through the
// function made for it.
func (w *replyWorker) refuse(ctx context.Context, args ReplyJobArgs) error {
	return w.failed(ctx, args, "access", nil)
}

// giveUp is for an error before the reply began. It is returned for River to
// try again, until the last attempt, which stores the failed reply instead.
func (w *replyWorker) giveUp(ctx context.Context, args ReplyJobArgs, last bool, cause error) error {
	if !last {
		return cause
	}
	w.log.Warn("chat reply could not begin", "chat", args.ChatID, "line", args.ReplyID, "err", cause)
	return w.failed(ctx, args, "unwritten", cause)
}

func (w *replyWorker) failed(ctx context.Context, args ReplyJobArgs, why string, cause error) error {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if _, err := w.deps.Rows.FailReply(wctx, args.LineID, args.ReplyID, why); err != nil {
		if cause != nil {
			return cause
		}
		return err
	}
	return nil
}

// liveAccess is the author's access as it stands, looked up again while the
// reply streams. Once it is found revoked, nothing more is written under it.
type liveAccess struct {
	mu      sync.Mutex
	access  Access
	revoked bool
}

func (l *liveAccess) get() (Access, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.access, !l.revoked
}

func (l *liveAccess) set(access Access) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.revoked {
		l.access = access
	}
}

func (l *liveAccess) revoke() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.revoked = true
}

// errRevoked is a write that was not made because the author's edit access is
// gone.
var errRevoked = errors.New("chat: access revoked")

// refresh looks the author's access up again and fails closed: a person who
// may no longer use chats, may no longer write to this one, or whose access
// could not be looked up counts as having lost it. A refresh cut short by the
// end of its own context decides nothing.
func (w *replyWorker) refresh(ctx context.Context, live *liveAccess, args ReplyJobArgs) {
	access, ok, err := w.deps.Access(ctx, args.UserID)
	if err == nil && ok {
		err = w.deps.Rows.CanWrite(ctx, access, args.ChatID)
	}
	switch {
	case ctx.Err() != nil:
	case err != nil || !ok:
		live.revoke()
	default:
		live.set(access)
	}
}

// stream asks the model and stores the outcome: a done line, or a failed one
// that keeps the text written so far. A failed line is stored whatever went
// wrong, so no line goes without a reply; only a write that fails as well
// returns an error, and the second attempt then finds the deltas. The author's
// access is looked up again every AccessEvery and before the line is stored;
// when edit access is gone the call is stopped and the reply fails.
func (w *replyWorker) stream(ctx context.Context, access Access, args ReplyJobArgs, lines []Line) error {
	var agent *Agent
	if args.AgentID != "" {
		lookup := w.deps.Agents
		if lookup == nil {
			lookup = w.deps.Rows
		}
		got, err := lookup.Agent(ctx, access, args.AgentID)
		switch {
		case errors.Is(err, ErrAgentNotFound):
			// An agent that is gone, switched off or not theirs is never
			// skipped: the reply fails and says so.
			return w.store(ctx, access, args, Reply{
				Status:   domain.StatusFailed,
				Attempts: []domain.Attempt{{Class: domain.ClassAgent}},
			})
		case err != nil:
			return err
		}
		agent = &got
	}
	live := &liveAccess{access: access}
	sink := &deltaSink{
		put: func(seq int, kind, body string) error {
			current, ok := live.get()
			if !ok {
				return errRevoked
			}
			return w.deps.Rows.PutDelta(ctx, current, args.ChatID, args.ReplyID, seq, kind, body)
		},
		every: w.cfg.FlushEvery,
		bytes: w.cfg.FlushBytes,
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sink.stop = cancel

	watching := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		tick := time.NewTicker(w.cfg.AccessEvery)
		defer tick.Stop()
		for {
			select {
			case <-watching:
				return
			case <-tick.C:
				w.refresh(callCtx, live, args)
				if _, ok := live.get(); !ok {
					cancel()
					return
				}
			}
		}
	}()

	var attempts []domain.Attempt
	req := w.request(args, agent)
	req.Messages = history(lines, w.cfg.HistoryChars)
	done, err := w.deps.Model.Stream(callCtx, req, sink.add, func(a ModelAttempt) {
		attempts = append(attempts, domain.Attempt{Model: a.FailedModel, Class: a.Class})
		sink.notice(Notice(a))
	})
	text, werr := sink.finish()
	close(watching)
	<-watched

	// Whatever ended the reply, its line is stored under the access as it is
	// now. A timeout or a shutdown does not cut this look-up short.
	if _, ok := live.get(); ok {
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
		w.refresh(rctx, live, args)
		rcancel()
	}
	if _, ok := live.get(); !ok {
		return w.refuse(ctx, args)
	}
	current, _ := live.get()

	reply := Reply{Body: truncate(text, domain.ReplyRunes)}
	switch {
	case werr != nil:
		w.log.Warn("chat reply could not write a delta", "chat", args.ChatID, "line", args.ReplyID, "err", werr)
		reply.Status = domain.StatusFailed
		failure := failureOf(ctx, werr)
		if ctx.Err() == nil {
			failure = &ModelError{Class: "unwritten"}
		}
		reply.Model = failure.Model
		reply.Attempts = closeAttempts(attempts, failure)
		if reply.Model == "" && len(attempts) > 0 {
			reply.Model = attempts[len(attempts)-1].Model
		}
	case err == nil:
		reply.Status = domain.StatusDone
		reply.Model = done.Model
		reply.Tokens = done.InputTokens + done.OutputTokens
		if done.CostUSD != nil {
			micro := int64(math.Round(*done.CostUSD * 1e6))
			reply.CostMicroUSD = &micro
		}
	default:
		reply.Status = domain.StatusFailed
		failure := failureOf(ctx, err)
		reply.Model = failure.Model
		reply.Attempts = closeAttempts(attempts, failure)
		if reply.Model == "" && len(attempts) > 0 {
			reply.Model = attempts[len(attempts)-1].Model
		}
		w.log.Info("chat reply failed", "chat", args.ChatID, "line", args.ReplyID, "class", failure.Class, "model", failure.Model)
	}
	return w.store(ctx, current, args, reply)
}

// request is the call a reply makes, without its messages: the route and the
// pin settled between the composer's picks and the agent's defaults, and the
// agent's instructions as the system text.
func (w *replyWorker) request(args ReplyJobArgs, agent *Agent) ModelRequest {
	picked := domain.Picked{PinAccount: args.PinAccount, PinModel: args.PinModel}
	install := ""
	if args.RoutePicked {
		picked.Route = args.Route
	} else {
		install = args.Route
	}
	var defaults *domain.Defaults
	system := ""
	if agent != nil {
		d := agent.Defaults()
		defaults = &d
		system = domain.AgentSystem(agent.Name, agent.PromptOverlay)
	}
	resolved := domain.Resolve(picked, defaults, install)
	return ModelRequest{
		Route:           resolved.Route,
		UserID:          args.UserID,
		System:          system,
		MaxOutputTokens: w.cfg.MaxOutputTokens,
		PinAccount:      resolved.PinAccount,
		PinProvider:     resolved.PinProvider,
		PinModel:        resolved.PinModel,
		AgentID:         args.AgentID,
	}
}

// store writes the assistant line, on a context that outlives a timeout or a
// shutdown of the job so the person is not left without a reply. If their
// access is gone by then, the reply is failed through refuse instead.
func (w *replyWorker) store(ctx context.Context, access Access, args ReplyJobArgs, reply Reply) error {
	reply.ChatID, reply.LineID, reply.ReplyTo = args.ChatID, args.ReplyID, args.LineID
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	err := w.deps.Rows.Finish(wctx, access, reply)
	if errors.Is(err, ErrGone) || errors.Is(err, ErrSay) {
		return w.refuse(ctx, args)
	}
	return err
}

// failureOf names why a reply broke off. A job context that ended is a
// timeout or a stop; any error that is not the gateway's own words is a
// connection that failed.
func failureOf(ctx context.Context, err error) *ModelError {
	var failure *ModelError
	switch {
	case errors.As(err, &failure):
		return failure
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &ModelError{Class: "timeout"}
	case ctx.Err() != nil:
		return &ModelError{Class: "cancelled"}
	default:
		return &ModelError{Class: "transient"}
	}
}

// closeAttempts adds the failure that ended the reply to the notices that led
// up to it. The gateway announces the failure of the last model too, so the
// same model and class is not listed twice.
func closeAttempts(attempts []domain.Attempt, last *ModelError) []domain.Attempt {
	out := append([]domain.Attempt{}, attempts...)
	if n := len(out); n > 0 && out[n-1].Model == last.Model && out[n-1].Class == last.Class {
		return out
	}
	return append(out, domain.Attempt{Model: last.Model, Class: last.Class})
}

func truncate(text string, runes int) string {
	if utf8.RuneCountInString(text) <= runes {
		return text
	}
	return string([]rune(text)[:runes])
}

// history turns the lines of a chat into the messages of a call. It keeps the
// newest lines that fit the budget of characters, and always the last one, so
// the trim comes off the oldest end. Neighbouring lines of one role, which a
// failed reply leaves behind, are joined.
func history(lines []Line, budget int) []ModelMessage {
	kept := 0
	used := 0
	for i := len(lines) - 1; i >= 0; i-- {
		n := utf8.RuneCountInString(lines[i].Body)
		if kept > 0 && used+n > budget {
			break
		}
		used += n
		kept++
	}
	out := make([]ModelMessage, 0, kept)
	for _, line := range lines[len(lines)-kept:] {
		if n := len(out); n > 0 && out[n-1].Role == line.Role {
			out[n-1].Text += "\n\n" + line.Body
			continue
		}
		out = append(out, ModelMessage{Role: line.Role, Text: line.Body})
	}
	return out
}

// deltaSink coalesces the text of a reply into delta rows: one row per batch,
// written when the batch is old enough or large enough. A notice is written
// at once, after the text before it. The first write to fail stops the
// reply's model call and is kept.
type deltaSink struct {
	put   func(seq int, kind, body string) error
	every time.Duration
	bytes int
	stop  context.CancelFunc

	mu      sync.Mutex
	all     strings.Builder
	pending strings.Builder
	timer   *time.Timer
	seq     int
	err     error
	closed  bool
}

func (s *deltaSink) add(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil || s.closed {
		return
	}
	s.all.WriteString(text)
	s.pending.WriteString(text)
	if s.pending.Len() >= s.bytes {
		s.flushLocked()
		return
	}
	if s.timer == nil {
		s.timer = time.AfterFunc(s.every, s.tick)
	}
}

func (s *deltaSink) tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	if !s.closed {
		s.flushLocked()
	}
}

func (s *deltaSink) notice(n Notice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil || s.closed {
		return
	}
	s.flushLocked()
	if s.err != nil {
		return
	}
	body, err := json.Marshal(n)
	if err == nil {
		s.writeLocked("status", string(body))
	}
}

func (s *deltaSink) flushLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.pending.Len() == 0 || s.err != nil {
		return
	}
	s.writeLocked("text", s.pending.String())
	s.pending.Reset()
}

func (s *deltaSink) writeLocked(kind, body string) {
	s.seq++
	if err := s.put(s.seq, kind, body); err != nil {
		s.err = err
		s.stop()
	}
}

// finish ends the sink and returns all the text that came in, and the first
// write error. Text that was never written as a delta is part of the text.
func (s *deltaSink) finish() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return s.all.String(), s.err
}

// SweepJobArgs is the River payload of the periodic delete of deltas whose
// line never arrived.
type SweepJobArgs struct{}

// Kind is the River job kind.
func (SweepJobArgs) Kind() string { return "chat_sweep_deltas" }

type sweepWorker struct {
	river.WorkerDefaults[SweepJobArgs]
	rows ReplyRows
}

// AddSweepWorker registers the worker that deletes stale deltas on workers.
func AddSweepWorker(workers *river.Workers, rows ReplyRows) {
	river.AddWorker(workers, &sweepWorker{rows: rows})
}

func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepJobArgs]) error {
	swept, err := w.rows.Sweep(ctx)
	if err != nil {
		return err
	}
	if swept > 0 {
		slog.InfoContext(ctx, "chat deltas swept", "rows", swept)
	}
	return nil
}

// SweepPeriodic runs the sweep every ten minutes.
func SweepPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(10*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return SweepJobArgs{}, nil },
		nil,
	)
}
