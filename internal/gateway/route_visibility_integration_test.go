//go:build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

type routeVisWant struct {
	see, use, manage bool
}

func routeVisAccess(role, kind string, onTeam bool) routeVisWant {
	manage := role == "owner" || role == "admin" || role == "custom"
	switch kind {
	case "own":
		return routeVisWant{see: true, use: true, manage: true}
	case "private":
		return routeVisWant{}
	case "team":
		return routeVisWant{see: onTeam || manage, use: onTeam, manage: manage}
	case "everyone":
		return routeVisWant{see: true, use: true, manage: manage}
	case "installed":
		return routeVisWant{see: true, use: true, manage: false}
	default:
		return routeVisWant{}
	}
}

type routeVisWorld struct {
	t         *testing.T
	owner     *pgxpool.Pool
	app       *pgxpool.Pool
	people    map[string]string
	shared    map[string]string
	own       map[string]string
	sharedAcc map[string]string
	ownAcc    map[string]string
	handler   http.Handler
	providers app.Providers
	routes    app.RouteService
}

func newRouteVisWorld(t *testing.T) *routeVisWorld {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := audit.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, pool)
	keys := masterKeys(t, 'r')
	members := identity.Members(appPool, nil, "http://example.test", nil, nil)
	providers := NewProviders(appPool, keys, nil)
	providers.LocalPlan = func(context.Context, string) bool { return true }
	providers.Allow = func(ctx context.Context, user string) (string, error) {
		id, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.SettingsAllow(ctx, id)
	}
	providers.Teams = func(ctx context.Context, user string) ([]string, error) {
		id, err := identity.ParseUserID(user)
		if err != nil {
			return nil, err
		}
		return members.TeamsOf(ctx, id)
	}
	providers.Names = func(ctx context.Context, teams []string) (map[string]string, error) {
		_, names, err := members.NamesOf(ctx, nil, teams)
		return names, err
	}
	routesSvc := NewRoutes(appPool, nil)
	routesSvc.Allow = providers.Allow
	routesSvc.Teams = providers.Teams
	routesSvc.Names = providers.Names

	mux := http.NewServeMux()
	access := func(r *http.Request) (string, bool) {
		u := r.Header.Get("X-Test-User")
		return u, u != ""
	}
	ProvidersHandler{Service: providers, Access: access, StepUp: func(*http.Request) bool { return true }}.MountProviders(mux, nil)
	RoutesHandler{
		Book:     NewRouteBook(appPool),
		Service:  routesSvc,
		Accounts: providers,
		Access:   access,
	}.MountRoutes(mux, nil)

	w := &routeVisWorld{
		t:         t,
		owner:     pool,
		app:       appPool,
		people:    map[string]string{"owner": visOwner, "admin": visAdmin, "member": visMember, "viewer": visViewer, "custom": visCustom},
		shared:    map[string]string{},
		own:       map[string]string{},
		sharedAcc: map[string]string{},
		ownAcc:    map[string]string{},
		handler:   mux,
		providers: providers,
		routes:    routesSvc,
	}

	w.exec(`INSERT INTO identity.roles (name, permissions) VALUES ('Desk', '{settings:manage}')`)
	for _, person := range []struct{ id, email, role string }{
		{visOwner, "owner@example.com", "owner"},
		{visAdmin, "admin@example.com", "admin"},
		{visMember, "member@example.com", "member"},
		{visViewer, "viewer@example.com", "viewer"},
		{visCustom, "custom@example.com", "Desk"},
		{visStranger, "stranger@example.com", "member"},
	} {
		w.exec(`
			INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3 OR (key IS NULL AND name = $3)), now())`,
			person.id, person.email, person.role)
	}
	w.exec(`INSERT INTO identity.teams (id, name) VALUES ($1::uuid, 'Finance')`, visTeam)

	insertAcct := func(id, ref, owner, vis string, house bool) {
		teams := "{}"
		if vis == "team" {
			teams = "{" + visTeam + "}"
		}
		w.exec(`
			INSERT INTO gateway.provider_accounts
				(id, owner_user_id, name, adapter, credential_ref, plan_kind, house, provider, visibility, team_ids, status)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, 'fake', $4, 'metered', $5, 'fake', $6, $7::uuid[], 'Connected')`,
			id, owner, ref, ref, house, vis, teams)
		w.exec(`INSERT INTO gateway.provider_models (account_id, model, position) VALUES ($1::uuid, 'm', 1)`, id)
		w.exec(`INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce) VALUES ($1, decode('00', 'hex'), decode('000000000000000000000000', 'hex'), 'k', decode('00', 'hex'), decode('000000000000000000000000', 'hex'))`, ref)
	}

	w.sharedAcc["private"] = "20000000-0000-7000-8000-000000000001"
	w.sharedAcc["team"] = "20000000-0000-7000-8000-000000000002"
	w.sharedAcc["everyone"] = "20000000-0000-7000-8000-000000000003"
	w.sharedAcc["house"] = "20000000-0000-7000-8000-000000000004"
	insertAcct(w.sharedAcc["private"], "ref_stranger", visStranger, "private", false)
	insertAcct(w.sharedAcc["team"], "ref_team", visAdmin, "team", false)
	insertAcct(w.sharedAcc["everyone"], "ref_everyone", visAdmin, "everyone", false)
	insertAcct(w.sharedAcc["house"], "ref_house", "", "everyone", true)

	for role, uid := range w.people {
		id := "30000000-0000-7000-8000-00000000000" + uid[len(uid)-1:]
		w.ownAcc[role] = id
		insertAcct(id, "ref_own_"+role, uid, "private", false)
	}

	insertRoute := func(id, name, owner, vis string, installed bool, acctID string) {
		teams := "{}"
		if vis == "team" {
			teams = "{" + visTeam + "}"
		}
		w.exec(`
			INSERT INTO gateway.routes (id, name, capability, decision, owner_user_id, visibility, team_ids, installed)
			VALUES ($1::uuid, $2, 'chat', '', NULLIF($3, '')::uuid, $4, $5::uuid[], $6)`,
			id, name, owner, vis, teams, installed)
		w.exec(`
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, weight, retry_budget)
			VALUES ($1::uuid, 1, $2::uuid, 'm', 1, 1)`, id, acctID)
	}

	w.shared["private"] = "40000000-0000-7000-8000-000000000001"
	w.shared["team"] = "40000000-0000-7000-8000-000000000002"
	w.shared["everyone"] = "40000000-0000-7000-8000-000000000003"
	w.shared["installed"] = "40000000-0000-7000-8000-000000000004"
	insertRoute(w.shared["private"], "route-stranger", visStranger, "private", false, w.sharedAcc["private"])
	insertRoute(w.shared["team"], "route-team", visAdmin, "team", false, w.sharedAcc["team"])
	insertRoute(w.shared["everyone"], "route-everyone", visAdmin, "everyone", false, w.sharedAcc["everyone"])
	insertRoute(w.shared["installed"], "route-installed", "", "everyone", true, w.sharedAcc["house"])

	for role, uid := range w.people {
		id := "50000000-0000-7000-8000-00000000000" + uid[len(uid)-1:]
		w.own[role] = id
		insertRoute(id, "route-own-"+role, uid, "private", false, w.ownAcc[role])
	}
	return w
}

