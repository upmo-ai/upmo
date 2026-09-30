package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

const (
	// SeatImage is the image a task sandbox starts from when Seats names none.
	// It ships sh, coreutils timeout and a sleep that accepts infinity, which
	// the Docker driver and the provisioning phases need. It has no git, so
	// a task with a repository needs an image that does, such as the base
	// target of deploy/sandbox/Dockerfile (UPMO_SEAT_IMAGE).
	SeatImage = "debian:bookworm-slim"
	// SeatTTL is how long a task sandbox lives when Seats names no TTL.
	// sandboxd reclaims it after that even if no Release arrives.
	SeatTTL = 30 * time.Minute
	// settleTimeout bounds the done call that settles a task once its
	// provisioning stops in the background.
	settleTimeout = 30 * time.Second
)

var (
	// ErrSandboxdUnset reports a sandbox that is needed while no provider is
	// wired, because UPMO_SANDBOXD_URL is unset.
	ErrSandboxdUnset = errors.New("runtime: sandboxd is not configured")
	// ErrNoSeatBook reports Seats wired to sandboxd with nowhere to record
	// the seat, so a sandbox it created could not be released after a restart.
	ErrNoSeatBook = errors.New("runtime: seats have no seat book")
	// ErrSeated reports a task that already holds a sandbox, or has one
	// being created.
	ErrSeated = errors.New("runtime: task already holds a sandbox")
	// ErrReleased reports a task released while its sandbox was being
	// created. The new sandbox is destroyed.
	ErrReleased = errors.New("runtime: task released during create")
)

// SeatBook is the durable record of which sandbox each task holds, so a
// seat outlives the process that created it. Task ids are canonical uuids.
// A seat with no sandbox id is a Create still in flight.
type SeatBook interface {
	// Claim records a pending seat for taskID that lapses after ttl. It
	// answers ErrSeated when the task already has a seat.
	Claim(ctx context.Context, taskID string, ttl time.Duration) error
	// Fill records sandboxID on taskID's pending seat, and expires when it
	// is not zero. It reports false when no pending seat is left, because a
	// Release dropped it.
	Fill(ctx context.Context, taskID, sandboxID string, expires time.Time) (bool, error)
	// Seat returns the sandbox id taskID holds, empty while pending, and
	// whether the task has a seat at all.
	Seat(ctx context.Context, taskID string) (string, bool, error)
	// Drop forgets taskID's seat if it still holds sandboxID. An empty
	// sandboxID drops only a pending seat. A seat that is gone is not an error.
	Drop(ctx context.Context, taskID, sandboxID string) error
}

// Seats records which sandbox each task holds in its Book and creates and
// destroys it through the provider. Because the Book is durable, a brain
// that restarts can still Release a seat an earlier process created.
//
// A nil Provider means sandboxd is not configured: Start refuses with
// ErrSandboxdUnset and Release leaves any recorded seat for a brain that can
// reach sandboxd. sandboxd still reclaims every sandbox at its TTL.
type Seats struct {
	// Provider is the sandboxd client, or nil when UPMO_SANDBOXD_URL is unset.
	Provider SandboxProvider
	// Book records each seat. Production keeps it in the runtime schema.
	Book SeatBook
	// Image and TTL fill the spec Provision builds. Empty means SeatImage and SeatTTL.
	Image string
	TTL   time.Duration
	// Log records a Destroy or a provisioning that failed. Nil logs nothing.
	Log *slog.Logger

	// mu guards progress, the provisioning of each seat in this process.
	mu       sync.Mutex
	progress map[string]*preparing
	// running counts the provisionings still running in the background.
	running sync.WaitGroup
}

// preparing is one seat's provisioning: its record, and the stop that
// cancels its background phases once they have started.
type preparing struct {
	rec  domain.Record
	stop context.CancelFunc
}

// Done receives the outcome of a provisioning that ran in the background:
// nil at PhaseReady, or the PhaseError of the phase that failed. ctx is
// detached from the request that opened the task and bounded, so done can
// fail the task and Release the seat. The error done returns is logged.
type Done func(ctx context.Context, err error) error

// Ready reports whether Seats can create a sandbox.
func (s *Seats) Ready() bool {
	return s != nil && s.Provider != nil && s.Book != nil
}

