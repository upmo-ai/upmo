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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestBrainNewCreatesConfiguredVolumeDirs(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cfg := Config{
		Env:          "development",
		PublicURL:    "http://localhost:5173",
		WebAuthnRPID: "localhost",
		ArtifactsDir: filepath.Join(root, "artifacts"),
		KnowledgeDir: filepath.Join(root, "knowledge"),
	}
	withWorker(t, owner, &cfg)
	for start := 0; start < 2; start++ {
		app, err := New(ctx, cfg,
			servingPools(t, owner),
			WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		)
		if err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
		app.Close()
		for _, dir := range []string{cfg.ArtifactsDir, cfg.KnowledgeDir} {
			info, err := os.Stat(dir)
			wantMode := os.FileMode(0o700)
			if start == 1 {
				wantMode = 0o750
			}
			if err != nil || !info.IsDir() || info.Mode().Perm() != wantMode {
				t.Fatalf("start %d directory %s = %v, %v; want mode %o", start, dir, info, err, wantMode)
			}
			if start == 0 {
				if err := os.Chmod(dir, 0o750); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestBrainNewVolumeDirFailureBeforePools(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, volume := range []string{"artifacts", "knowledge"} {
		cfg := Config{Env: "development", PublicURL: "http://localhost:5173"}
		if volume == "artifacts" {
			cfg.ArtifactsDir = filepath.Join(file, "child")
		} else {
			cfg.KnowledgeDir = filepath.Join(file, "child")
		}
		app, err := New(context.Background(), cfg)
		if app != nil {
			app.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "brain: create "+volume+" directory:") {
			t.Fatalf("%s: expected directory error before opening pools, got %v", volume, err)
		}
	}
}

func TestBrainNewAndExtensionOptions(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}

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
	withWorker(t, owner, &cfg)

	extraWorkersCalled := false
	var capturedDeps Deps

	app, err := New(ctx, cfg,
		servingPools(t, owner),
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
	if capturedDeps.App == nil || capturedDeps.Members == nil || capturedDeps.Identity == nil || capturedDeps.Logger == nil {
		t.Fatalf("Deps missing required fields: %+v", capturedDeps)
	}

	// Custom route registered via WithExtraRoutes works.
	eeRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(eeRec, httptest.NewRequest(http.MethodGet, "/api/ee/ping", nil))
	if eeRec.Code != http.StatusOK || eeRec.Body.String() == "" {
		t.Fatalf("GET /api/ee/ping = %d %q", eeRec.Code, eeRec.Body.String())
	}

	// Provisioning is the worker's route now: brain answers 404 even with
	// provision keys configured.
	statusRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/api/v1/provision/status", nil))
	if statusRec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/provision/status on brain = %d, want 404", statusRec.Code)
	}

	// The schema heads come through the definer the app role may call.
	versionRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(versionRec, httptest.NewRequest(http.MethodGet, "/version", nil))
	var version struct {
		Schemas map[string]int64 `json:"schema_versions"`
	}
	if err := json.Unmarshal(versionRec.Body.Bytes(), &version); err != nil || versionRec.Code != http.StatusOK || version.Schemas["identity"] != 1 || version.Schemas["platform"] != 1 {
		t.Fatalf("GET /version = %d %s %v", versionRec.Code, versionRec.Body.String(), err)
	}
}

// New built directly from a Config runs the same guard as the loaders, so the
// compose development values refuse brain in production before any pool or
// database is touched. An empty Env is production, like an unset one.
func TestBrainNewRefusesDevelopmentValuesInProduction(t *testing.T) {
	cfg := Config{
		DatabaseAppURL:  "postgres://upmo_app:upmo_app@postgres:5432/upmo?sslmode=disable",
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		GatewayKey:      "upmo_svc_gateway_" + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'g'}, 32)),
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		ShutdownTimeout: 2 * time.Second,
	}
	_, err := New(context.Background(), cfg)
	if err == nil || err.Error() != "config: UPMO_GATEWAY_KEY uses a development or weak key; set a unique key or UPMO_ENV=development" {
		t.Fatalf("New err = %v", err)
	}
	if strings.Contains(err.Error(), cfg.DatabaseAppURL) || strings.Contains(err.Error(), cfg.GatewayKey) {
		t.Fatalf("New err holds a value: %v", err)
	}
}