func (w *routeVisWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

func (w *routeVisWorld) call(user, method, path string, body any) (int, []byte) {
	w.t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			w.t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, r)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (w *routeVisWorld) listFor(user string) map[string]app.ListedRoute {
	w.t.Helper()
	code, raw := w.call(user, http.MethodGet, "/api/gateway/routes", nil)
	if code != http.StatusOK {
		w.t.Fatalf("list for %s = %d %s", user, code, raw)
	}
	var rows []app.ListedRoute
	if err := json.Unmarshal(raw, &rows); err != nil {
		w.t.Fatal(err)
	}
	out := make(map[string]app.ListedRoute, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

func (w *routeVisWorld) tryAs(user, allow, sql string, args ...any) (int64, error) {
	w.t.Helper()
	ctx := context.Background()
	tx, err := w.app.Begin(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	teams := "{}"
	if user != "" {
		var got *string
		if err := w.owner.QueryRow(ctx, `
			SELECT array_agg(team_id ORDER BY team_id)::text
			FROM identity.team_members WHERE user_id = $1::uuid`, user).Scan(&got); err != nil {
			w.t.Fatal(err)
		}
		if got != nil {
			teams = *got
		}
	}
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
			set_config('upmo.allow', $2, true),
			set_config('upmo.teams', $3, true)`, user, allow, teams); err != nil {
		w.t.Fatal(err)
	}
	tag, err := tx.Exec(ctx, sql, args...)
	return tag.RowsAffected(), err
}

func TestRouteAccessMatrixAndRLS(t *testing.T) {
	w := newRouteVisWorld(t)

	for _, onTeam := range []bool{false, true} {
		label := "off-team"
		if onTeam {
			label = "on-team"
			for _, uid := range w.people {
				w.exec(`INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, visTeam, uid)
			}
		}
		t.Run(label, func(t *testing.T) {
			for role, uid := range w.people {
				allow := ""
				if role == "owner" || role == "admin" || role == "custom" {
					allow = domain.SharePermission
				}
				listed := w.listFor(uid)
				for _, kind := range []string{"own", "private", "team", "everyone", "installed"} {
					id := w.shared[kind]
					if kind == "own" {
						id = w.own[role]
					}
					want := routeVisAccess(role, kind, onTeam)
					got, seen := listed[id]
					if seen != want.see {
						t.Errorf("%s %s see = %v, want %v", role, kind, seen, want.see)
					}
					if seen {
						if got.CanUse != want.use {
							t.Errorf("%s %s can_use = %v, want %v", role, kind, got.CanUse, want.use)
						}
						if got.CanManage != want.manage {
							t.Errorf("%s %s can_manage = %v, want %v", role, kind, got.CanManage, want.manage)
						}
						if got.Usable != want.use {
							t.Errorf("%s %s usable = %v, want %v", role, kind, got.Usable, want.use)
						}
					}
					// Direct SQL RLS checks on gateway.routes and gateway.route_links.
					n, err := w.tryAs(uid, allow, `SELECT 1 FROM gateway.routes WHERE id = $1::uuid`, id)
					if err != nil {
						t.Fatalf("%s %s select route: %v", role, kind, err)
					}
					if (n == 1) != want.see {
						t.Errorf("%s %s sql routes select = %d, want see=%v", role, kind, n, want.see)
					}
					ln, err := w.tryAs(uid, allow, `SELECT 1 FROM gateway.route_links WHERE route_id = $1::uuid`, id)
					if err != nil {
						t.Fatalf("%s %s select link: %v", role, kind, err)
					}
					if (ln == 1) != want.see {
						t.Errorf("%s %s sql route_links select = %d, want see=%v", role, kind, ln, want.see)
					}
					un, err := w.tryAs(uid, allow, `UPDATE gateway.routes SET name = name || '-x' WHERE id = $1::uuid`, id)
					if err != nil {
						t.Fatalf("%s %s update route: %v", role, kind, err)
					}
					if (un == 1) != want.manage {
						t.Errorf("%s %s sql routes update = %d, want manage=%v", role, kind, un, want.manage)
					}
					dn, err := w.tryAs(uid, allow, `DELETE FROM gateway.route_links WHERE route_id = $1::uuid`, id)
					if err != nil {
						t.Fatalf("%s %s delete link: %v", role, kind, err)
					}
					if (dn == 1) != want.manage {
						t.Errorf("%s %s sql route_links delete = %d, want manage=%v", role, kind, dn, want.manage)
					}
				}
			}
		})
	}
}