// Provision creates a sandbox for taskID when repo or classes need one
// and reports whether one was needed. When none is needed it
// does not call the provider. A repo CloneURL refuses is refused before
// Create.
//
// Provision returns as soon as the seat holds its sandbox. The
// provisioning phases then run in the background, on a context
// detached from ctx and bounded by the seat's TTL, because the sandbox is
// gone after that anyway. When they stop, done is called once with the
// outcome; a phase that fails leaves the seat held, so done fails the task
// and calls Release. done is not called once a Release has stopped the
// phases, because whoever released the seat has already moved the task on.
// Progress shows the record from the start until Release.
func (s *Seats) Provision(ctx context.Context, taskID, repo string, classes []domain.Class, done Done) (bool, error) {
	needs, err := domain.NeedsSandbox(repo, classes)
	if err != nil || !needs {
		return false, err
	}
	if _, err := domain.CloneURL(repo); err != nil {
		return true, err
	}
	key, ok := taskKey(taskID)
	if !ok {
		return true, ErrSpec
	}
	// Tracked before Start, so a Release from here on also forgets it.
	mine := s.begin(key)
	spec := s.spec(taskID, repo)
	box, err := s.Start(ctx, taskID, spec)
	if err != nil {
		if mine != nil {
			s.forget(key, mine)
		}
		return true, err
	}

	// The TTL is brain's own clock, so sandboxd's clock cannot cut it short.
	run, stop := context.WithTimeout(context.WithoutCancel(ctx), spec.TTL)
	if mine == nil || !s.arm(key, mine, stop) {
		// A Release came between Start and here, or this call never owned
		// the record; either way the phases are not this call's to run.
		stop()
		return true, ErrReleased
	}
	s.running.Go(func() { s.prepare(run, stop, key, mine, box.ID, repo, done) })
	return true, nil
}

