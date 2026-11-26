//go:build integration

package brain

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// usesRig is the whole brain with Ada's own provider account and, around it,
// agents and automations of Ada and of Bob that depend on the account or do
// not.
type usesRig struct {
	app      *App
	cookie   *http.Cookie
	provider string
	// owner and bob are the sessions of the workspace owner, who may manage
	// members, and of Bob, a plain member.
	owner, bob *http.Cookie
}

func newUsesRig(t *testing.T) usesRig {
	t.Helper()
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
	t.Cleanup(app.Close)

	var ownerID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('owner@example.com', sha256('owner@example.com'::bytea), 'k1', 'Owner', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	var adaID, bobID string
	for _, who := range []struct {
		email, name string
		id          *string
	}{
		{"ada@example.com", "Ada", &adaID},
		{"bob@example.com", "Bob", &bobID},
	} {
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1, sha256(convert_to($1, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = 'member'), now())
			RETURNING id::text`, who.email, who.name).Scan(who.id); err != nil {
			t.Fatal(err)
		}
	}
	session := func(id string) *http.Cookie {
		uid, err := identity.ParseUserID(id)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := identity.Sessions(appPool, identity.IdleTimeout, identity.MaxAge).Start(ctx, uid, "passkey", "")
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: "__Host-upmo_session", Value: token, Path: "/", Secure: true, HttpOnly: true}
	}
	cookie := session(adaID)

	req := httptest.NewRequest(http.MethodPost, "/api/providers", strings.NewReader(`{
		"provider":"openai","name":"Ada key","adapter":"openai_compat",
		"plan_kind":"metered","base_url":"https://api.example.com",
		"api_key":"sk-ada-aaaa-aaaa-aaaa-aaaa","models":["m1"]
	}`))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/providers = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	var routeID string
	if err := owner.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('daily', 'chat', true) RETURNING id::text`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.route_links (route_id, position, provider_account_id, model) VALUES ($1::uuid, 1, $2::uuid, 'm1')`, routeID, created.ID); err != nil {
		t.Fatal(err)
	}

	// Agents: by route chain, by model, unrelated, and Bob's private agent that
	// names the same model.
	agentIDs := map[string]string{}
	for _, a := range []struct{ owner, name, route, provider, model string }{
		{adaID, "Route agent", "daily", "", ""},
		{adaID, "Model agent", "", "openai", "m1"},
		{adaID, "Unrelated agent", "other", "openai", "m2"},
		{bobID, "Bob private agent", "daily", "openai", "m1"},
	} {
		var id string
		if err := owner.QueryRow(ctx, `
			INSERT INTO agents.agents (owner_user_id, visibility, team_ids, name, slug, default_route, default_model_provider, default_model_id)
			VALUES ($1::uuid, 'private', '{}'::uuid[], $2, lower(replace($2, ' ', '-')), NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''))
			RETURNING id::text`, a.owner, a.name, a.route, a.provider, a.model).Scan(&id); err != nil {
			t.Fatal(err)
		}
		agentIDs[a.name] = id
	}
	// Automations: each one runs the agent of its template.
	for _, a := range []struct{ owner, goal, agent string }{
		{adaID, "Morning digest", "Route agent"},
		{adaID, "Weekly report", "Model agent"},
		{adaID, "Unrelated chore", "Unrelated agent"},
		{bobID, "Bob private chore", "Bob private agent"},
	} {
		var templateID string
		if err := owner.QueryRow(ctx, `
			INSERT INTO tasks.templates (owner_user_id, goal, agent_id, budget)
			VALUES ($1::uuid, $2, $3::uuid, 100)
			RETURNING id::text`, a.owner, a.goal, agentIDs[a.agent]).Scan(&templateID); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Exec(ctx, `
			INSERT INTO tasks.automations (owner_user_id, template_id, kind, spec)
			VALUES ($1::uuid, $2::uuid, 'cron', '0 8 * * *')`, a.owner, templateID); err != nil {
			t.Fatal(err)
		}
	}
	return usesRig{app: app, cookie: cookie, provider: created.ID, owner: session(ownerID), bob: session(bobID)}
}

// account creates an openai account for model m1 as the cookie's user, with
// the given visibility, and returns its id.
func (r usesRig) account(t *testing.T, cookie *http.Cookie, visibility string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/providers", strings.NewReader(`{
		"provider":"openai","name":"Second key","adapter":"openai_compat",
		"plan_kind":"metered","base_url":"https://api.example.com",
		"api_key":"sk-second-aaaa-aaaa-aaaa-aaaa","models":["m1"],"visibility":"`+visibility+`"
	}`))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/providers (%s) = %d %s", visibility, rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func (r usesRig) uses(t *testing.T) (routes, agents, automations []string) {
	t.Helper()
	return r.usesOf(t, r.cookie, r.provider)
}

func (r usesRig) usesOf(t *testing.T, cookie *http.Cookie, provider string) (routes, agents, automations []string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/"+provider+"/uses", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET uses = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Routes      []string `json:"routes"`
		Agents      []string `json:"agents"`
		Automations []string `json:"automations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.Routes, got.Agents, got.Automations
}

func TestFeature_UPM391_UsesListsTheAgentsAndAutomationsThatDependOnTheAccount(t *testing.T) {
	routes, agents, automations := newUsesRig(t).uses(t)
	if !reflect.DeepEqual(routes, []string{"daily"}) {
		t.Fatalf("routes = %v, want [daily]", routes)
	}
	if want := []string{"Model agent", "Route agent"}; !reflect.DeepEqual(agents, want) {
		t.Fatalf("agents = %v, want %v (one by model, one by route chain)", agents, want)
	}
	if want := []string{"Morning digest", "Weekly report"}; !reflect.DeepEqual(automations, want) {
		t.Fatalf("automations = %v, want %v", automations, want)
	}
}

func TestFeature_UPM391_UsesLeavesOutWhatTheCallerMayNotSee(t *testing.T) {
	_, agents, automations := newUsesRig(t).uses(t)
	if want := []string{"Model agent", "Route agent"}; !reflect.DeepEqual(agents, want) {
		t.Fatalf("agents = %v, want exactly %v", agents, want)
	}
	if want := []string{"Morning digest", "Weekly report"}; !reflect.DeepEqual(automations, want) {
		t.Fatalf("automations = %v, want exactly %v", automations, want)
	}
}

func TestFeature_UPM391_UsesListsAutomationsAManagerMaySee(t *testing.T) {
	r := newUsesRig(t)
	shared := r.account(t, r.owner, "everyone")

	// The owner holds members:manage, so every automation that runs a model
	// agent for the account is theirs to see, whoever owns it. The agents stay
	// private to their owners.
	_, agents, automations := r.usesOf(t, r.owner, shared)
	if len(agents) != 0 {
		t.Fatalf("manager agents = %v, want none (private agents of others)", agents)
	}
	if want := []string{"Bob private chore", "Weekly report"}; !reflect.DeepEqual(automations, want) {
		t.Fatalf("manager automations = %v, want exactly %v", automations, want)
	}

	// Bob is a plain member with no right to Ada's automations. His own
	// account lists only what he owns.
	bobs := r.account(t, r.bob, "private")
	_, agents, automations = r.usesOf(t, r.bob, bobs)
	if want := []string{"Bob private agent"}; !reflect.DeepEqual(agents, want) {
		t.Fatalf("member agents = %v, want exactly %v", agents, want)
	}
	if want := []string{"Bob private chore"}; !reflect.DeepEqual(automations, want) {
		t.Fatalf("member automations = %v, want exactly %v", automations, want)
	}
}
