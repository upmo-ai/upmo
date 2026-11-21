//go:build integration

package brain

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestReadyAndSession(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for schema, files := range map[string]fs.FS{audit.Schema: audit.Migrations(), identity.Schema: identity.Migrations()} {
		if err := migrate.Up(ctx, owner, schema, files); err != nil {
			t.Fatal(err)
		}
	}
	pool := pgtest.App(t, owner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	work, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// NewGuarded, like main.go: the brain handler carries its own guard, with
	// the audit middleware outside Protect so a refusal is still recorded.
	protected := httpserver.NewGuarded("127.0.0.1:0", handler).Handler

	ready := httptest.NewRecorder()
	protected.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || ready.Body.String() != "ok" {
		t.Fatalf("readyz = %d %q", ready.Code, ready.Body.String())
	}

	session := httptest.NewRecorder()
	protected.ServeHTTP(session, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if session.Code != http.StatusUnauthorized {
		t.Fatalf("session = %d, want 401", session.Code)
	}

	page := httptest.NewRecorder()
	protected.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/api/personalization", nil))
	if page.Code != http.StatusUnauthorized {
		t.Fatalf("personalization = %d, want 401", page.Code)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	cross := httptest.NewRecorder()
	protected.ServeHTTP(cross, req)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-site delete = %d, want 403", cross.Code)
	}

	// The refusal on a table route is still recorded, anonymously, with the
	// request id the response carried.
	out := &bytes.Buffer{}
	if _, err := audit.SealOnce(ctx, owner, out); err != nil {
		t.Fatal(err)
	}
	var action, outcome, requestID string
	if err := owner.QueryRow(ctx,
		`SELECT action, outcome, COALESCE(request_id::text, '') FROM audit.events WHERE position = 1`,
	).Scan(&action, &outcome, &requestID); err != nil {
		t.Fatal(err)
	}
	if action != "auth.sign_out" || outcome != "denied" {
		t.Fatalf("sealed %s %s, want a denied sign-out", action, outcome)
	}
	if requestID != cross.Header().Get("X-Request-Id") || requestID == "" {
		t.Fatalf("request id %q, header %q", requestID, cross.Header().Get("X-Request-Id"))
	}
}

func TestRequireSessionSeamIntegration(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	// Session writes record their audit events in the same transaction, so the
	// audit schema migrates with identity here.
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	var userID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	uid, err := identity.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passwords := identity.Passwords(pool, sealer.NewEmail(t), sessions)
	passkeys, err := identity.Passkeys(pool, sealer.NewEmail(t), sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		t.Fatal(err)
	}
	id := identity.New(sessions, passkeys, passwords)

	token, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}

	var seenPrincipal identity.Principal
	targetHandler := id.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := identity.SessionFrom(r.Context())
		if !ok {
			http.Error(w, "missing principal", http.StatusInternalServerError)
			return
		}
		seenPrincipal = p
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.AddCookie(&http.Cookie{
		Name:     "__Host-upmo_session",
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
	})
	targetHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if seenPrincipal.UserID != uid {
		t.Fatalf("principal user = %v, want %v", seenPrincipal.UserID, uid)
	}
}

func TestProvidersThroughTheBrainRoutes(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for schema, files := range map[string]fs.FS{
		audit.Schema:    audit.Migrations(),
		identity.Schema: identity.Migrations(),
		gateway.Schema:  gateway.Migrations(),
	} {
		if err := migrate.Up(ctx, owner, schema, files); err != nil {
			t.Fatal(err)
		}
	}
	pool := pgtest.App(t, owner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	signIn := func(email, role string) *http.Cookie {
		t.Helper()
		var userID string
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1, sha256(convert_to($1, 'UTF8')), 'k1', 'Person', (SELECT id FROM identity.roles WHERE key = $2), now())
			RETURNING id::text`, email, role).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		uid, err := identity.ParseUserID(userID)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := sessions.Start(ctx, uid, "passkey", "")
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "__Host-upmo_session", Value: token, Path: "/", Secure: true, HttpOnly: true}
	}
	serve := func(masterKey string) http.Handler {
		t.Helper()
		t.Setenv("UPMO_MASTER_KEY", masterKey)
		boxes, err := taskSandboxes(cfg, pool, log)
		if err != nil {
			t.Fatal(err)
		}
		work, err := jobs.Open(pool)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return httpserver.NewGuarded("127.0.0.1:0", handler).Handler
	}
	do := func(h http.Handler, who *http.Cookie, method, path, body string, header ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if who != nil {
			req.AddCookie(who)
		}
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const add = `{"provider":"custom","name":"Mine","adapter":"openai_compat","plan_kind":"local","base_url":"http://127.0.0.1:11434","models":["m"]}`
	ada, bob := signIn("ada@example.com", "owner"), signIn("bob@example.com", "member")

	with := serve(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32)))
	if rec := do(with, nil, http.MethodGet, "/api/providers", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d, want 401", rec.Code)
	}
	if rec := do(with, ada, http.MethodGet, "/api/providers", "", "Authorization", "Bearer upmo_pat_x"); rec.Code != http.StatusForbidden {
		t.Fatalf("bearer token = %d, want 403", rec.Code)
	}
	if rec := do(with, ada, http.MethodPost, "/api/providers", add); rec.Code != http.StatusCreated || strings.Contains(rec.Body.String(), "credential") {
		t.Fatalf("add = %d %s", rec.Code, rec.Body.String())
	}
	list := do(with, ada, http.MethodGet, "/api/providers", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"visibility":"private"`) || !strings.Contains(list.Body.String(), `"name":"Mine"`) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if rec := do(with, bob, http.MethodGet, "/api/providers", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("another person's list = %d %s", rec.Code, rec.Body.String())
	}
	cross := do(with, ada, http.MethodPost, "/api/providers", add, "Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example")
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-site add = %d, want 403", cross.Code)
	}

	// No master key: the brain still serves, lists, and refuses to save.
	without := serve("")
	if rec := do(without, ada, http.MethodGet, "/api/providers", ""); rec.Code != http.StatusOK {
		t.Fatalf("list without a master key = %d", rec.Code)
	}
	if rec := do(without, ada, http.MethodPost, "/api/providers", add); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("add without a master key = %d, want 503", rec.Code)
	}
	t.Setenv("UPMO_MASTER_KEY", "not base64 at all")
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	work, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil); err == nil {
		t.Fatal("brain started with a malformed master key")
	}
}

