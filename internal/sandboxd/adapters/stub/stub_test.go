package stub

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/egress"
)

func spec() runtime.Spec {
	return runtime.Spec{
		Task:        "018f3b2c-7a10-7c4a-8e11-111111111111",
		Image:       "upmo/sandbox:base",
		Tier:        runtime.TierContainer,
		TTL:         time.Hour,
		Allow:       []string{"github.com"},
		Credentials: []runtime.Credential{{Sentinel: "upmo_sentinel_" + strings.Repeat("a", 64), Ref: "github_token", Host: "github.com", Methods: []string{"GET"}, Paths: []string{"/user"}}},
	}
}

func TestCreatePauseDestroy(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	d := New(func() time.Time { return now })
	ctx := context.Background()

	box, err := d.Create(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if box.ID == "" || box.Tier != runtime.TierContainer || box.Shape != runtime.ResourceShape() || !box.Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("box = %+v", box)
	}
	allow, creds, ok := d.Egress(box.ID)
	if !ok || len(allow) != 1 || allow[0] != "github.com" || len(creds) != 1 {
		t.Fatalf("egress = %v %v %v", allow, creds, ok)
	}

	if err := d.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := d.Egress(box.ID); ok {
		t.Fatal("a paused sandbox has egress")
	}
	if err := d.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.Destroy(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("second destroy err = %v", err)
	}
	if err := d.Pause(ctx, box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("pause after destroy err = %v", err)
	}
}

func TestCreateRefuses(t *testing.T) {
	d := New(nil)
	bad := spec()
	bad.Allow = []string{"127.0.0.1"}
	if _, err := d.Create(context.Background(), bad); !errors.Is(err, runtime.ErrSpec) {
		t.Fatalf("spec err = %v", err)
	}
	for _, tier := range []string{runtime.TierGVisor, runtime.TierKata} {
		s := spec()
		s.Tier = tier
		if _, err := d.Create(context.Background(), s); !errors.Is(err, runtime.ErrIsolation) {
			t.Fatalf("%s err = %v, want ErrIsolation", tier, err)
		}
	}
}

func TestTTLExpiry(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	d := New(func() time.Time { return now })
	box, err := d.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, _, ok := d.Egress(box.ID); ok {
		t.Fatal("an expired sandbox has egress")
	}
	if err := d.Destroy(context.Background(), box.ID); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("destroy after expiry err = %v", err)
	}
}

func TestStubAuthenticateRejectsPausedAndExpired(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	d := New(func() time.Time { return now })
	box, err := d.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(strings.Repeat("s", 32))
	ip := netip.MustParseAddr("10.0.0.2")
	d.recordProxyIdentityForTest(box.ID, ip, secret)
	if p, ok := d.Authenticate(ip, secret); !ok || p.ID != box.ID {
		t.Fatalf("Authenticate = %+v %t", p, ok)
	}
	if release, _, ok := d.Acquire(box.ID, 1); !ok {
		t.Fatal("Acquire refused live sandbox")
	} else {
		release()
	}
	if err := d.Pause(context.Background(), box.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Authenticate(ip, secret); ok {
		t.Fatal("authenticated paused sandbox")
	}
	d2 := New(func() time.Time { return now })
	expired, err := d2.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	d2.recordProxyIdentityForTest(expired.ID, ip, secret)
	now = now.Add(time.Hour)
	if _, ok := d2.Authenticate(ip, secret); ok {
		t.Fatal("authenticated expired sandbox")
	}
}

func TestStubRevokeTimeoutWaitsForHeldLease(t *testing.T) {
	var output bytes.Buffer
	secret := []byte(strings.Repeat("s", 32))
	d := NewWithLogger(nil, slog.New(slog.NewJSONHandler(&output, nil)))
	box, err := d.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("10.0.0.7")
	d.recordProxyIdentityForTest(box.ID, ip, secret)
	p, ok := d.Authenticate(ip, secret)
	if !ok {
		t.Fatal("Authenticate refused sandbox")
	}
	release, held, ok := d.Acquire(box.ID, p.Lease)
	if !ok {
		t.Fatal("Acquire refused sandbox")
	}
	start := time.Now()
	err = d.Pause(context.Background(), box.ID)
	if !errors.Is(err, egress.ErrRevokeTimeout) || time.Since(start) < egress.RevokeTimeout-100*time.Millisecond {
		t.Fatalf("Pause=%v after %s", err, time.Since(start))
	}
	if held.Err() == nil {
		t.Fatal("Pause did not cancel held request")
	}
	release()
	line := output.String()
	if !strings.Contains(line, `"msg":"lease_revoke_timeout"`) || !strings.Contains(line, `"sandbox":"`+box.ID+`"`) || !strings.Contains(line, `"reason":"pause"`) || strings.Contains(line, string(secret)) {
		t.Fatalf("timeout log=%s", line)
	}
}

func TestUnbuiltMethods(t *testing.T) {
	d := New(nil)
	ctx := context.Background()
	if _, err := d.Exec(ctx, "x", runtime.Command{}, nil); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("exec")
	}
	if _, err := d.PTY(ctx, "x", runtime.Size{}); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("pty")
	}
	if _, err := d.ReadFile(ctx, "x", "a"); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("read")
	}
	if err := d.WriteFile(ctx, "x", "a", nil); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("write")
	}
	if _, err := d.Expose(ctx, "x", 3000); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("expose")
	}
	if _, err := d.StartEditor(ctx, "x", "Working"); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("start editor")
	}
	if err := d.StopEditor(ctx, "x"); !errors.Is(err, runtime.ErrUnsupported) {
		t.Error("stop editor")
	}
}