// Invariant 9: brain never holds a master key. New, which brain.Run and every
// embedder call, refuses UPMO_MASTER_KEY or a previous key alone before any
// pool opens, in development too, and starts with neither set.
func TestBrainRefusesTheMasterKey(t *testing.T) {
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
	withWorker(t, owner, &cfg)
	quiet := WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, name := range []string{"UPMO_MASTER_KEY", "UPMO_MASTER_KEY_PREVIOUS_k0"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'q'}, 32)))
			app, err := New(ctx, cfg, servingPools(t, owner), quiet)
			if app != nil {
				app.Close()
			}
			if err == nil || err.Error() != "config: UPMO_MASTER_KEY is refused" {
				t.Fatalf("New with %s = %v, want config: UPMO_MASTER_KEY is refused", name, err)
			}
		})
	}
	app, err := New(ctx, cfg, servingPools(t, owner), quiet)
	if err != nil {
		t.Fatalf("New with no master key: %v", err)
	}
	app.Close()
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

	withWorker(t, owner, &cfg)
	var workerJobs, periodicJobs JobClient
	worker := &hookJobWorker{done: make(chan struct{}, 2)}
	app, err := New(ctx, cfg,
		servingPools(t, owner),
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

func TestFeature_UPM365_UsesIncludesTeamSharedAgentsThroughBrain(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)


	cfg := Config{
		Env:             "development",
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		ShutdownTimeout: 2 * time.Second,
	}
	withWorker(t, owner, &cfg)
	app, err := New(ctx, cfg,
		servingPools(t, owner),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer app.Close()

	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('owner@example.com', sha256('owner@example.com'::bytea), 'k1', 'Owner', (SELECT id FROM identity.roles WHERE key = 'owner'), now())`); err != nil {
		t.Fatal(err)
	}
	var memberID, otherID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('bob@example.com', sha256('bob@example.com'::bytea), 'k1', 'Bob', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	var teamT, otherTeam string
	if err := owner.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Team T') RETURNING id::text`).Scan(&teamT); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Other team') RETURNING id::text`).Scan(&otherTeam); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.team_members (team_id, user_id)
		VALUES ($1::uuid, $2::uuid), ($3::uuid, $4::uuid)`,
		teamT, memberID, otherTeam, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO agents.agents (owner_user_id, visibility, team_ids, name, slug, default_model_provider, default_model_id)
		VALUES
		  ($1::uuid, 'team', ARRAY[$2::uuid], 'Team T agent', 'team-t-agent', 'openai', 'm1'),
		  ($1::uuid, 'team', ARRAY[$3::uuid], 'Other team agent', 'other-team-agent', 'openai', 'm1'),
		  ($1::uuid, 'private', '{}'::uuid[], 'Other private agent', 'other-private-agent', 'openai', 'm1')`,
		otherID, teamT, otherTeam); err != nil {
		t.Fatal(err)
	}

	uid, err := identity.ParseUserID(memberID)
	if err != nil {
		t.Fatal(err)
	}
	sessions := identity.Sessions(appPool, cfg.SessionIdle, cfg.SessionMaxAge)
	token, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "__Host-upmo_session", Value: token, Path: "/", Secure: true, HttpOnly: true}

	addReq := httptest.NewRequest(http.MethodPost, "/api/providers", strings.NewReader(`{
		"provider":"openai","name":"Member key","adapter":"openai_compat",
		"plan_kind":"metered","base_url":"https://api.example.com",
		"api_key":"sk-ada-aaaa-aaaa-aaaa-aaaa","models":["m1"]
	}`))
	addReq.AddCookie(cookie)
	addRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(addRec, addReq)
	if addRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/providers = %d %s", addRec.Code, addRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(addRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	usesReq := httptest.NewRequest(http.MethodGet, "/api/providers/"+created.ID+"/uses", nil)
	usesReq.AddCookie(cookie)
	usesRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(usesRec, usesReq)
	if usesRec.Code != http.StatusOK {
		t.Fatalf("GET /api/providers/%s/uses = %d %s", created.ID, usesRec.Code, usesRec.Body.String())
	}
	var uses struct {
		Routes      []string `json:"routes"`
		Agents      []string `json:"agents"`
		Automations []string `json:"automations"`
	}
	if err := json.Unmarshal(usesRec.Body.Bytes(), &uses); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(uses.Agents, []string{"Team T agent"}) {
		t.Fatalf("uses.agents = %v, want [Team T agent]", uses.Agents)
	}
	if len(uses.Routes) != 0 || len(uses.Automations) != 0 {
		t.Fatalf("uses = %+v, want empty routes and automations", uses)
	}

	if _, err := owner.Exec(ctx, `UPDATE identity.workspace SET allow_member_providers = false WHERE id`); err != nil {
		t.Fatal(err)
	}
	listReq := httptest.NewRequest(http.MethodGet, "/api/providers", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/providers = %d %s", listRec.Code, listRec.Body.String())
	}
	var listed []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range listed {
		if row.ID == created.ID {
			found = true
			if row.Status != domain.StatusWorkspaceOff {
				t.Fatalf("provider %s status = %q, want %q", created.ID, row.Status, domain.StatusWorkspaceOff)
			}
		}
	}
	if !found {
		t.Fatalf("provider %s not found in GET /api/providers: %+v", created.ID, listed)
	}

	blockedReq := httptest.NewRequest(http.MethodPost, "/api/providers", strings.NewReader(`{
		"provider":"openai","name":"Blocked key","adapter":"openai_compat",
		"plan_kind":"metered","base_url":"https://api.example.com",
		"api_key":"sk-ada-bbbb-bbbb-bbbb-bbbb","models":["m1"]
	}`))
	blockedReq.AddCookie(cookie)
	blockedRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(blockedRec, blockedReq)
	if blockedRec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/providers with policy off = %d %s, want 403", blockedRec.Code, blockedRec.Body.String())
	}
}

