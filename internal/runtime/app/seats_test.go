package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

func TestSeatsProvisionCreatesForShellAndRepo(t *testing.T) {
	cases := map[string]struct {
		repo    string
		classes []domain.Class
	}{
		"repo":  {repo: "org/name"},
		"shell": {classes: []domain.Class{domain.ClassShell}},
		"file":  {classes: []domain.Class{domain.ClassWeb, domain.ClassFile}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := &fakeProvider{}
			s := &Seats{Provider: p}
			needed, err := s.Provision(context.Background(), task, tc.repo, tc.classes)
			if err != nil || !needed {
				t.Fatalf("provision = %t %v", needed, err)
			}
			if len(p.created) != 1 {
				t.Fatalf("creates = %d", len(p.created))
			}
			spec := p.created[0]
			if spec.Task != task || spec.Image != SeatImage || spec.Tier != domain.TierContainer || spec.TTL != SeatTTL || len(spec.Allow) != 0 {
				t.Fatalf("spec = %+v", spec)
			}
			if id, ok := s.Held(task); !ok || id != "box-1" {
				t.Fatalf("held = %q %t", id, ok)
			}
		})
	}
}

func TestSeatsProvisionSkipsCreateWhenNotNeeded(t *testing.T) {
	p := &fakeProvider{}
	s := &Seats{Provider: p}
	needed, err := s.Provision(context.Background(), task, " ", []domain.Class{domain.ClassConnector, domain.ClassKnowledge})
	if err != nil || needed || len(p.created) != 0 {
		t.Fatalf("provision = %t %v creates=%d", needed, err, len(p.created))
	}
	if _, err := s.Provision(context.Background(), task, "", []domain.Class{"sideways"}); !errors.Is(err, domain.ErrClass) || len(p.created) != 0 {
		t.Fatalf("unknown class = %v creates=%d", err, len(p.created))
	}
}

func TestSeatsUsesImageAndTTL(t *testing.T) {
	p := &fakeProvider{}
	s := &Seats{Provider: p, Image: "upmo/sandbox:base", TTL: time.Minute}
	if _, err := s.Provision(context.Background(), task, "org/name", nil); err != nil {
		t.Fatal(err)
	}
	if p.created[0].Image != "upmo/sandbox:base" || p.created[0].TTL != time.Minute {
		t.Fatalf("spec = %+v", p.created[0])
	}
}

func TestSeatsWithoutProviderIsClear(t *testing.T) {
	ctx := context.Background()
	var zero Seats
	var none *Seats
	for name, s := range map[string]*Seats{"zero": &zero, "nil": none} {
		t.Run(name, func(t *testing.T) {
			if s.Ready() {
				t.Fatal("ready without a provider")
			}
			if _, err := s.Start(ctx, task, validSpec()); !errors.Is(err, ErrSandboxdUnset) {
				t.Fatalf("start = %v", err)
			}
			if needed, err := s.Provision(ctx, task, "org/name", nil); !needed || !errors.Is(err, ErrSandboxdUnset) {
				t.Fatalf("provision = %t %v", needed, err)
			}
			if err := s.Release(ctx, task); err != nil {
				t.Fatalf("release = %v", err)
			}
			if _, ok := s.Held(task); ok {
				t.Fatal("held without a provider")
			}
		})
	}
}

func TestSeatsReleaseDestroysAndForgets(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s := &Seats{Provider: p}
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, " "+task+" "); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.destroyed, []string{"box-1"}) {
		t.Fatalf("destroyed = %v", p.destroyed)
	}
	if _, ok := s.Held(task); ok {
		t.Fatal("still held after release")
	}
	if err := s.Release(ctx, task); err != nil || len(p.destroyed) != 1 {
		t.Fatalf("second release = %v destroyed=%v", err, p.destroyed)
	}
	if err := s.Release(ctx, "nope"); !errors.Is(err, ErrSpec) {
		t.Fatalf("bad id = %v", err)
	}
}

func TestSeatsRefusesASecondSeat(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s := &Seats{Provider: p}
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, task, validSpec()); !errors.Is(err, ErrSeated) || len(p.created) != 1 {
		t.Fatalf("second start = %v creates=%d", err, len(p.created))
	}
}

