//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// A listed route names the account every hop calls, and each account carries
// the status and models the usable answer reads: a person sees usable=false
// for a Not connected account's models and for every route through it, and
// usable only once a working outcome has landed.
func TestListedRoutesNameAccountsAUsableAnswerCanRead(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	providers := postgres.Providers{Pool: pgtest.App(t, pool)}
	store := postgres.Store{Pool: pool}

	made, err := providers.Create(ctx, app.Caller{User: staleOwner}, domain.Provider{
		Provider:      "fake",
		Name:          "usable-check",
		Adapter:       domain.AdapterFake,
		PlanKind:      domain.PlanLocal,
		Status:        domain.StatusNotConnected,
		Models:        []string{"m1"},
		Own:           true,
		CredentialRef: "own_usable",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var routeID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO gateway.routes (name, capability, installed) VALUES ('usable-chat', 'chat', true) RETURNING id::text`,
	).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO gateway.route_links (route_id, position, provider_account_id, model) VALUES ($1::uuid, 1, $2::uuid, 'm1')`,
		routeID, made.ID); err != nil {
		t.Fatal(err)
	}

	// The listed hop names the account, so the HTTP layer can match it
	// against the person's own list.
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	route, found := usableRouteRow(rows, "usable-chat")
	if !found || len(route.Hops) != 1 {
		t.Fatalf("listed routes = %+v", rows)
	}
	if route.Hops[0].AccountID != made.ID || route.Hops[0].Model != "m1" {
		t.Fatalf("hop = %+v, want m1 on %s", route.Hops[0], made.ID)
	}

	// Fresh from the wizard the account is Not connected: no usable model,
	// so no usable route through it.
	accounts, err := providers.List(ctx, app.Caller{User: staleOwner})
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Status != domain.StatusNotConnected {
		t.Fatalf("accounts = %+v", accounts)
	}
	if got := domain.UsableModels(accounts[0]); len(got) != 0 {
		t.Fatalf("a Not connected account listed usable models %v", got)
	}
	if domain.UsableRoute(usableRouteView(route), accounts) {
		t.Fatal("a route through a Not connected account read usable")
	}

	// A working outcome lands, and the same rows now read usable.
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusConnected, time.Now()); err != nil {
		t.Fatal(err)
	}
	accounts, err = providers.List(ctx, app.Caller{User: staleOwner})
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Status != domain.StatusConnected {
		t.Fatalf("accounts after the outcome = %+v", accounts)
	}
	if !domain.UsableRoute(usableRouteView(route), accounts) {
		t.Fatal("a route through a Connected account that lists the model read unusable")
	}
}

func usableRouteRow(rows []app.ListedRoute, name string) (app.ListedRoute, bool) {
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return app.ListedRoute{}, false
}

func usableRouteView(r app.ListedRoute) domain.UsableRouteView {
	view := domain.UsableRouteView{Name: r.Name, Capability: domain.Capability(r.Capability)}
	for _, hop := range r.Hops {
		view.Hops = append(view.Hops, domain.UsableHop{AccountID: hop.AccountID, Model: hop.Model})
	}
	return view
}

