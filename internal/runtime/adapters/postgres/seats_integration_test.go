//go:build integration

package postgres_test

import (
	"context"
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

func TestMigrationRecordsItsVersionInTheRuntimeSchema(t *testing.T) {
	owner := up(t)
	var n int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM runtime.goose_db_version WHERE version_id > 0`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("runtime versions = %d", n)
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
	book := postgres.Seats{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	if _, held, err := book.Seat(ctx, task); err != nil || held {
		t.Fatalf("seat before claim = %t %v", held, err)
	}
	if err := book.Claim(ctx, task, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := book.Claim(ctx, task, 10*time.Minute); !errors.Is(err, runtime.ErrSeated) {
		t.Fatalf("second claim = %v", err)
	}
	if id, held, err := book.Seat(ctx, task); err != nil || !held || id != "" {
		t.Fatalf("pending seat = %q %t %v", id, held, err)
	}
	var lapse int64
	if err := owner.QueryRow(ctx, `SELECT extract(epoch FROM expires_at - created_at)::bigint FROM runtime.seats WHERE task_id = $1`, task).Scan(&lapse); err != nil {
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
		t.Fatalf("fill of a held seat = %t %v", ok, err)
	}
	if id, held, err := book.Seat(ctx, task); err != nil || !held || id != "box-1" {
		t.Fatalf("held seat = %q %t %v", id, held, err)
	}
	var stored time.Time
	if err := owner.QueryRow(ctx, `SELECT expires_at FROM runtime.seats WHERE task_id = $1`, task).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(expires) {
		t.Fatalf("expires = %v", stored)
	}

	// Dropping the pending seat or another sandbox leaves the held seat.
	for _, id := range []string{"", "box-2"} {
		if err := book.Drop(ctx, task, id); err != nil {
			t.Fatal(err)
		}
		if _, held, _ := book.Seat(ctx, task); !held {
			t.Fatalf("drop %q removed the held seat", id)
		}
	}
	if err := book.Drop(ctx, task, "box-1"); err != nil {
		t.Fatal(err)
	}
	if _, held, err := book.Seat(ctx, task); err != nil || held {
		t.Fatalf("seat after drop = %t %v", held, err)
	}
	if err := book.Drop(ctx, task, "box-1"); err != nil {
		t.Fatalf("drop of a gone seat = %v", err)
	}
}

func TestSeatFillAfterADroppedClaimRecordsNothing(t *testing.T) {
	owner := up(t)
	book := postgres.Seats{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	if err := book.Claim(ctx, task, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := book.Drop(ctx, task, ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := book.Fill(ctx, task, "box-1", time.Time{}); err != nil || ok {
		t.Fatalf("fill = %t %v", ok, err)
	}
	if _, held, err := book.Seat(ctx, task); err != nil || held {
		t.Fatalf("seat = %t %v", held, err)
	}
}

func TestSeatRefusesABadRow(t *testing.T) {
	owner := up(t)
	book := postgres.Seats{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	if err := book.Claim(ctx, "00000000-0000-0000-0000-000000000000", time.Minute); err == nil {
		t.Fatal("claimed a nil task id")
	}
	if err := book.Claim(ctx, task, time.Minute); err != nil {
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

	before := &runtime.Seats{Provider: provider, Book: runtime.SeatStore(pgtest.App(t, owner))}
	if needed, err := before.Provision(ctx, task, "org/name", nil, nil); err != nil || !needed {
		t.Fatalf("provision = %t %v", needed, err)
	}
	if err := before.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	// A new process with a new pool finds the seat and releases it.
	after := &runtime.Seats{Provider: provider, Book: runtime.SeatStore(pgtest.App(t, owner))}
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
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM runtime.seats`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("seats after release = %d", n)
	}
}

func TestSeatsReleaseKeepsTheSeatWhenDestroyFails(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	provider := &fakeProvider{}
	seats := &runtime.Seats{Provider: provider, Book: runtime.SeatStore(pgtest.App(t, owner))}
	if _, err := seats.Provision(ctx, task, "org/name", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := seats.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	provider.fail(errors.New("sandboxd down"))
	if err := seats.Release(ctx, task); err == nil {
		t.Fatal("release hid the destroy failure")
	}
	if _, ok, err := seats.Held(ctx, task); err != nil || !ok {
		t.Fatalf("seat forgotten after a failed destroy: %v", err)
	}
	provider.fail(runtime.ErrNotFound)
	if err := seats.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := seats.Held(ctx, task); err != nil || ok {
		t.Fatalf("seat kept after sandboxd lost the sandbox: %v", err)
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