func TestFeature_UPM357_BrainUsageLimitsRoute(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)

	cfg := Config{
		Env:             "development",
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		ShutdownTimeout: 2 * time.Second,
	}
	withWorker(t, owner, &cfg)
	app, err := New(ctx, cfg,
		servingPools(t, owner),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer app.Close()

	var ownerID, memberID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('owner@example.com', sha256('owner@example.com'::bytea), 'k1', 'Owner', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('member@example.com', sha256('member@example.com'::bytea), 'k1', 'Member', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}

	var tplID, autoID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO tasks.templates (owner_user_id, goal, agent_id, budget)
		VALUES ($1::uuid, 'Daily report', '40000000-0000-7000-8000-000000000099'::uuid, 100)
		RETURNING id::text`, memberID).Scan(&tplID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO tasks.automations (owner_user_id, template_id, kind, spec)
		VALUES ($1::uuid, $2::uuid, 'cron', '0 8 * * *')
		RETURNING id::text`, memberID, tplID).Scan(&autoID); err != nil {
		t.Fatal(err)
	}

	sessions := identity.Sessions(appPool, cfg.SessionIdle, cfg.SessionMaxAge)
	startCookie := func(userID string) *http.Cookie {
		uid, err := identity.ParseUserID(userID)
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := sessions.Start(ctx, uid, "passkey", "")
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "__Host-upmo_session", Value: tok, Path: "/", Secure: true, HttpOnly: true}
	}
	ownerCookie := startCookie(ownerID)
	memberCookie := startCookie(memberID)

	// Owner sets an automation limit (owned by memberID) through brain.
	putReq := httptest.NewRequest(http.MethodPut, "/api/usage/limits", strings.NewReader(`{"kind":"automation","subject_id":"`+autoID+`","amount_micro_usd":4200}`))
	putReq.AddCookie(ownerCookie)
	putRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("owner PUT automation limit = %d %s", putRec.Code, putRec.Body.String())
	}

	// Owner GET sees the automation row; member GET omits rows.
	ownerGet := httptest.NewRequest(http.MethodGet, "/api/usage/limits", nil)
	ownerGet.AddCookie(ownerCookie)
	ownerGetRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(ownerGetRec, ownerGet)
	if ownerGetRec.Code != http.StatusOK {
		t.Fatalf("owner GET = %d %s", ownerGetRec.Code, ownerGetRec.Body.String())
	}
	var ownerView gateway.UsageLimitsView
	if err := json.Unmarshal(ownerGetRec.Body.Bytes(), &ownerView); err != nil {
		t.Fatal(err)
	}
	if ownerView.Rows == nil || len(*ownerView.Rows) != 1 || (*ownerView.Rows)[0].SubjectID != autoID {
		t.Fatalf("owner view rows = %+v, want automation %s", ownerView.Rows, autoID)
	}

	memberGet := httptest.NewRequest(http.MethodGet, "/api/usage/limits", nil)
	memberGet.AddCookie(memberCookie)
	memberGetRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(memberGetRec, memberGet)
	if memberGetRec.Code != http.StatusOK {
		t.Fatalf("member GET = %d %s", memberGetRec.Code, memberGetRec.Body.String())
	}
	var memberView gateway.UsageLimitsView
	if err := json.Unmarshal(memberGetRec.Body.Bytes(), &memberView); err != nil {
		t.Fatal(err)
	}
	if memberView.Rows != nil {
		t.Fatalf("member view rows = %+v, want nil", *memberView.Rows)
	}
}

// Brain saves provider keys through the worker, so it checks the worker,
// signed, at start: a worker that is gone stops brain before it serves.
func TestBrainStartNeedsAReadyWorker(t *testing.T) {
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
	worker := withWorker(t, owner, &cfg)
	worker.Stop()
	app, err := New(ctx, cfg, servingPools(t, owner), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err == nil {
		app.Close()
	}
	if err == nil || err.Error() != "config: worker is not ready" {
		t.Fatalf("brain.New with the worker down = %v", err)
	}
}
