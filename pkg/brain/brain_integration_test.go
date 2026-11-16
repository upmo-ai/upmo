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
	tok, err := svcsig.Sign(derived, svcsig.AudienceProvision, http.MethodGet, "/api/v1/provision/status", time.Now())
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
	if !payload.Ready || payload.SchemaVersions["identity"] != 5 {
		t.Fatalf("unexpected status payload: %+v", payload)
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
