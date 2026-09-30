//go:build integration

package sandboxd

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/docker"
	"github.com/upmo-ai/upmo/internal/testutil/seatbook"
)

// testNetwork keeps the suite off the sandbox network a running stack uses,
// like the driver's own integration tests.
func testNetwork() string {
	if v := os.Getenv("UPMO_TEST_SANDBOX_NETWORK"); v != "" {
		return v
	}
	return docker.DefaultNetwork + "-test"
}

// TestBrainClientOverDocker runs brain's runtime client against the real
// sandboxd routes and the Docker driver. It reads UPMO_TEST_DOCKER_HOST like
// the driver's own integration tests: unset skips, set but unreachable fails.
func TestBrainClientOverDocker(t *testing.T) {
	host := os.Getenv("UPMO_TEST_DOCKER_HOST")
	if host == "" {
		t.Skip("UPMO_TEST_DOCKER_HOST is not set; make test-integration sets it")
	}
	t.Setenv("DOCKER_HOST", host)
	openCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driver, err := docker.Open(openCtx, docker.Options{Network: testNetwork()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })

	key := serviceKey('k')
	h, err := Routes(key, "", driver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	s := spec()
	s.Image = "debian:bookworm-slim"
	if v := os.Getenv("UPMO_TEST_SANDBOX_IMAGE"); v != "" {
		s.Image = v
	}
	box, err := provider.Create(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	destroyed := false
	t.Cleanup(func() {
		if !destroyed {
			_ = provider.Destroy(context.Background(), box.ID)
		}
	})

	run := func(line string, timeout time.Duration) (string, string, int, error) {
		var stdout, stderr strings.Builder
		exit, err := provider.Exec(ctx, box.ID, runtime.Command{Args: []string{"sh", "-c", line}, Timeout: timeout},
			func(o runtime.Output) error {
				if o.Stream == "stderr" {
					stderr.Write(o.Data)
				} else {
					stdout.Write(o.Data)
				}
				return nil
			})
		return stdout.String(), stderr.String(), exit.Code, err
	}

	out, errOut, code, err := run("echo out; echo err >&2; exit 3", time.Minute)
	if err != nil || out != "out\n" || errOut != "err\n" || code != 3 {
		t.Fatalf("exec = %q %q %d %v", out, errOut, code, err)
	}
	// Output larger than one frame arrives whole and in order.
	out, _, code, err = run("seq 1 20000", time.Minute)
	if err != nil || code != 0 || !strings.HasPrefix(out, "1\n2\n") || !strings.HasSuffix(out, "\n20000\n") || strings.Count(out, "\n") != 20000 {
		t.Fatalf("seq = %d bytes, code %d, %v", len(out), code, err)
	}

	data := []byte("hi from brain\x00\n")
	if err := provider.WriteFile(ctx, box.ID, "hello.bin", data); err != nil {
		t.Fatal(err)
	}
	if out, _, _, err := run("od -An -c /scratch/hello.bin | tr -s ' '", time.Minute); err != nil || !strings.Contains(out, `\0 \n`) {
		t.Fatalf("od = %q %v", out, err)
	}
	if _, _, _, err := run("printf made-inside > /scratch/inside.txt", time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, err := provider.ReadFile(ctx, box.ID, "/scratch/inside.txt"); err != nil || string(got) != "made-inside" {
		t.Fatalf("read = %q %v", got, err)
	}
	if _, err := provider.ReadFile(ctx, box.ID, "/scratch/none"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file err = %v", err)
	}
	if _, err := provider.ReadFile(ctx, box.ID, "/scratch"); !errors.Is(err, runtime.ErrNotFile) {
		t.Fatalf("directory err = %v", err)
	}
	if err := provider.WriteFile(ctx, box.ID, "/missing/x", nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing parent err = %v", err)
	}

	start := time.Now()
	out, _, _, err = run("echo started; sleep 30", time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || out != "started\n" {
		t.Fatalf("timeout = %q %v", out, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("timeout returned after %v", took)
	}

	if err := provider.Pause(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Exec(ctx, box.ID, runtime.Command{Args: []string{"true"}}, nil); !errors.Is(err, runtime.ErrPaused) {
		t.Fatalf("exec on paused err = %v", err)
	}
	if got, err := provider.ReadFile(ctx, box.ID, "hello.bin"); err != nil || string(got) != string(data) {
		t.Fatalf("read on paused = %q %v", got, err)
	}

	if err := provider.Destroy(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	destroyed = true
	if _, err := provider.Exec(ctx, box.ID, runtime.Command{Args: []string{"true"}}, nil); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("exec after destroy err = %v", err)
	}
}

// TestBrainSeatsProvisionOverDocker runs the Preparing path brain takes on
// the real sandboxd routes and the Docker driver: a task without a
// repository reaches ready, and a task with one fails in the clone phase,
// because a seat's allow-list is empty and no egress proxy serves this test
// (and the default image has no git).
func TestBrainSeatsProvisionOverDocker(t *testing.T) {
	host := os.Getenv("UPMO_TEST_DOCKER_HOST")
	if host == "" {
		t.Skip("UPMO_TEST_DOCKER_HOST is not set; make test-integration sets it")
	}
	t.Setenv("DOCKER_HOST", host)
	openCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driver, err := docker.Open(openCtx, docker.Options{Network: testNetwork()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	key := serviceKey('k')
	h, err := Routes(key, "", driver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	seats := &runtime.Seats{Provider: provider, Book: &seatbook.Memory{}, TTL: 5 * time.Minute}
	if v := os.Getenv("UPMO_TEST_SANDBOX_IMAGE"); v != "" {
		seats.Image = v
	}
	ctx := context.Background()
	// provision returns once the seat is held; the phases run on, and their
	// outcome arrives on the channel.
	provision := func(t *testing.T, task, repo string, classes []runtime.Class) error {
		t.Helper()
		outcome := make(chan error, 1)
		if _, err := seats.Provision(ctx, task, repo, classes, func(_ context.Context, err error) error {
			outcome <- err
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-outcome:
			return err
		case <-time.After(3 * time.Minute):
			t.Fatal("provisioning did not finish")
			return nil
		}
	}

	t.Run("no repository", func(t *testing.T) {
		task := "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
		t.Cleanup(func() { _ = seats.Release(context.Background(), task) })
		if err := provision(t, task, "", []runtime.Class{runtime.ClassShell}); err != nil {
			t.Fatal(err)
		}
		if rec, ok := seats.Progress(task); !ok || rec.Phase != runtime.PhaseReady {
			t.Fatalf("progress = %+v %t", rec, ok)
		}
		id, ok, err := seats.Held(ctx, task)
		if err != nil || !ok {
			t.Fatalf("held = %t %v", ok, err)
		}
		marker, err := provider.ReadFile(ctx, id, "/scratch/.upmo-provision")
		if err != nil || !strings.Contains(string(marker), "repo: none") {
			t.Fatalf("marker = %q %v", marker, err)
		}
		if err := seats.Release(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := provider.Destroy(ctx, id); !errors.Is(err, runtime.ErrNotFound) {
			t.Fatalf("sandbox kept after release: %v", err)
		}
	})

	t.Run("repository without network", func(t *testing.T) {
		task := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
		t.Cleanup(func() { _ = seats.Release(context.Background(), task) })
		err := provision(t, task, "org/name", nil)
		var phase *runtime.PhaseError
		if !errors.As(err, &phase) || phase.Phase != runtime.PhaseClone || !errors.Is(err, runtime.ErrCommand) {
			t.Fatalf("provision = %v", err)
		}
		t.Logf("clone failed as expected: %v", err)
		if rec, ok := seats.Progress(task); !ok || rec.Phase != runtime.PhaseFailed {
			t.Fatalf("progress = %+v %t", rec, ok)
		}
	})
}
