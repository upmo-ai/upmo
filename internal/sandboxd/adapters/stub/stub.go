// Package stub is an in-memory sandbox driver. It starts no container and
// isolates nothing, so it accepts only the container tier. Real drivers
// replace it without a change to brain.
package stub

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// Driver holds sandboxes in memory. The zero value is not usable; call New.
type Driver struct {
	mu    sync.Mutex
	boxes map[string]box
	now   func() time.Time
}

type box struct {
	spec    runtime.Spec
	expires time.Time
	paused  bool
}

var _ runtime.SandboxProvider = (*Driver)(nil)

// New returns an empty driver. now is the clock; nil means time.Now.
func New(now func() time.Time) *Driver {
	if now == nil {
		now = time.Now
	}
	return &Driver{boxes: map[string]box{}, now: now}
}

// Create admits spec and records a sandbox until its TTL passes.
func (d *Driver) Create(ctx context.Context, spec runtime.Spec) (runtime.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Sandbox{}, err
	}
	spec, err := runtime.CheckSpec(spec)
	if err != nil {
		return runtime.Sandbox{}, err
	}
	if spec.Tier != runtime.TierContainer {
		return runtime.Sandbox{}, runtime.ErrIsolation
	}
	id, err := uuid.NewV7()
	if err != nil {
		return runtime.Sandbox{}, err
	}
	expires := d.now().UTC().Add(spec.TTL)
	d.mu.Lock()
	d.boxes[id.String()] = box{spec: spec, expires: expires}
	d.mu.Unlock()
	return runtime.Sandbox{ID: id.String(), Tier: spec.Tier, Shape: runtime.ResourceShape(), Expires: expires}, nil
}

// Pause marks the sandbox paused.
func (d *Driver) Pause(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.live(id)
	if !ok {
		return runtime.ErrNotFound
	}
	b.paused = true
	d.boxes[strings.TrimSpace(id)] = b
	return nil
}

// Destroy forgets the sandbox.
func (d *Driver) Destroy(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.live(id); !ok {
		return runtime.ErrNotFound
	}
	delete(d.boxes, strings.TrimSpace(id))
	return nil
}

// Egress returns the allow-list and credentials of a live, unpaused sandbox
// for the egress proxy.
func (d *Driver) Egress(id string) ([]string, []runtime.Credential, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b, ok := d.live(id)
	if !ok || b.paused {
		return nil, nil, false
	}
	return append([]string(nil), b.spec.Allow...), append([]runtime.Credential(nil), b.spec.Credentials...), true
}

// live returns the sandbox for id and drops it once its TTL has passed.
// The caller holds d.mu.
func (d *Driver) live(id string) (box, bool) {
	id = strings.TrimSpace(id)
	b, ok := d.boxes[id]
	if !ok {
		return box{}, false
	}
	if !d.now().UTC().Before(b.expires) {
		delete(d.boxes, id)
		return box{}, false
	}
	return b, true
}

// Exec is not built in the stub driver.
func (d *Driver) Exec(context.Context, string, runtime.Command, func(runtime.Output) error) (runtime.Exit, error) {
	return runtime.Exit{}, runtime.ErrUnsupported
}

// PTY is not built in the stub driver.
func (d *Driver) PTY(context.Context, string, runtime.Size) (io.ReadWriteCloser, error) {
	return nil, runtime.ErrUnsupported
}

// ReadFile is not built in the stub driver.
func (d *Driver) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, runtime.ErrUnsupported
}

// WriteFile is not built in the stub driver.
func (d *Driver) WriteFile(context.Context, string, string, []byte) error {
	return runtime.ErrUnsupported
}

// Expose is not built in the stub driver.
func (d *Driver) Expose(context.Context, string, int) (runtime.Endpoint, error) {
	return runtime.Endpoint{}, runtime.ErrUnsupported
}

// StartEditor is not built in the stub driver.
func (d *Driver) StartEditor(context.Context, string, string) (runtime.Endpoint, error) {
	return runtime.Endpoint{}, runtime.ErrUnsupported
}

// StopEditor is not built in the stub driver.
func (d *Driver) StopEditor(context.Context, string) error {
	return runtime.ErrUnsupported
}