func TestSeatsStartRefusesASpecForAnotherTask(t *testing.T) {
	p := &fakeProvider{}
	s := &Seats{Provider: p}
	if _, err := s.Start(context.Background(), "018f3b2c-7a10-7c4a-8e11-222222222222", validSpec()); !errors.Is(err, ErrSpec) || len(p.created) != 0 {
		t.Fatalf("start = %v creates=%d", err, len(p.created))
	}
	spec := validSpec()
	spec.Tier = "microvm"
	if _, err := s.Start(context.Background(), task, spec); !errors.Is(err, ErrSpec) || len(p.created) != 0 {
		t.Fatalf("bad spec = %v creates=%d", err, len(p.created))
	}
}

func TestSeatsCreateFailureHoldsNothing(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{createErr: ErrIsolation}
	s := &Seats{Provider: p}
	if _, err := s.Start(ctx, task, validSpec()); !errors.Is(err, ErrIsolation) {
		t.Fatalf("start = %v", err)
	}
	if _, ok := s.Held(task); ok {
		t.Fatal("held after failed create")
	}
	p.createErr = nil
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatalf("retry = %v", err)
	}
}

func TestSeatsReleaseDuringCreateDestroysTheNewSandbox(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{gate: make(chan struct{}), entered: make(chan struct{})}
	s := &Seats{Provider: p}
	done := make(chan error, 1)
	go func() {
		_, err := s.Start(ctx, task, validSpec())
		done <- err
	}()
	<-p.entered
	if err := s.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	close(p.gate)
	if err := <-done; !errors.Is(err, ErrReleased) {
		t.Fatalf("start = %v", err)
	}
	if !slices.Equal(p.destroyedIDs(), []string{"box-1"}) {
		t.Fatalf("destroyed = %v", p.destroyedIDs())
	}
	if _, ok := s.Held(task); ok {
		t.Fatal("held after release during create")
	}
}

func TestSeatsDestroyFailureKeepsTheSeat(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s := &Seats{Provider: p}
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatal(err)
	}
	p.destroyErr = errors.New("sandboxd down")
	if err := s.Release(ctx, task); err == nil {
		t.Fatal("release hid the destroy failure")
	}
	if _, ok := s.Held(task); !ok {
		t.Fatal("seat forgotten after a failed destroy")
	}
	p.destroyErr = ErrNotFound
	if err := s.Release(ctx, task); err != nil {
		t.Fatalf("gone sandbox = %v", err)
	}
	if _, ok := s.Held(task); ok {
		t.Fatal("seat kept after sandboxd lost the sandbox")
	}
}

// fakeProvider records Create and Destroy. gate, when set, holds Create
// until it is closed, after signalling entered.
type fakeProvider struct {
	mu         sync.Mutex
	created    []Spec
	destroyed  []string
	createErr  error
	destroyErr error
	gate       chan struct{}
	entered    chan struct{}
}

var _ SandboxProvider = (*fakeProvider)(nil)

func (f *fakeProvider) Create(_ context.Context, spec Spec) (Sandbox, error) {
	if f.gate != nil {
		close(f.entered)
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return Sandbox{}, f.createErr
	}
	f.created = append(f.created, spec)
	return Sandbox{ID: "box-1", Tier: spec.Tier}, nil
}

func (f *fakeProvider) Destroy(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *fakeProvider) destroyedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.destroyed)
}

func (f *fakeProvider) Exec(context.Context, string, Command, func(Output) error) (Exit, error) {
	return Exit{}, ErrUnsupported
}

func (f *fakeProvider) PTY(context.Context, string, Size) (io.ReadWriteCloser, error) {
	return nil, ErrUnsupported
}

func (f *fakeProvider) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, ErrUnsupported
}

func (f *fakeProvider) WriteFile(context.Context, string, string, []byte) error {
	return ErrUnsupported
}

func (f *fakeProvider) Expose(context.Context, string, int) (Endpoint, error) {
	return Endpoint{}, ErrUnsupported
}

func (f *fakeProvider) Pause(context.Context, string) error { return ErrUnsupported }
