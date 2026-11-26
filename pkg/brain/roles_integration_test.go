//go:build integration

package brain

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func ownerLoginURL(t *testing.T, owner *pgxpool.Pool) string {
	t.Helper()
	u, err := url.Parse(owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + owner.Config().ConnConfig.Database
	return u.String()
}

func roleTestConfig() Config {
	return Config{
		Env:             "development",
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		ShutdownTimeout: 2 * time.Second,
	}
}

// Brain refuses a superuser on either of its pools, whether it opens the pool
// from a URL or is handed one: a superuser ignores every row policy.
func TestBrainRefusesPrivilegedPools(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	quiet := WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		name, app, runner, want string
	}{
		{"superuser app URL", ownerLoginURL(t, owner), pgtest.LoginURL(t, owner, "upmo_runner"), "UPMO_DATABASE_APP_URL"},
		// The plant: the runner pool built from the owner URL.
		{"superuser runner URL", pgtest.AppLoginURL(t, owner), ownerLoginURL(t, owner), "UPMO_DATABASE_RUNNER_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := roleTestConfig()
			cfg.DatabaseAppURL, cfg.DatabaseRunnerURL = tc.app, tc.runner
			app, err := New(ctx, cfg, quiet)
			if app != nil {
				app.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "BYPASSRLS") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New = %v, want a refusal naming BYPASSRLS and %s", err, tc.want)
			}
		})
	}
	app, err := New(ctx, roleTestConfig(), quiet, WithPools(pgtest.AppLogin(t, owner), owner))
	if app != nil {
		app.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Fatalf("New with the owner as runner pool = %v, want a refusal naming BYPASSRLS", err)
	}
	app, err = New(ctx, roleTestConfig(), quiet, WithPools(pgtest.Login(t, owner, "upmo_runner"), pgtest.Login(t, owner, "upmo_runner")))
	if app != nil {
		app.Close()
	}
	if err == nil || err.Error() != "brain: UPMO_DATABASE_APP_URL must log in as upmo_app, not upmo_runner" {
		t.Fatalf("New with the runner as app pool = %v", err)
	}
}

// Brain HTTP never opens UPMO_DATABASE_URL: with it unset, brain loads its
// configuration, opens the app and runner logins and serves /readyz.
func TestBrainStartsWithoutOwnerURL(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_DATABASE_URL", "")
	if err := os.Unsetenv("UPMO_DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_DATABASE_APP_URL", pgtest.AppLoginURL(t, owner))
	t.Setenv("UPMO_DATABASE_RUNNER_URL", pgtest.LoginURL(t, owner, "upmo_runner"))
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	// Off the default volume roots, which other test packages read.
	for _, dir := range []string{"UPMO_ARTIFACTS_DIR", "UPMO_KNOWLEDGE_DIR", "UPMO_AVATARS_DIR"} {
		t.Setenv(dir, t.TempDir())
	}
	var worker Config
	w := withWorker(t, owner, &worker)
	t.Setenv("UPMO_WORKER_URL", w.URL)
	t.Setenv("UPMO_WORKER_BRAIN_KEY", worker.WorkerBrainKey)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig without UPMO_DATABASE_URL: %v", err)
	}
	app, err := New(ctx, cfg, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("New without UPMO_DATABASE_URL: %v", err)
	}
	defer app.Close()
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("GET /readyz = %d %q", rec.Code, rec.Body.String())
	}
	var who string
	if err := app.Deps().App.QueryRow(ctx, `SELECT current_user`).Scan(&who); err != nil || who != "upmo_app" {
		t.Fatalf("app pool current_user = %q %v", who, err)
	}
}
