package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

const (
	// SeatImage is the image a task sandbox starts from when Seats names none.
	// It ships coreutils timeout and a sleep that accepts infinity, which the
	// Docker driver needs.
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
	// Log records a Destroy that failed. Nil logs nothing.
	Log *slog.Logger
}

// Ready reports whether Seats can create a sandbox.
func (s *Seats) Ready() bool {
	return s != nil && s.Provider != nil && s.Book != nil
}

// Provision creates a sandbox for taskID when repo or classes need one.
// It reports whether one was needed. When none is needed it
// does not call the provider.
func (s *Seats) Provision(ctx context.Context, taskID, repo string, classes []domain.Class) (bool, error) {
	needs, err := domain.NeedsSandbox(repo, classes)
	if err != nil || !needs {
		return false, err
	}
	_, err = s.Start(ctx, taskID, s.spec(taskID))
	return true, err
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

// Release destroys the sandbox taskID holds and forgets it. A task that
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