func TestRouteValidationShareAndAntiShadowingHTTP(t *testing.T) {
	w := newRouteVisWorld(t)

	// 1. Private route anti-shadowing against installed, everyone, and non-member team routes (422).
	for _, shadowed := range []string{"route-installed", "route-everyone", "route-team"} {
		code, raw := w.call(visMember, http.MethodPost, "/api/gateway/routes", map[string]any{
			"name":       shadowed,
			"capability": "chat",
			"hops":       []map[string]any{{"account_id": w.ownAcc["member"], "model": "m"}},
		})
		if code != http.StatusUnprocessableEntity {
			t.Errorf("shadow %q = %d %s, want 422", shadowed, code, raw)
		}
	}

	// 2. Two users can each create a private route with the same name; duplicate for same user is 409.
	code, raw := w.call(visMember, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "personal-fast",
		"capability": "chat",
		"hops":       []map[string]any{{"account_id": w.ownAcc["member"], "model": "m"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("member create = %d %s", code, raw)
	}
	var memberRoute app.ListedRoute
	if err := json.Unmarshal(raw, &memberRoute); err != nil {
		t.Fatal(err)
	}
	code, raw = w.call(visViewer, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "personal-fast",
		"capability": "chat",
		"hops":       []map[string]any{{"account_id": w.ownAcc["viewer"], "model": "m"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("viewer same private name = %d %s", code, raw)
	}
	code, raw = w.call(visMember, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "personal-fast",
		"capability": "chat",
		"hops":       []map[string]any{{"account_id": w.ownAcc["member"], "model": "m"}},
	})
	if code != http.StatusConflict {
		t.Fatalf("member duplicate = %d %s, want 409", code, raw)
	}

	// 3. MaxRouteHops = 3 and cycle rejection (422).
	code, raw = w.call(visMember, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "too-long",
		"capability": "chat",
		"hops": []map[string]any{
			{"account_id": w.ownAcc["member"], "model": "m1"},
			{"account_id": w.ownAcc["member"], "model": "m2"},
			{"account_id": w.sharedAcc["everyone"], "model": "m3"},
			{"account_id": w.sharedAcc["house"], "model": "m4"},
		},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf(">3 hops = %d %s, want 422", code, raw)
	}
	code, raw = w.call(visMember, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "cycle-route",
		"capability": "chat",
		"hops": []map[string]any{
			{"account_id": w.ownAcc["member"], "model": "m1"},
			{"account_id": w.ownAcc["member"], "model": "m1"},
		},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("cycle = %d %s, want 422", code, raw)
	}

	// 4. Actor must have can_use on every hop's provider_account_id.
	// Stranger's private account -> 422.
	code, raw = w.call(visAdmin, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "bad-hop-stranger",
		"capability": "chat",
		"hops":       []map[string]any{{"account_id": w.sharedAcc["private"], "model": "m"}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("stranger account hop = %d %s, want 422", code, raw)
	}
	// Team account that admin manages (may_manage) but is NOT on the team (may_use=false) -> 403.
	code, raw = w.call(visAdmin, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "bad-hop-team",
		"capability": "chat",
		"hops":       []map[string]any{{"account_id": w.sharedAcc["team"], "model": "m"}},
	})
	if code != http.StatusForbidden {
		t.Fatalf("unusable team account hop = %d %s, want 403", code, raw)
	}

	// 5. Shared routes cannot reference a private provider account on create, update, or share (422).
	code, raw = w.call(visAdmin, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "shared-with-priv",
		"capability": "chat",
		"visibility": "everyone",
		"hops":       []map[string]any{{"account_id": w.ownAcc["admin"], "model": "m"}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("create shared with private account = %d %s, want 422", code, raw)
	}
	// Admin creates a private route with their private account, then tries to share it -> 422.
	code, raw = w.call(visAdmin, http.MethodPost, "/api/gateway/routes", map[string]any{
		"name":       "admin-priv",
		"capability": "chat",
		"hops":       []map[string]any{{"account_id": w.ownAcc["admin"], "model": "m"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("admin private create = %d %s", code, raw)
	}
	var adminPriv app.ListedRoute
	if err := json.Unmarshal(raw, &adminPriv); err != nil {
		t.Fatal(err)
	}
	code, raw = w.call(visAdmin, http.MethodPut, "/api/gateway/routes/"+adminPriv.ID+"/share", map[string]any{
		"visibility": "everyone",
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("share private route with private account = %d %s, want 422", code, raw)
	}
	// Replace hop with everyone account, then sharing succeeds!
	code, raw = w.call(visAdmin, http.MethodPatch, "/api/gateway/routes/"+adminPriv.ID, map[string]any{
		"hops": []map[string]any{{"account_id": w.sharedAcc["everyone"], "model": "m"}},
	})
	if code != http.StatusOK {
		t.Fatalf("patch hop = %d %s", code, raw)
	}
	code, raw = w.call(visAdmin, http.MethodPut, "/api/gateway/routes/"+adminPriv.ID+"/share", map[string]any{
		"visibility": "team",
		"team_ids":   []string{visTeam},
	})
	if code != http.StatusOK {
		t.Fatalf("share valid team route = %d %s", code, raw)
	}
	// Trying to PATCH a private account hop back into the now-shared route -> 422.
	code, raw = w.call(visAdmin, http.MethodPatch, "/api/gateway/routes/"+adminPriv.ID, map[string]any{
		"hops": []map[string]any{{"account_id": w.ownAcc["admin"], "model": "m"}},
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("patch shared route with private account = %d %s, want 422", code, raw)
	}
	// Trying to make the shared everyone provider account private while shared route uses it -> 422.
	code, raw = w.call(visAdmin, http.MethodPut, "/api/providers/"+w.sharedAcc["everyone"], map[string]any{
		"visibility": "private",
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("unshare provider used by shared route = %d %s, want 422", code, raw)
	}

	// 6. Member cannot share their own route (403); owner cannot edit/share/delete installed route (403).
	code, _ = w.call(visMember, http.MethodPut, "/api/gateway/routes/"+memberRoute.ID+"/share", map[string]any{
		"visibility": "everyone",
	})
	if code != http.StatusForbidden {
		t.Fatalf("member share = %d, want 403", code)
	}
	for _, req := range []struct{ method, path string }{
		{http.MethodPatch, "/api/gateway/routes/" + w.shared["installed"]},
		{http.MethodPut, "/api/gateway/routes/" + w.shared["installed"] + "/share"},
		{http.MethodDelete, "/api/gateway/routes/" + w.shared["installed"]},
	} {
		code, raw = w.call(visOwner, req.method, req.path, map[string]any{"name": "x", "visibility": "everyone"})
		if code != http.StatusForbidden {
			t.Errorf("%s %s installed = %d %s, want 403", req.method, req.path, code, raw)
		}
	}
}

func TestRouteDefaultsAndDownUp(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		t.Fatal(err)
	}

	const routeOwner = "019a0000-0000-7000-8000-000000000001"
	var routeID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO gateway.routes (owner_user_id, name, capability, decision)
		VALUES ($1::uuid, 'default-chat', 'chat', '')
		RETURNING id::text`, routeOwner).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	var installed bool
	var vis, owner string
	var teams []string
	if err := pool.QueryRow(ctx, `
		SELECT installed, visibility, COALESCE(owner_user_id::text, ''), team_ids::text[]
		FROM gateway.routes WHERE id = $1::uuid`, routeID).Scan(&installed, &vis, &owner, &teams); err != nil {
		t.Fatal(err)
	}
	if installed || vis != "everyone" || owner != routeOwner || len(teams) != 0 {
		t.Fatalf("default route = installed:%v vis:%q owner:%q teams:%v", installed, vis, owner, teams)
	}

	var forced int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_class
		WHERE oid IN ('gateway.routes'::regclass, 'gateway.route_links'::regclass)
		  AND relrowsecurity AND relforcerowsecurity`).Scan(&forced); err != nil || forced != 2 {
		t.Fatalf("forced RLS tables = %d, %v", forced, err)
	}

	if err := migrate.Down(ctx, pool, Schema, Migrations()); err != nil {
		t.Fatalf("down: %v", err)
	}
	var ns *string
	if err := pool.QueryRow(ctx, `SELECT to_regnamespace('gateway')::text`).Scan(&ns); err != nil {
		t.Fatal(err)
	}
	if ns != nil {
		t.Fatalf("gateway schema survived down: %q", *ns)
	}
	if err := migrate.Up(ctx, pool, Schema, Migrations()); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version_id) FROM gateway.goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("version after re-up = %d, %v", version, err)
	}
}

func TestSharedRouteAccountVisibilityBlindSpots(t *testing.T) {
	w := newRouteVisWorld(t)

	createEveryoneAccount := func(owner, name string) string {
		t.Helper()
		code, raw := w.call(owner, http.MethodPost, "/api/providers", map[string]any{
			"provider":   "custom",
			"name":       name,
			"adapter":    "openai_compat",
			"plan_kind":  "local",
			"base_url":   "http://127.0.0.1:11434",
			"models":     []string{"m"},
			"visibility": "everyone",
		})
		if code != http.StatusCreated {
			t.Fatalf("create provider %q = %d %s", name, code, raw)
		}
		return decode[struct {
			ID string `json:"id"`
		}](t, raw).ID
	}

	queryBoolAs := func(user, allow, sql string, arg string) bool {
		t.Helper()
		ctx := context.Background()
		tx, err := w.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `
			SELECT set_config('upmo.user_id', $1, true),
				set_config('upmo.allow', $2, true),
				set_config('upmo.teams', '{}', true)`, user, allow); err != nil {
			t.Fatal(err)
		}
		var out bool
		if err := tx.QueryRow(ctx, sql, arg).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}

	t.Run("invisible team route blocks making account private", func(t *testing.T) {
		acctID := createEveryoneAccount(visOwner, "Owner Everyone A")

		code, raw := w.call(visAdmin, http.MethodPost, "/api/gateway/routes", map[string]any{
			"name":       "team-only-route-a",
			"capability": "chat",
			"visibility": "team",
			"team_ids":   []string{visTeam},
			"hops":       []map[string]any{{"account_id": acctID, "model": "m"}},
		})
		if code != http.StatusCreated {
			t.Fatalf("create team route = %d %s", code, raw)
		}
		teamRoute := decode[app.ListedRoute](t, raw)

		// Under RLS, a caller without settings:manage and off visTeam cannot see
		// the team route, yet the SECURITY DEFINER helper detects the shared use.
		n, err := w.tryAs(visMember, "", `SELECT 1 FROM gateway.routes WHERE id = $1::uuid`, teamRoute.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("off-team member saw %d rows for team route, want 0", n)
		}
		if !queryBoolAs(visMember, "", `SELECT gateway.provider_account_used_by_shared_route($1::uuid)`, acctID) {
			t.Fatal("provider_account_used_by_shared_route = false, want true")
		}

		// Account owner (also off visTeam) cannot make the account private.
		code, raw = w.call(visOwner, http.MethodPut, "/api/providers/"+acctID, map[string]any{
			"visibility": "private",
		})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("make account private = %d %s, want 422", code, raw)
		}
		if got := message(t, raw); got != "A provider used by a shared route cannot be made private." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("invisible private account blocks sharing existing private route", func(t *testing.T) {
		acctID := createEveryoneAccount(visOwner, "Owner Everyone B")

		code, raw := w.call(visAdmin, http.MethodPost, "/api/gateway/routes", map[string]any{
			"name":       "admin-priv-b",
			"capability": "chat",
			"hops":       []map[string]any{{"account_id": acctID, "model": "m"}},
		})
		if code != http.StatusCreated {
			t.Fatalf("create private route = %d %s", code, raw)
		}
		privRoute := decode[app.ListedRoute](t, raw)

		// Only a private route links acctID, so visOwner can make it private.
		code, raw = w.call(visOwner, http.MethodPut, "/api/providers/"+acctID, map[string]any{
			"visibility": "private",
		})
		if code != http.StatusOK {
			t.Fatalf("make account private = %d %s, want 200", code, raw)
		}

		// Under RLS, visAdmin (even with settings:manage) can no longer see acctID,
		// yet the SECURITY DEFINER helper detects the private account on privRoute.
		n, err := w.tryAs(visAdmin, domain.SharePermission, `SELECT 1 FROM gateway.provider_accounts WHERE id = $1::uuid`, acctID)
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("admin saw %d rows for owner's private account, want 0", n)
		}
		if !queryBoolAs(visAdmin, domain.SharePermission, `SELECT gateway.route_has_private_account($1::uuid)`, privRoute.ID) {
			t.Fatal("route_has_private_account = false, want true")
		}

		// Sharing the existing private route via PUT .../share must fail with 422.
		code, raw = w.call(visAdmin, http.MethodPut, "/api/gateway/routes/"+privRoute.ID+"/share", map[string]any{
			"visibility": "everyone",
		})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("PUT share = %d %s, want 422", code, raw)
		}
		if got := message(t, raw); got != "A shared route cannot use a private provider account." {
			t.Fatalf("PUT share error = %q", got)
		}

		// Changing visibility via PATCH without replacing hops must also fail with 422.
		code, raw = w.call(visAdmin, http.MethodPatch, "/api/gateway/routes/"+privRoute.ID, map[string]any{
			"visibility": "team",
			"team_ids":   []string{visTeam},
		})
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("PATCH share = %d %s, want 422", code, raw)
		}
		if got := message(t, raw); got != "A shared route cannot use a private provider account." {
			t.Fatalf("PATCH share error = %q", got)
		}
	})
}
