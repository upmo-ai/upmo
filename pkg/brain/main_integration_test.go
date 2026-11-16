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
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('ada@example.com', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
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
	passwords := identity.Passwords(pool, sessions)
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
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
			INSERT INTO identity.users (email, name, role_id, joined_at)
			VALUES ($1, 'Person', (SELECT id FROM identity.roles WHERE key = $2), now())
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

func TestAgentsThroughTheBrainRoutes(t *testing.T) {
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
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('ada@example.com', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
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
	lookup := chatAgentLookup{store: store}

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
