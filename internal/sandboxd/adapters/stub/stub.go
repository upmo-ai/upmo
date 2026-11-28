// Package stub is an in-memory sandbox driver. It starts no container and
// isolates nothing, so it accepts only the container tier. Real drivers
// replace it without a change to brain.
package stub

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/egress"
)

// Driver holds sandboxes in memory. The zero value is not usable; call New.
type Driver struct {
	mu    sync.Mutex
	boxes map[string]box
	now   func() time.Time
	log   *slog.Logger
	cond  *sync.Cond
}

type box struct {
	spec     runtime.Spec
	expires  time.Time
	paused   bool
	ip       netip.Addr
	secret   [32]byte
	lease    uint64
	ctx      context.Context
	cancel   context.CancelFunc
	inFlight int
}

var _ runtime.SandboxProvider = (*Driver)(nil)

// New returns an empty driver. now is the clock; nil means time.Now.
func New(now func() time.Time) *Driver {
	if now == nil {
		now = time.Now
	}
	d := &Driver{boxes: map[string]box{}, now: now}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func NewWithLogger(now func() time.Time, log *slog.Logger) *Driver {
	d := New(now)
	d.log = log
	return d
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
	leaseCtx, cancel := context.WithCancel(context.Background())
	d.boxes[id.String()] = box{spec: spec, expires: expires, lease: 1, ctx: leaseCtx, cancel: cancel}
	d.mu.Unlock()
	return runtime.Sandbox{ID: id.String(), Tier: spec.Tier, Shape: runtime.ResourceShape(), Expires: expires}, nil
}

// Pause marks the sandbox paused.
func (d *Driver) Pause(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	d.mu.Lock()
	b, ok := d.live(id)
	if !ok {
		d.mu.Unlock()
		return runtime.ErrNotFound
	}
	b.paused = true
	d.boxes[id] = b
	d.mu.Unlock()
	return d.revoke(id, "pause")
}

// Destroy forgets the sandbox.
func (d *Driver) Destroy(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	d.mu.Lock()
	if _, ok := d.live(id); !ok {
		d.mu.Unlock()
		return runtime.ErrNotFound
	}
	d.mu.Unlock()
	revokeErr := d.revoke(id, "destroy")
	d.mu.Lock()
	delete(d.boxes, id)
	d.mu.Unlock()
	return revokeErr
}

// ActiveSeats returns the number of live, unexpired sandboxes held by this driver.
func (d *Driver) ActiveSeats() int {
	now := d.now().UTC()
	d.mu.Lock()
	defer d.mu.Unlock()
	count := 0
	for id, b := range d.boxes {
		if !now.Before(b.expires) {
			delete(d.boxes, id)
			continue
		}
		count++
	}
	return count
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

func (d *Driver) Authenticate(source netip.Addr, secret []byte) (egress.Principal, bool) {
	got := sha256.Sum256(secret)
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, b := range d.boxes {
		equal := subtle.ConstantTimeCompare(got[:], b.secret[:])
		if source.IsValid() && source == b.ip && len(secret) == 32 && equal == 1 && b.lease != 0 && !b.paused && d.now().UTC().Before(b.expires) {
			return egress.Principal{ID: id, Allow: append([]string(nil), b.spec.Allow...), Creds: append([]runtime.Credential(nil), b.spec.Credentials...), Lease: b.lease}, true
		}
	}
	var dummy [32]byte
	_ = subtle.ConstantTimeCompare(got[:], dummy[:])
	return egress.Principal{}, false
}

// Acquire fails closed until the stub tracks request leases.
func (d *Driver) Acquire(id string, token uint64) (func(), context.Context, bool) {
	d.mu.Lock()
	b, ok := d.boxes[id]
	if !ok || token == 0 || b.lease != token || b.paused || !d.now().UTC().Before(b.expires) {
		d.mu.Unlock()
		return nil, context.Background(), false
	}
	b.inFlight++
	d.boxes[id] = b
	held, cancel := context.WithCancel(b.ctx)
	d.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			d.mu.Lock()
			if cur, ok := d.boxes[id]; ok {
				cur.inFlight--
				d.boxes[id] = cur
			}
			d.cond.Broadcast()
			d.mu.Unlock()
		})
	}, held, true
}

func (d *Driver) recordProxyIdentityForTest(id string, ip netip.Addr, secret []byte) {
	d.mu.Lock()
	b := d.boxes[id]
	b.ip = ip
	b.secret = sha256.Sum256(secret)
	d.boxes[id] = b
	d.mu.Unlock()
}

func (d *Driver) revoke(id, reason string) error {
	d.mu.Lock()
	b, ok := d.boxes[id]
	if !ok {
		d.mu.Unlock()
		return nil
	}
	b.lease = 0
	b.ip = netip.Addr{}
	clear(b.secret[:])
	if b.cancel != nil {
		b.cancel()
	}
	d.boxes[id] = b
	timed := false
	timer := time.AfterFunc(egress.RevokeTimeout, func() { d.mu.Lock(); timed = true; d.cond.Broadcast(); d.mu.Unlock() })
	for b.inFlight > 0 && !timed {
		d.cond.Wait()
		b = d.boxes[id]
	}
	timer.Stop()
	d.mu.Unlock()
	if timed {
		if d.log != nil {
			d.log.Warn("lease_revoke_timeout", "sandbox", id, "reason", reason)
		}
		return egress.ErrRevokeTimeout
	}
	return nil
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
