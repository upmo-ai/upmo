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
	// ErrSeated reports a task that already holds a sandbox, or has one
	// being created.
	ErrSeated = errors.New("runtime: task already holds a sandbox")
	// ErrReleased reports a task released while its sandbox was being
	// created. The new sandbox is destroyed.
	ErrReleased = errors.New("runtime: task released during create")
)

// Seats remembers which sandbox each task holds, in process memory, and
// creates and destroys it through the provider. It holds no durable state:
// a restart forgets every seat and sandboxd reclaims the sandbox at its TTL.
// The zero value is ready. A nil Provider means sandboxd is not configured:
// Start refuses with ErrSandboxdUnset and Release has nothing to release.
type Seats struct {
	// Provider is the sandboxd client, or nil when UPMO_SANDBOXD_URL is unset.
	Provider SandboxProvider
	// Image and TTL fill the spec Provision builds. Empty means SeatImage and SeatTTL.
	Image string
	TTL   time.Duration
	// Log records a Destroy that failed. Nil logs nothing.
	Log *slog.Logger

	mu sync.Mutex
	// held maps a task id to its seat. An empty container marks a Create
	// still in flight.
	held map[string]domain.Seat
}

// Ready reports whether Seats can create a sandbox.
func (s *Seats) Ready() bool {
	return s != nil && s.Provider != nil
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

// Start admits spec, creates its sandbox and remembers it for taskID. A
// blank spec task takes taskID; a different one is refused with ErrSpec.
func (s *Seats) Start(ctx context.Context, taskID string, spec Spec) (Sandbox, error) {
	if !s.Ready() {
		return Sandbox{}, ErrSandboxdUnset
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

	s.mu.Lock()
	if s.held == nil {
		s.held = map[string]domain.Seat{}
	}
	if _, taken := s.held[key]; taken {
		s.mu.Unlock()
		return Sandbox{}, ErrSeated
	}
	s.held[key] = domain.Seat{}
	s.mu.Unlock()

	box, err := s.Provider.Create(ctx, spec)

	s.mu.Lock()
	seat, still := s.held[key]
	pending := still && seat.Container == ""
	if err != nil {
		if pending {
			delete(s.held, key)
		}
		s.mu.Unlock()
		return Sandbox{}, err
	}
	held, herr := domain.Hold(box.ID)
	if !pending || herr != nil {
		if pending {
			delete(s.held, key)
		}
		s.mu.Unlock()
		if herr != nil {
			return Sandbox{}, herr
		}
		// Best effort: a failure is logged and sandboxd reclaims it at its TTL.
		_ = s.destroy(context.WithoutCancel(ctx), key, box.ID)
		return Sandbox{}, ErrReleased
	}
	s.held[key] = held
	s.mu.Unlock()
	return box, nil
}

// Release destroys the sandbox taskID holds and forgets it. A task that
// holds none, or a nil Seats, is a no-op. A Create still in flight is
// destroyed by Start once it returns. A sandbox sandboxd no longer has is
// forgotten. Any other Destroy failure keeps the seat, so a later Release
// can retry, and sandboxd still reclaims the sandbox at its TTL.
func (s *Seats) Release(ctx context.Context, taskID string) error {
	if s == nil {
		return nil
	}
	key, ok := taskKey(taskID)
	if !ok {
		return ErrSpec
	}
	s.mu.Lock()
	seat, held := s.held[key]
	if !held {
		s.mu.Unlock()
		return nil
	}
	if seat.Container == "" {
		delete(s.held, key)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if err := s.destroy(ctx, key, seat.Container); err != nil {
		return err
	}
	s.mu.Lock()
	if s.held[key].Container == seat.Container {
		delete(s.held, key)
	}
	s.mu.Unlock()
	return nil
}

// Held returns the sandbox id taskID holds, if any.
func (s *Seats) Held(taskID string) (string, bool) {
	key, ok := taskKey(taskID)
	if s == nil || !ok {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seat, held := s.held[key]
	if !held || seat.Container == "" {
		return "", false
	}
	return seat.Container, true
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
