package app

import (
	"context"
	"errors"
	"log/slog"
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

	// mu guards progress, the provisioning record of each seat in this
	// process.
	mu       sync.Mutex
	progress map[string]domain.Record
}

// Ready reports whether Seats can create a sandbox.
func (s *Seats) Ready() bool {
	return s != nil && s.Provider != nil && s.Book != nil
}

// Provision creates a sandbox for taskID when repo or classes need one,
// then runs the provisioning phases in it and
// reports whether one was needed. When none is needed it does not call the
// provider. A repo CloneURL refuses is refused before Create.
//
// A phase that fails returns a PhaseError and leaves the seat held, so the
// caller fails the task and calls Release. Progress shows the record until
// that Release.
func (s *Seats) Provision(ctx context.Context, taskID, repo string, classes []domain.Class) (bool, error) {
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
	mine := s.track(key, domain.Begin(), true)
	box, err := s.Start(ctx, taskID, s.spec(taskID))
	if err != nil {
		if mine {
			s.forget(key)
		}
		return true, err
	}
	prep := Provisioner{Provider: s.Provider, Track: func(rec domain.Record) { _ = s.track(key, rec, false) }}
	rec, err := prep.Run(ctx, box.ID, repo)
	if err != nil && s.Log != nil {
		s.Log.Warn("sandbox provisioning failed", "task", key, "sandbox", box.ID, "phase", rec.Phase, "err", err)
	}
	return true, err
}

// Progress returns the provisioning record of taskID's seat, from the
// moment Provision starts until Release. It lives in this process only;
// the seat itself is in the Book.
func (s *Seats) Progress(taskID string) (domain.Record, bool) {
	key, ok := taskKey(taskID)
	if s == nil || !ok {
		return domain.Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.progress[key]
	return rec, ok
}

// track records rec for key. With start it only adds a record that is not
// there yet, and reports whether it did; without it, it only updates one
// that is still there, so a Run after a Release leaves nothing behind.
func (s *Seats) track(key string, rec domain.Record, start bool) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.progress[key]; ok == start {
		return false
	}
	if s.progress == nil {
		s.progress = map[string]domain.Record{}
	}
	s.progress[key] = rec
	return true
}

func (s *Seats) forget(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.progress, key)
}

// spec is the sandbox Provision asks for: the container tier, the seat
// image and TTL, and an empty allow-list, so egress stays denied.
func (s *Seats) spec(taskID string) Spec {
	spec := Spec{Task: taskID, Image: SeatImage, Tier: domain.TierContainer, TTL: SeatTTL}
	if s != nil && strings.TrimSpace(s.Image) != "" {
		spec.Image = s.Image
	}
	if s != nil && s.TTL > 0 {
		spec.TTL = s.TTL
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
// provisioning record. A task that
// holds none, or a nil Seats, is a no-op. A pending seat is dropped, and
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
	s.forget(key)
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