func TestStoreByNameForWorkerUserAndTeamIsolation(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: pool}

	addUser := func(name, role string) string {
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1, sha256(convert_to($1, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3), now())
			RETURNING id::text`, name+"@example.com", name, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	addTeam := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	joinTeam := func(teamID, userID string) {
		if _, err := pool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, teamID, userID); err != nil {
			t.Fatal(err)
		}
	}

	_ = addUser("founder", "owner")
	alice := addUser("alice", "member")
	bob := addUser("bob", "member")
	teamA := addTeam("Alpha")
	teamB := addTeam("Beta")
	joinTeam(teamA, alice)
	joinTeam(teamB, alice)

	var acctID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('house-acct', 'fake', 'ref_house', 'metered', true, 'everyone')
		RETURNING id::text`).Scan(&acctID); err != nil {
		t.Fatal(err)
	}

	addRoute := func(name, owner, vis string, installed bool, model string, teams ...string) {
		if teams == nil {
			teams = []string{}
		}
		var routeID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO gateway.routes (name, capability, owner_user_id, visibility, team_ids, installed)
			VALUES ($1, 'chat', NULLIF($2, '')::uuid, $3, $4::uuid[], $5)
			RETURNING id::text`, name, owner, vis, teams, installed).Scan(&routeID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
			VALUES ($1::uuid, 1, $2::uuid, $3, 0)`, routeID, acctID, model); err != nil {
			t.Fatal(err)
		}
	}

	// Installed, everyone, team-A, and two users' private routes with the same name.
	addRoute("inst-route", "", "everyone", true, "m-inst")
	addRoute("every-route", alice, "everyone", false, "m-every")
	addRoute("team-a-route", alice, "team", false, "m-team-a", teamA)
	addRoute("shared-priv-name", alice, "private", false, "m-alice")
	addRoute("shared-priv-name", bob, "private", false, "m-bob")

	// System worker (who.User == "") resolves installed & shared routes, never private.
	for _, okName := range []string{"inst-route", "every-route", "team-a-route"} {
		if _, err := store.ByNameFor(ctx, okName, domain.Principal{}); err != nil {
			t.Errorf("worker ByNameFor(%q) failed: %v", okName, err)
		}
	}
	if _, err := store.ByNameFor(ctx, "shared-priv-name", domain.Principal{}); err == nil {
		t.Fatal("worker resolved a private route")
	}
	if _, err := store.ByName(ctx, "shared-priv-name"); err == nil {
		t.Fatal("Store.ByName resolved a private route")
	}

	// Alice and Bob each resolve their own private route under the same name.
	rAlice, err := store.ByNameFor(ctx, "shared-priv-name", domain.Principal{User: alice})
	if err != nil || len(rAlice.Links) != 1 || rAlice.Links[0].Model != "m-alice" {
		t.Fatalf("alice private route = %+v, err=%v", rAlice, err)
	}
	rBob, err := store.ByNameFor(ctx, "shared-priv-name", domain.Principal{User: bob})
	if err != nil || len(rBob.Links) != 1 || rBob.Links[0].Model != "m-bob" {
		t.Fatalf("bob private route = %+v, err=%v", rBob, err)
	}

	// Team route: Alice is on teamA; Bob is not.
	if _, err := store.ByNameFor(ctx, "team-a-route", domain.Principal{User: alice}); err != nil {
		t.Fatalf("alice team route failed: %v", err)
	}
	if _, err := store.ByNameFor(ctx, "team-a-route", domain.Principal{User: bob}); err == nil {
		t.Fatal("bob resolved a team route he is not on")
	}

	// Single-team narrowing (Principal.TeamID): Alice on teamA & teamB.
	if _, err := store.ByNameFor(ctx, "team-a-route", domain.Principal{User: alice, TeamID: teamA}); err != nil {
		t.Fatalf("alice narrowed to teamA failed: %v", err)
	}
	if _, err := store.ByNameFor(ctx, "team-a-route", domain.Principal{User: alice, TeamID: teamB}); err == nil {
		t.Fatal("alice narrowed to teamB should not resolve teamA route")
	}

	// Dispatch uses ByNameFor: system worker cannot call private route; Alice can.
	adapter := &pinAdapter{}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Ledger:  store,
		Secrets: pinSecrets{"ref_house": "house-key"},
		Stream:  map[string]app.StreamChatter{domain.AdapterFake: adapter},
	}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	if err := disp.ChatStreamPinned(ctx, "shared-priv-name", "", domain.Principal{}, in, domain.Pin{}, 0, &pinSink{}); err == nil {
		t.Fatal("Dispatch allowed system worker to call private route")
	}
	sink := &pinSink{}
	if err := disp.ChatStreamPinned(ctx, "shared-priv-name", "", domain.Principal{User: alice, Source: "system"}, in, domain.Pin{}, 0, sink); err != nil {
		t.Fatalf("Dispatch failed for Alice's private route: %v", err)
	}
	if sink.done == nil || sink.done.Model != "m-alice" {
		t.Fatalf("Dispatch used wrong hop: done=%+v fail=%+v", sink.done, sink.fail)
	}
}