// prepare runs the provisioning phases for key in sandbox id and hands the
// outcome to done, unless a Release stopped them first. It never lets a
// panic out of its goroutine: one in the phases fails the provisioning,
// and one in done is logged.
func (s *Seats) prepare(ctx context.Context, stop context.CancelFunc, key string, mine *preparing, id, repo string, done Done) {
	defer stop()
	defer func() {
		if p := recover(); p != nil {
			s.warn("sandbox provisioning settle panicked", "task", key, "sandbox", id, "panic", p)
		}
	}()
	rec, err := s.phases(ctx, key, mine, id, repo)
	if !s.holds(key, mine) {
		return
	}
	if err != nil {
		s.warn("sandbox provisioning failed", "task", key, "sandbox", id, "phase", rec.Phase, "err", err)
	}
	if done == nil {
		return
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	if derr := done(settle, err); derr != nil {
		s.warn("sandbox provisioning settle failed", "task", key, "sandbox", id, "err", derr)
	}
}

// phases runs the Provisioner, turning a panic into a failed provisioning.
func (s *Seats) phases(ctx context.Context, key string, mine *preparing, id, repo string) (rec domain.Record, err error) {
	defer func() {
		if p := recover(); p != nil {
			rec, err = domain.Record{Phase: domain.PhaseFailed}, fmt.Errorf("%w: panic: %v", ErrProvisionFailed, p)
			s.update(key, mine, rec)
		}
	}()
	prep := Provisioner{Provider: s.Provider, Track: func(rec domain.Record) { s.update(key, mine, rec) }}
	return prep.Run(ctx, id, repo)
}

// Wait blocks until every provisioning running in the background has
// stopped, or ctx ends. brain calls it on shutdown, after the server has
// stopped taking requests, so no new provisioning starts while it waits.
func (s *Seats) Wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	idle := make(chan struct{})
	go func() {
		s.running.Wait()
		close(idle)
	}()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Progress returns the provisioning record of taskID's seat, from the
// moment Provision starts until Release, including while its phases run.
// It lives in this process only; the seat itself is in the Book.
func (s *Seats) Progress(taskID string) (domain.Record, bool) {
	key, ok := taskKey(taskID)
	if s == nil || !ok {
		return domain.Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.progress[key]
	if !ok {
		return domain.Record{}, false
	}
	return p.rec, true
}

// begin adds a fresh provisioning for key and returns it, or nil when key
// already has one or s is nil.
func (s *Seats) begin(key string) *preparing {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.progress[key]; ok {
		return nil
	}
	if s.progress == nil {
		s.progress = map[string]*preparing{}
	}
	p := &preparing{rec: domain.Begin()}
	s.progress[key] = p
	return p
}

// arm gives mine the stop for its background phases, and reports false
// when a Release has already forgotten it.
func (s *Seats) arm(key string, mine *preparing, stop context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress[key] != mine {
		return false
	}
	mine.stop = stop
	return true
}

// update records rec on mine while key still holds it, so phases that
// run on after a Release leave nothing behind.
func (s *Seats) update(key string, mine *preparing, rec domain.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.progress[key] == mine {
		mine.rec = rec
	}
}

// holds reports whether key still holds mine, that is, no Release came.
func (s *Seats) holds(key string, mine *preparing) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress[key] == mine
}

// forget drops key's provisioning, or only mine when mine is not nil, and
// stops its background phases.
func (s *Seats) forget(key string, mine *preparing) {
	if s == nil {
		return
	}
	s.mu.Lock()
	var stop context.CancelFunc
	if p, ok := s.progress[key]; ok && (mine == nil || p == mine) {
		delete(s.progress, key)
		stop = p.stop
	}
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *Seats) warn(msg string, args ...any) {
	if s.Log != nil {
		s.Log.Warn(msg, args...)
	}
}

// spec is the sandbox Provision asks for: the container tier, the seat
// image and TTL, and the clone host on the allow-list when repo has one.
func (s *Seats) spec(taskID, repo string) Spec {
	spec := Spec{Task: taskID, Image: SeatImage, Tier: domain.TierContainer, TTL: SeatTTL}
	if s != nil && strings.TrimSpace(s.Image) != "" {
		spec.Image = s.Image
	}
	if s != nil && s.TTL > 0 {
		spec.TTL = s.TTL
	}
	if clone, err := domain.CloneURL(repo); err == nil && clone != "" {
		if u, err := url.Parse(clone); err == nil {
			if host := u.Hostname(); host != "" {
				spec.Allow = []string{host}
			}
		}
	}
	return spec
}

// Start admits spec, claims taskID's seat, creates its sandbox and records
// it. A blank spec task takes taskID; a different one is refused with
// ErrSpec. A sandbox that cannot be recorded is destroyed, so no sandbox is
// left that a restarted brain could not release.
func (s *Seats) Start(ctx context.Context, taskID string, spec Spec) (Sandbox, error) {
	if s == nil || s.Provider == nil {
		return Sandbox{}, ErrSandboxdUnset
	}
	if s.Book == nil {
		return Sandbox{}, ErrNoSeatBook
	}
	key, ok := taskKey(taskID)
	if !ok {
		return Sandbox{}, ErrSpec
	}
	if strings.TrimSpace(spec.Task) == "" {
		spec.Task = key
	}
	spec, err := CheckSpec(spec)
	if err != nil {
		return Sandbox{}, err
	}
	if spec.Task != key {
		return Sandbox{}, ErrSpec
	}

	if err := s.Book.Claim(ctx, key, spec.TTL); err != nil {
		return Sandbox{}, err
	}
	box, err := s.Provider.Create(ctx, spec)
	// The caller may have gone; the seat is still settled.
	settle := context.WithoutCancel(ctx)
	if err != nil {
		return Sandbox{}, errors.Join(err, s.Book.Drop(settle, key, ""))
	}
	held, err := domain.Hold(box.ID)
	if err != nil {
		return Sandbox{}, errors.Join(err, s.Book.Drop(settle, key, ""))
	}
	filled, err := s.Book.Fill(settle, key, held.Container, box.Expires)
	if err != nil || !filled {
		// Best effort: a failure is logged and sandboxd reclaims it at its TTL.
		_ = s.destroy(settle, key, held.Container)
		if err != nil {
			return Sandbox{}, errors.Join(err, s.Book.Drop(settle, key, ""))
		}
		return Sandbox{}, ErrReleased
	}
	return box, nil
}

// Release destroys the sandbox taskID holds and forgets it and its
// provisioning record, stopping any phases still running in the
// background. A task that holds none, or a nil Seats, is a no-op. A pending seat is dropped, and
// the Start still creating it destroys the sandbox once Create returns; a
// pending seat whose process is gone has no sandbox id to destroy, and
// sandboxd reclaims that sandbox at its TTL. A sandbox sandboxd no longer
// has is forgotten. Any other Destroy failure keeps the seat, so a later
// Release can retry, and sandboxd still reclaims the sandbox at its TTL.
func (s *Seats) Release(ctx context.Context, taskID string) error {
	if s == nil {
		return nil
	}
	key, ok := taskKey(taskID)
	if !ok {
		return ErrSpec
	}
	s.forget(key, nil)
	if s.Provider == nil || s.Book == nil {
		return nil
	}
	id, held, err := s.Book.Seat(ctx, key)
	if err != nil || !held {
		return err
	}
	if id != "" {
		if err := s.destroy(ctx, key, id); err != nil {
			return err
		}
	}
	return s.Book.Drop(ctx, key, id)
}

// Held returns the sandbox id taskID holds, if any. A pending seat holds none.
func (s *Seats) Held(ctx context.Context, taskID string) (string, bool, error) {
	key, ok := taskKey(taskID)
	if s == nil || s.Book == nil || !ok {
		return "", false, nil
	}
	id, held, err := s.Book.Seat(ctx, key)
	if err != nil || !held || id == "" {
		return "", false, err
	}
	return id, true, nil
}

// destroy calls the provider and treats a sandbox it no longer has as gone.
func (s *Seats) destroy(ctx context.Context, key, id string) error {
	err := s.Provider.Destroy(ctx, id)
	if err == nil || errors.Is(err, ErrNotFound) {
		return nil
	}
	if s.Log != nil {
		s.Log.Warn("sandbox destroy failed", "task", key, "sandbox", id, "err", err)
	}
	return err
}

// taskKey is the canonical task id, so one task has one seat however its
// id is written.
func taskKey(taskID string) (string, bool) {
	id, err := uuid.Parse(strings.TrimSpace(taskID))
	if err != nil || id == uuid.Nil {
		return "", false
	}
	return id.String(), true
}
