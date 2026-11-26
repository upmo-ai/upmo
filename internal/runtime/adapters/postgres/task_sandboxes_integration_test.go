//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/runtime/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const task = "018f3b2c-7a10-7c4a-8e11-111111111111"

// hash is the shape of a stored proxy secret hash: 32 bytes.
var hash = bytes.Repeat([]byte{0x61}, 32)

func TestMigrationRecordsItsVersionInTheRuntimeSchema(t *testing.T) {
	owner := up(t)
	var n int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM runtime.goose_db_version WHERE version_id > 0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("runtime versions = %d, want 1", n)
	}
	if err := migrate.Down(context.Background(), owner, runtime.Schema, runtime.Migrations()); err != nil {
		t.Fatal(err)
	}
	var gone bool
	if err := owner.QueryRow(context.Background(), `SELECT to_regnamespace('runtime') IS NULL`).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if !gone {
		t.Fatal("runtime schema left after down")
	}
}

func TestSeatClaimFillAndDrop(t *testing.T) {
	owner := up(t)
	book := postgres.TaskSandboxes{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	if _, held, err := book.Lookup(ctx, task); err != nil || held {
		t.Fatalf("row before claim = %t %v", held, err)
	}
	if err := book.Claim(ctx, task, 10*time.Minute, hash); err != nil {
		t.Fatal(err)
	}
	if err := book.Claim(ctx, task, 10*time.Minute, hash); !errors.Is(err, runtime.ErrAlreadyHeld) {
		t.Fatalf("second claim = %v", err)
	}
	if id, held, err := book.Lookup(ctx, task); err != nil || !held || id != "" {
		t.Fatalf("pending = %q %t %v", id, held, err)
	}
	var lapse int64
	if err := owner.QueryRow(ctx, `SELECT extract(epoch FROM expires_at - created_at)::bigint FROM runtime.task_sandboxes WHERE task_id = $1`, task).Scan(&lapse); err != nil {
		t.Fatal(err)
	}
	if lapse != 600 {
		t.Fatalf("claim lapses after %d seconds", lapse)
	}

	expires := time.Date(2026, 11, 1, 12, 30, 0, 0, time.UTC)
	if ok, err := book.Fill(ctx, task, "box-1", expires); err != nil || !ok {
		t.Fatalf("fill = %t %v", ok, err)
	}
	if ok, err := book.Fill(ctx, task, "box-2", time.Time{}); err != nil || ok {
		t.Fatalf("fill of a held row = %t %v", ok, err)
	}
	if id, held, err := book.Lookup(ctx, task); err != nil || !held || id != "box-1" {
		t.Fatalf("held = %q %t %v", id, held, err)
	}
	var stored time.Time
	if err := owner.QueryRow(ctx, `SELECT expires_at FROM runtime.task_sandboxes WHERE task_id = $1`, task).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(expires) {
		t.Fatalf("expires = %v", stored)
	}

	// Dropping a pending id or another sandbox leaves the held row.
	for _, id := range []string{"", "box-2"} {
		if err := book.Drop(ctx, task, id); err != nil {
			t.Fatal(err)
		}
		if _, held, _ := book.Lookup(ctx, task); !held {
			t.Fatalf("drop %q removed the held row", id)
		}
	}
	if err := book.Drop(ctx, task, "box-1"); err != nil {
		t.Fatal(err)
	}
	if _, held, err := book.Lookup(ctx, task); err != nil || held {
		t.Fatalf("row after drop = %t %v", held, err)
	}
	if err := book.Drop(ctx, task, "box-1"); err != nil {
		t.Fatalf("drop of a gone row = %v", err)
	}
}

func TestSeatFillAfterADroppedClaimRecordsNothing(t *testing.T) {
	owner := up(t)
	book := postgres.TaskSandboxes{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	if err := book.Claim(ctx, task, time.Minute, hash); err != nil {
		t.Fatal(err)
	}
	if err := book.Drop(ctx, task, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := book.Fill(ctx, task, "box-1", time.Time{}); err != nil || ok {
		t.Fatalf("fill = %t %v", ok, err)
	}
	if _, held, err := book.Lookup(ctx, task); err != nil || held {
		t.Fatalf("held = %t %v", held, err)
	}
}

func TestSeatRefusesABadRow(t *testing.T) {
	owner := up(t)
	book := postgres.TaskSandboxes{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	if err := book.Claim(ctx, "00000000-0000-0000-0000-000000000000", time.Minute, hash); err == nil {
		t.Fatal("claimed a nil task id")
	}
	if err := book.Claim(ctx, task, time.Minute, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := book.Fill(ctx, task, " box-1", time.Time{}); err == nil {
		t.Fatal("filled an untrimmed sandbox id")
	}
}

func TestSeatsReleaseAfterRestart(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	provider := &fakeProvider{}

	before := &runtime.TaskSandboxes{Provider: provider, Store: runtime.NewTaskSandboxStore(pgtest.App(t, owner))}
	if needed, err := before.Provision(ctx, task, "org/name", nil, nil); err != nil || !needed {
		t.Fatalf("provision = %t %v", needed, err)
	}
	if err := before.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	// A new process with a new pool finds the row and releases it.
	after := &runtime.TaskSandboxes{Provider: provider, Store: runtime.NewTaskSandboxStore(pgtest.App(t, owner))}
	if id, ok, err := after.Held(ctx, task); err != nil || !ok || id != "box-1" {
		t.Fatalf("held after restart = %q %t %v", id, ok, err)
	}
	if err := after.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if got := provider.destroyedIDs(); !slices.Equal(got, []string{"box-1"}) {
		t.Fatalf("destroyed = %v", got)
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM runtime.task_sandboxes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rows after release = %d", n)
	}
}

func TestSeatsReleaseKeepsTheSeatWhenDestroyFails(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	provider := &fakeProvider{}
	boxes := &runtime.TaskSandboxes{Provider: provider, Store: runtime.NewTaskSandboxStore(pgtest.App(t, owner))}
	if _, err := boxes.Provision(ctx, task, "org/name", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := boxes.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	provider.fail(errors.New("sandboxd down"))
	if err := boxes.Release(ctx, task); err == nil {
		t.Fatal("release hid the destroy failure")
	}
	if _, ok, err := boxes.Held(ctx, task); err != nil || !ok {
		t.Fatalf("binding forgotten after a failed destroy: %v", err)
	}
	provider.fail(runtime.ErrNotFound)
	if err := boxes.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := boxes.Held(ctx, task); err != nil || ok {
		t.Fatalf("binding kept after sandboxd lost the sandbox: %v", err)
	}
}

// Start chooses the proxy secret: the row upmo_app inserts holds the SHA-256
// of the exact secret Create received, and a second Start picks a new one.
func TestStartStoresTheHashOfTheSecretCreateSent(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	provider := &fakeProvider{}
	boxes := &runtime.TaskSandboxes{Provider: provider, Store: runtime.NewTaskSandboxStore(pgtest.App(t, owner))}
	stored := func() []byte {
		t.Helper()
		var h []byte
		if err := owner.QueryRow(ctx, `SELECT proxy_secret_hash FROM runtime.task_sandboxes WHERE task_id = $1`, task).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	spec := runtime.Spec{Image: "upmo/sandbox:base", Tier: runtime.TierContainer, TTL: time.Minute, ProxySecret: bytes.Repeat([]byte{0x7a}, 32)}
	if _, err := boxes.Start(ctx, task, spec); err != nil {
		t.Fatal(err)
	}
	first := stored()
	provider.mu.Lock()
	sent := slices.Clone(provider.secrets)
	provider.mu.Unlock()
	if len(sent) != 1 || len(sent[0]) != 32 {
		t.Fatalf("secrets sent = %x, want one of 32 bytes", sent)
	}
	if bytes.Equal(sent[0], spec.ProxySecret) {
		t.Fatal("Start sent the caller's secret, want its own")
	}
	if sum := sha256.Sum256(sent[0]); !bytes.Equal(first, sum[:]) {
		t.Fatalf("stored hash = %x, want SHA-256 of the sent secret %x", first, sum)
	}
	if err := boxes.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := boxes.Start(ctx, task, spec); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	second := slices.Clone(provider.secrets[1])
	provider.mu.Unlock()
	if bytes.Equal(second, sent[0]) {
		t.Fatal("second Start reused the secret")
	}
	if sum := sha256.Sum256(second); !bytes.Equal(stored(), sum[:]) {
		t.Fatalf("second stored hash = %x, want %x", stored(), sum)
	}
}

// up migrates identity first, because it creates upmo_app, then runtime.
func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, runtime.Schema, runtime.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}

// fakeProvider creates "box-1" and records Destroy.
type fakeProvider struct {
	mu         sync.Mutex
	destroyed  []string
	destroyErr error
	secrets    [][]byte
}

var _ runtime.SandboxProvider = (*fakeProvider)(nil)

func (f *fakeProvider) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyErr = err
}

func (f *fakeProvider) destroyedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.destroyed)
}

func (f *fakeProvider) Create(_ context.Context, spec runtime.Spec) (runtime.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets = append(f.secrets, slices.Clone(spec.ProxySecret))
	return runtime.Sandbox{ID: "box-1", Tier: spec.Tier, Expires: time.Now().Add(spec.TTL)}, nil
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

func (f *fakeProvider) Exec(context.Context, string, runtime.Command, func(runtime.Output) error) (runtime.Exit, error) {
	return runtime.Exit{}, runtime.ErrUnsupported
}

func (f *fakeProvider) PTY(context.Context, string, runtime.Size) (io.ReadWriteCloser, error) {
	return nil, runtime.ErrUnsupported
}

func (f *fakeProvider) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, runtime.ErrUnsupported
}

func (f *fakeProvider) WriteFile(context.Context, string, string, []byte) error {
	return runtime.ErrUnsupported
}

func (f *fakeProvider) Expose(context.Context, string, int) (runtime.Endpoint, error) {
	return runtime.Endpoint{}, runtime.ErrUnsupported
}

func (f *fakeProvider) Pause(context.Context, string) error { return runtime.ErrUnsupported }

func (f *fakeProvider) StartEditor(context.Context, string, string) (runtime.Endpoint, error) {
	return runtime.Endpoint{}, runtime.ErrUnsupported
}

func (f *fakeProvider) StopEditor(context.Context, string) error { return runtime.ErrUnsupported }
