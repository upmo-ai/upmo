//go:build integration

package brain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestBrainNewAndExtensionOptions(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)

	provKey := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'p'}, 32))
	cfg := Config{
		Env:             "development",
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		ShutdownTimeout: 2 * time.Second,
		ProvisionKeys:   []string{provKey},
	}

	extraWorkersCalled := false
	var capturedDeps Deps

	app, err := New(ctx, cfg,
		WithPools(owner, appPool),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithExtraWorkers(func(_ *river.Workers, deps Deps) {
			extraWorkersCalled = true
			capturedDeps = deps
		}),
		WithExtraRoutes(func(mux *http.ServeMux, deps Deps) {
			mux.HandleFunc("GET /api/ee/ping", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(deps.BuildInfo.Version))
			})
		}),
	)
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer app.Close()

	if !extraWorkersCalled {
		t.Fatal("WithExtraWorkers hook was not called")
	}
	if capturedDeps.Owner == nil || capturedDeps.App == nil || capturedDeps.Members == nil || capturedDeps.Identity == nil || capturedDeps.Logger == nil {
		t.Fatalf("Deps missing required fields: %+v", capturedDeps)
	}

	// Custom route registered via WithExtraRoutes works.
	eeRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(eeRec, httptest.NewRequest(http.MethodGet, "/api/ee/ping", nil))
	if eeRec.Code != http.StatusOK || eeRec.Body.String() == "" {
		t.Fatalf("GET /api/ee/ping = %d %q", eeRec.Code, eeRec.Body.String())
	}

	// Default /api/v1/provision/status route verifies svcsig and answers 200.
	unauth := httptest.NewRecorder()
	app.Handler().ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/v1/provision/status", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth /api/v1/provision/status = %d, want 401", unauth.Code)
	}

	master, err := svcsig.ParseKey(provKey)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceProvision)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svcsig.SignRequest(derived, svcsig.AudienceProvision, http.MethodGet, "/api/v1/provision/status", "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/provision/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	statusRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(statusRec, req)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("signed /api/v1/provision/status = %d %s", statusRec.Code, statusRec.Body.String())
	}
	var payload struct {
		Ready          bool             `json:"ready"`
		SchemaVersions map[string]int64 `json:"schema_versions"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Ready || payload.SchemaVersions["identity"] != 6 {
		t.Fatalf("unexpected status payload: %+v", payload)
	}

	// The provision nonce sweep is a periodic job that runs on start, and its
	// worker is registered: a completed job proves both.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var swept int
		if err := owner.QueryRow(ctx, `
			SELECT count(*) FROM platform.river_job
			WHERE kind = 'provision_nonce_sweep' AND state = 'completed'`).Scan(&swept); err != nil {
			t.Fatal(err)
		}
		if swept > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provision nonce sweep did not complete on the brain app")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// WithProvisioner overrides default provisioning handler.
	appCustom, err := New(ctx, cfg,
		WithPools(owner, appPool),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithProvisioner(func(mux *http.ServeMux, _ Deps) {
			mux.HandleFunc("GET /api/v1/provision/status", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTeapot)
			})
		}),
	)
	if err != nil {
		t.Fatalf("brain.New(WithProvisioner): %v", err)
	}
	defer appCustom.Close()

	customRec := httptest.NewRecorder()
	appCustom.Handler().ServeHTTP(customRec, httptest.NewRequest(http.MethodGet, "/api/v1/provision/status", nil))
	if customRec.Code != http.StatusTeapot {
		t.Fatalf("custom provisioner = %d, want 418", customRec.Code)
	}
}

// New built directly from a Config runs the same guard as the loaders, so the
// compose development values refuse brain in production before any pool or
// database is touched. An empty Env is production, like an unset one.
func TestBrainNewRefusesDevelopmentValuesInProduction(t *testing.T) {
	t.Setenv("UPMO_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32)))
	cfg := Config{
		DatabaseURL:     "postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable",
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		ServiceKey:      "upmo_svc_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'g'}, 32)),
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		ShutdownTimeout: 2 * time.Second,
	}
	_, err := New(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "UPMO_MASTER_KEY uses a development or weak key") {
		t.Fatalf("New err = %v", err)
	}
	if strings.Contains(err.Error(), cfg.DatabaseURL) {
		t.Fatalf("New err holds a value: %v", err)
	}
}

type hookJobArgs struct{}

func (hookJobArgs) Kind() string { return "brain_hook_test" }

type hookJobWorker struct {
	river.WorkerDefaults[hookJobArgs]
	done chan struct{}
}

func (w *hookJobWorker) Work(context.Context, *river.Job[hookJobArgs]) error {
	w.done <- struct{}{}
	return nil
}

func TestExtensionHooksCanEnqueueJobs(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Env:             "development",
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		ShutdownTimeout: 2 * time.Second,
	}

	var workerJobs, periodicJobs JobClient
	worker := &hookJobWorker{done: make(chan struct{}, 2)}
	app, err := New(ctx, cfg,
		WithPools(owner, pgtest.App(t, owner)),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithExtraWorkers(func(workers *river.Workers, deps Deps) {
			workerJobs = deps.Jobs
			river.AddWorker(workers, worker)
		}),
		WithExtraPeriodic(func(deps Deps) []*river.PeriodicJob {
			periodicJobs = deps.Jobs
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer app.Close()

	if workerJobs == nil || periodicJobs == nil {
		t.Fatalf("hooks got Jobs %v and %v, want both set", workerJobs, periodicJobs)
	}
	for name, jobs := range map[string]JobClient{"worker hook": workerJobs, "periodic hook": periodicJobs} {
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := jobs.InsertTx(ctx, tx, hookJobArgs{}, nil); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("%s: InsertTx: %v", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-worker.done:
		case <-time.After(10 * time.Second):
			t.Fatal("job inserted through a hook's Jobs never ran")
		}
	}
}