func TestRequestPoolReappliesRoleAfterReset(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	pool, err := brainops.RequestPool(ctx, owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	n := int(pool.Config().MaxConns)
	held := make([]*pgxpool.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(ctx, "RESET ROLE"); err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	for _, c := range held {
		c.Release()
	}
	var who string
	if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil {
		t.Fatal(err)
	}
	if who != "upmo_app" {
		t.Fatalf("after checkout current_user = %s", who)
	}
}

func TestBrainVersionAndReadyzRoutes(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for schema, files := range map[string]fs.FS{
		audit.Schema:    audit.Migrations(),
		identity.Schema: identity.Migrations(),
	} {
		if err := migrate.Up(ctx, owner, schema, files); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := migrate.SchemaVersions(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	if _, err := pool.Exec(ctx, "SELECT * FROM identity.goose_db_version"); err == nil {
		t.Fatal("upmo_app read identity.goose_db_version directly")
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	work, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, func(context.Context) (map[string]int64, error) {
		return maps.Clone(versions), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	protected := httpserver.NewGuarded("127.0.0.1:0", handler).Handler
	info := buildinfo.Info()

	for _, path := range []string{"/version", "/api/version"} {
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, rec.Code)
		}
		var got buildinfo.Response
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if got.Version != info.Version || got.Commit != info.Commit || got.BuildTime != info.BuildTime {
			t.Fatalf("%s info = %+v, want %+v", path, got, info)
		}
		if got.SchemaVersions[audit.Schema] == 0 || got.SchemaVersions[identity.Schema] == 0 {
			t.Fatalf("%s schema_versions = %v, want audit and identity", path, got.SchemaVersions)
		}
		if _, hasGateway := got.SchemaVersions[gateway.Schema]; hasGateway {
			t.Fatalf("%s unexpectedly reported unmigrated gateway schema: %v", path, got.SchemaVersions)
		}
	}

	ready := httptest.NewRecorder()
	protected.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || ready.Body.String() != "ok" {
		t.Fatalf("readyz = %d %q", ready.Code, ready.Body.String())
	}
	if ready.Header().Get("X-Upmo-Version") != info.Version || ready.Header().Get("X-Upmo-Commit") != info.Commit {
		t.Fatalf("readyz headers = %q %q, want %q %q", ready.Header().Get("X-Upmo-Version"), ready.Header().Get("X-Upmo-Commit"), info.Version, info.Commit)
	}
}

func TestBrainMetricsInternalOnlyAndInstrumented(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for schema, files := range map[string]fs.FS{
		audit.Schema:    audit.Migrations(),
		identity.Schema: identity.Migrations(),
	} {
		if err := migrate.Up(ctx, owner, schema, files); err != nil {
			t.Fatal(err)
		}
	}
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	reg := metrics.NewRegistry()
	work, err := jobs.Open(pool, jobs.WithMetrics(reg))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(httpserver.NewGuarded("127.0.0.1:0", handler).Handler)
	t.Cleanup(public.Close)
	internal := httptest.NewServer(metrics.Server("127.0.0.1:0", reg).Handler)
	t.Cleanup(internal.Close)

	// Public mux must never expose /metrics.
	pubMetrics, err := http.Get(public.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	_ = pubMetrics.Body.Close()
	if pubMetrics.StatusCode != http.StatusNotFound {
		t.Fatalf("public GET /metrics = %d, want 404", pubMetrics.StatusCode)
	}

	ready, err := http.Get(public.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	_ = ready.Body.Close()
	if ready.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", ready.StatusCode)
	}

	res, err := http.Get(internal.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("internal GET /metrics = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != metrics.ContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, metrics.ContentType)
	}
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	for _, want := range []string{
		`upmo_http_requests_total{service="brain",route="GET /readyz",status="200"} 1`,
		`upmo_http_request_duration_seconds_count{service="brain",route="GET /readyz",status="200"} 1`,
		`upmo_http_requests_total{service="brain",route="unmatched",status="404"} 1`,
		`upmo_river_queue_depth{queue="default"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in internal /metrics:\n%s", want, body)
		}
	}
}

// agentsRig is the brain mounted on a fresh database with one signed-in owner.
type agentsRig struct {
	owner  *pgxpool.Pool
	pool   *pgxpool.Pool
	served http.Handler
	cookie *http.Cookie
	uid    identity.UserID
}

func newAgentsRig(t *testing.T) agentsRig {
	owner := pgtest.New(t)
	ctx := context.Background()
	for _, step := range []struct {
		schema string
		files  fs.FS
	}{
		{audit.Schema, audit.Migrations()},
		{identity.Schema, identity.Migrations()},
		{gateway.Schema, gateway.Migrations()},
		{agents.Schema, agents.Migrations()},
	} {
		if err := migrate.Up(ctx, owner, step.schema, step.files); err != nil {
			t.Fatal(err)
		}
	}
	pool := pgtest.App(t, owner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Config{
		SessionIdle:   identity.IdleTimeout,
		SessionMaxAge: identity.MaxAge,
		PublicURL:     "http://localhost:5173",
		WebAuthnRPID:  "localhost",
	}
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	var userID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	uid, err := identity.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "__Host-upmo_session", Value: token, Path: "/", Secure: true, HttpOnly: true}
	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		t.Fatal(err)
	}
	work, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(pool, cfg, log, boxes, work, notices.Store{Pool: pool}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	served := httpserver.NewGuarded("127.0.0.1:0", handler).Handler
	return agentsRig{owner: owner, pool: pool, served: served, cookie: cookie, uid: uid}
}

func TestAgentsThroughTheBrainRoutes(t *testing.T) {
	rig := newAgentsRig(t)
	owner, pool, served, cookie, uid := rig.owner, rig.pool, rig.served, rig.cookie, rig.uid
	ctx := context.Background()
	createReq := httptest.NewRequest(http.MethodPost, "/api/agents",
		strings.NewReader(`{"name":"AP Clerk","instructions":"Check VAT.","plan_approval":"always"}`))
	createReq.AddCookie(cookie)
	createRec := httptest.NewRecorder()
	served.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated || !strings.Contains(createRec.Body.String(), `"slug":"ap-clerk"`) || !strings.Contains(createRec.Body.String(), `"version":1`) {
		t.Fatalf("create = %d %s", createRec.Code, createRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	served.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"AP Clerk"`) {
		t.Fatalf("list = %d %s", listRec.Code, listRec.Body.String())
	}

	// The brain lists routes on its app-role pool. An agent pinned to an
	// installed route through a connected house account is usable, so the
	// picker offers it.
	const accountID = "e2e00000-0000-7000-8000-00000000f001"
	const routeID = "e2e00000-0000-7000-8000-00000000f101"
	for _, stmt := range []string{
		`INSERT INTO gateway.provider_accounts (id, name, adapter, credential_ref, plan_kind, house, visibility, provider, status)
			VALUES ('` + accountID + `', 'house-fake', 'fake', 'house', 'local', true, 'everyone', 'e2e', 'Connected')`,
		`INSERT INTO gateway.provider_models (account_id, model, position) VALUES ('` + accountID + `', 'fake-ok', 1)`,
		`INSERT INTO gateway.routes (id, name, capability, decision, installed) VALUES ('` + routeID + `', 'house-main', 'chat', '', true)`,
		`INSERT INTO gateway.route_links (route_id, position, provider_account_id, model) VALUES ('` + routeID + `', 1, '` + accountID + `', 'fake-ok')`,
	} {
		if _, err := owner.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	routedReq := httptest.NewRequest(http.MethodPost, "/api/agents",
		strings.NewReader(`{"name":"Routed Clerk","instructions":"Check VAT.","plan_approval":"always","default_route":"house-main"}`))
	routedReq.AddCookie(cookie)
	routedRec := httptest.NewRecorder()
	served.ServeHTTP(routedRec, routedReq)
	if routedRec.Code != http.StatusCreated {
		t.Fatalf("create routed = %d %s", routedRec.Code, routedRec.Body.String())
	}
	usableReq := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	usableReq.AddCookie(cookie)
	usableRec := httptest.NewRecorder()
	served.ServeHTTP(usableRec, usableReq)
	var listed []struct {
		Name   string `json:"name"`
		Usable bool   `json:"usable"`
	}
	if err := json.Unmarshal(usableRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list = %d %s", usableRec.Code, usableRec.Body.String())
	}
	usable := map[string]bool{}
	for _, a := range listed {
		usable[a.Name] = a.Usable
	}
	if !usable["Routed Clerk"] {
		t.Fatalf("agent on a route with a connected account is not usable: %s", usableRec.Body.String())
	}

	var routed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(routedRec.Body.Bytes(), &routed); err != nil {
		t.Fatal(err)
	}
	access, ok, err := chatAccess(ctx, identity.Members(pool, nil, "http://example.test", nil, nil), uid)
	if err != nil || !ok {
		t.Fatalf("chat access = %v %v", ok, err)
	}
	members := identity.Members(pool, nil, "http://example.test", nil, nil)
	lookup := chatAgentLookup{store: agents.NewStore(pool), access: func(ctx context.Context, userID string) (agents.Access, error) {
		person, err := identity.ParseUserID(userID)
		if err != nil {
			return agents.Access{}, err
		}
		who, _, err := agentAccess(ctx, members, person)
		return who, err
	}}
	if _, err := lookup.Agent(ctx, access, routed.ID); err != nil {
		t.Fatalf("the reply worker cannot look up the agent the picker offers: %v", err)
	}
}

func (r agentsRig) call(method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.AddCookie(r.cookie)
	rec := httptest.NewRecorder()
	r.served.ServeHTTP(rec, req)
	return rec
}

type agentRow struct {
	Instructions, Route, Provider, Model string
}

func (r agentsRig) row(t *testing.T, id string) agentRow {
	t.Helper()
	var row agentRow
	if err := r.owner.QueryRow(context.Background(), `
		SELECT instructions, coalesce(default_route, ''), coalesce(default_model_provider, ''), coalesce(default_model_id, '')
		FROM agents.agents WHERE id = $1::uuid`, id).Scan(&row.Instructions, &row.Route, &row.Provider, &row.Model); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestAgentsAcceptLegacyAndNewFieldNamesAndDeleteThroughTheBrainRoutes(t *testing.T) {
	rig := newAgentsRig(t)

	rec := rig.call(http.MethodPost, "/api/agents",
		`{"name":"Research","description":"d","prompt_overlay":"p","route":"fast","model_provider":"OpenAI","model_id":"gpt-x","tool_classes":["web"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create legacy = %d %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["prompt_overlay"] != "p" || raw["instructions"] != "p" || raw["route"] != "fast" || raw["default_route"] != "fast" ||
		raw["model_provider"] != "OpenAI" || raw["default_model_provider"] != "OpenAI" ||
		raw["model_id"] != "gpt-x" || raw["default_model_id"] != "gpt-x" || raw["slug"] != "research" || raw["version"] != float64(1) {
		t.Fatalf("create legacy response = %v", raw)
	}
	if classes, _ := raw["tool_classes"].([]any); len(classes) != 1 || classes[0] != "web" {
		t.Fatalf("create legacy tool_classes = %v", raw["tool_classes"])
	}
	id, _ := raw["id"].(string)
	var storedClasses []string
	if err := rig.owner.QueryRow(context.Background(), `SELECT tool_classes FROM agents.agents WHERE id = $1::uuid`, id).Scan(&storedClasses); err != nil ||
		len(storedClasses) != 1 || storedClasses[0] != "web" {
		t.Fatalf("legacy create stored tool_classes %v %v", storedClasses, err)
	}
	if got := rig.row(t, id); got != (agentRow{"p", "fast", "OpenAI", "gpt-x"}) {
		t.Fatalf("legacy create stored %+v", got)
	}

	// The new names store the same way.
	rec = rig.call(http.MethodPost, "/api/agents",
		`{"name":"Bookkeeper","slug":"ap-clerk","instructions":"Check VAT.","default_route":"work","default_model_provider":"Anthropic","default_model_id":"m","plan_approval":"always"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create new = %d %s", rec.Code, rec.Body.String())
	}
	var second map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	secondID, _ := second["id"].(string)
	if second["slug"] != "ap-clerk" || second["plan_approval"] != "always" {
		t.Fatalf("create new response = %v", second)
	}
	if got := rig.row(t, secondID); got != (agentRow{"Check VAT.", "work", "Anthropic", "m"}) {
		t.Fatalf("new create stored %+v", got)
	}

	// PATCH takes the legacy names and the agent reads back with them.
	rec = rig.call(http.MethodPatch, "/api/agents/"+id, `{"route":"deep","prompt_overlay":"New overlay","model_provider":"Anthropic","model_id":"claude-x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch legacy = %d %s", rec.Code, rec.Body.String())
	}
	rec = rig.call(http.MethodGet, "/api/agents/"+id, "")
	var back map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back["default_route"] != "deep" || back["instructions"] != "New overlay" || back["prompt_overlay"] != "New overlay" ||
		back["default_model_provider"] != "Anthropic" || back["default_model_id"] != "claude-x" || back["version"] != float64(2) {
		t.Fatalf("read back after legacy patch = %v", back)
	}
	if classes, _ := back["tool_classes"].([]any); len(classes) != 1 || classes[0] != "web" {
		t.Fatalf("read back tool_classes = %v", back["tool_classes"])
	}
	if got := rig.row(t, id); got != (agentRow{"New overlay", "deep", "Anthropic", "claude-x"}) {
		t.Fatalf("legacy patch stored %+v", got)
	}

	// PATCH takes the new names too.
	if rec = rig.call(http.MethodPatch, "/api/agents/"+id, `{"default_route":"fast","instructions":"Newer"}`); rec.Code != http.StatusOK {
		t.Fatalf("patch new = %d %s", rec.Code, rec.Body.String())
	}
	if got := rig.row(t, id); got != (agentRow{"Newer", "fast", "Anthropic", "claude-x"}) {
		t.Fatalf("new patch stored %+v", got)
	}

	if rec = rig.call(http.MethodDelete, "/api/agents/"+id, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if rec = rig.call(http.MethodGet, "/api/agents/"+id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted agent read = %d", rec.Code)
	}
	var left int
	if err := rig.owner.QueryRow(context.Background(), `SELECT count(*) FROM agents.agents WHERE id = $1::uuid`, id).Scan(&left); err != nil || left != 0 {
		t.Fatalf("deleted agent rows = %d %v", left, err)
	}
}

func TestAgentsListCarriesUsableAndEmptySlicesThroughTheBrainRoutes(t *testing.T) {
	rig := newAgentsRig(t)
	const accountID = "e2e00000-0000-7000-8000-00000000f001"
	for _, stmt := range []string{
		`INSERT INTO gateway.provider_accounts (id, name, adapter, credential_ref, plan_kind, house, visibility, provider, status)
			VALUES ('` + accountID + `', 'house-fake', 'fake', 'house', 'local', true, 'everyone', 'e2e', 'Connected')`,
		`INSERT INTO gateway.provider_models (account_id, model, position) VALUES ('` + accountID + `', 'fake-ok', 1)`,
		`INSERT INTO identity.teams (id, name) VALUES ('e2e00000-0000-7000-8000-00000000f201', 'Elsewhere')`,
		`INSERT INTO gateway.routes (id, name, capability, decision, installed) VALUES
			('e2e00000-0000-7000-8000-00000000f101', 'house-main', 'chat', '', true)`,
		`INSERT INTO gateway.routes (id, name, capability, decision, installed, owner_user_id, visibility, team_ids) VALUES
			('e2e00000-0000-7000-8000-00000000f102', 'managed-only', 'chat', '', false,
			 'e2e00000-0000-7000-8000-00000000f301', 'team', '{e2e00000-0000-7000-8000-00000000f201}')`,
		`INSERT INTO gateway.route_links (route_id, position, provider_account_id, model) VALUES
			('e2e00000-0000-7000-8000-00000000f101', 1, '` + accountID + `', 'fake-ok'),
			('e2e00000-0000-7000-8000-00000000f102', 1, '` + accountID + `', 'fake-ok')`,
	} {
		if _, err := rig.owner.Exec(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{
		`{"name":"On route","default_route":"house-main"}`,
		`{"name":"By model","default_model_provider":"e2e","default_model_id":"fake-ok"}`,
		`{"name":"Off","default_route":"house-main","enabled":false}`,
		`{"name":"On managed only","default_route":"managed-only"}`,
	} {
		if rec := rig.call(http.MethodPost, "/api/agents", body); rec.Code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}

	// The route exists and the person manages it, but may not call it.
	rec := rig.call(http.MethodGet, "/api/gateway/routes", "")
	var listedRoutes []struct {
		Name      string `json:"name"`
		CanUse    bool   `json:"can_use"`
		CanManage bool   `json:"can_manage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listedRoutes); err != nil {
		t.Fatalf("routes = %d %s", rec.Code, rec.Body.String())
	}
	var sawManaged bool
	for _, r := range listedRoutes {
		if r.Name == "managed-only" {
			sawManaged = true
			if r.CanUse || !r.CanManage {
				t.Fatalf("managed-only route = %+v, want manage without use", r)
			}
		}
	}
	if !sawManaged {
		t.Fatalf("managed-only route not listed: %s", rec.Body.String())
	}

	rec = rig.call(http.MethodGet, "/api/agents", "")
	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	usable := map[string]bool{}
	for _, a := range listed {
		usable[a["name"].(string)], _ = a["usable"].(bool)
		for _, key := range []string{"tool_classes", "connector_ids", "knowledge_folder_ids", "check_criteria", "team_ids", "team_names"} {
			if got, ok := a[key].([]any); !ok || got == nil {
				t.Fatalf("%s of %v is %v, want an empty list", key, a["name"], a[key])
			}
		}
	}
	want := map[string]bool{"On route": true, "By model": true, "Off": false, "On managed only": false}
	for name, ok := range want {
		if got, found := usable[name]; !found || got != ok {
			t.Fatalf("usable = %v, want %v", usable, want)
		}
	}
}

func TestChatAgentLookupIntegration(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for _, step := range []struct {
		schema string
		files  fs.FS
	}{
		{audit.Schema, audit.Migrations()},
		{identity.Schema, identity.Migrations()},
		{agents.Schema, agents.Migrations()},
	} {
		if err := migrate.Up(ctx, owner, step.schema, step.files); err != nil {
			t.Fatal(err)
		}
	}
	pool := pgtest.App(t, owner)
	store := agents.NewStore(pool)
	lookup := chatAgentLookup{store: store, access: func(_ context.Context, userID string) (agents.Access, error) {
		return agents.Access{UserID: userID, Allow: "agent:create,agent:view,agent:edit"}, nil
	}}

	const (
		ownerID = "018f3b2c-7a10-7c4a-8e11-111111111111"
		otherID = "018f3b2c-7a10-7c4a-8e11-222222222222"
		allow   = "agent:create,agent:view,agent:edit"
	)
	ownerAccess := agents.Access{UserID: ownerID, Allow: allow}
	ownerChat := chat.Access{UserID: ownerID, Allow: allow}
	otherChat := chat.Access{UserID: otherID, Allow: allow}
	at := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

	active, err := agents.CreateAgent(ctx, store, nil, ownerAccess, agents.AgentFields{
		Name:                 "Bookkeeper",
		Description:          "AP specialist",
		Instructions:         "Check VAT and PO numbers.",
		DefaultRoute:         "work",
		DefaultModelProvider: "Anthropic",
		DefaultModelID:       "claude-sonnet-4-5",
		Enabled:              true,
		Visibility:           agents.VisibilityPrivate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agents.SaveLesson(ctx, store, ownerAccess, active.ID, agents.LessonFields{
		Title:        "EU Reverse Charge",
		Cue:          "EU supplier invoice",
		Body:         "Verify VAT ID before booking.",
		AlwaysInline: true,
	}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.SaveLesson(ctx, store, ownerAccess, active.ID, agents.LessonFields{
		Title:        "FX Rounding",
		Cue:          "Multi-currency invoice",
		Body:         "NON_INLINE_SECRET_BODY",
		AlwaysInline: false,
	}, at); err != nil {
		t.Fatal(err)
	}

	got, err := lookup.Agent(ctx, ownerChat, active.ID)
	if err != nil {
		t.Fatalf("lookup.Agent(active) = %v", err)
	}
	if got.ID != active.ID || got.Name != "Bookkeeper" || got.Description != "AP specialist" ||
		got.Route != "work" || got.ModelProvider != "Anthropic" || got.ModelID != "claude-sonnet-4-5" || !got.Enabled {
		t.Fatalf("got metadata = %+v", got)
	}
	if !strings.Contains(got.PromptOverlay, "Check VAT and PO numbers.") ||
		!strings.Contains(got.PromptOverlay, "## EU Reverse Charge\n\nVerify VAT ID before booking.") ||
		strings.Contains(got.PromptOverlay, "NON_INLINE_SECRET_BODY") {
		t.Fatalf("PromptOverlay = %q", got.PromptOverlay)
	}

	// Unshared private agent is hidden from other users.
	if _, err := lookup.Agent(ctx, otherChat, active.ID); !errors.Is(err, chat.ErrAgentNotFound) {
		t.Fatalf("lookup.Agent(unshared) = %v, want ErrAgentNotFound", err)
	}

	// Missing agent returns ErrAgentNotFound.
	if _, err := lookup.Agent(ctx, ownerChat, "018f3b2c-7a10-7c4a-8e11-ffffffffffff"); !errors.Is(err, chat.ErrAgentNotFound) {
		t.Fatalf("lookup.Agent(missing) = %v, want ErrAgentNotFound", err)
	}

	// Disabled agent returns ErrAgentNotFound even for the owner.
	disabled, err := agents.CreateAgent(ctx, store, nil, ownerAccess, agents.AgentFields{
		Name:         "Paused",
		Instructions: "Disabled.",
		Enabled:      false,
		Visibility:   agents.VisibilityPrivate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.Agent(ctx, ownerChat, disabled.ID); !errors.Is(err, chat.ErrAgentNotFound) {
		t.Fatalf("lookup.Agent(disabled) = %v, want ErrAgentNotFound", err)
	}
}
