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

	made, err := providers.Create(ctx, staleOwner, domain.Provider{
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
		`INSERT INTO gateway.routes (name, capability) VALUES ('usable-chat', 'chat') RETURNING id::text`,
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
	accounts, err := providers.List(ctx, staleOwner)
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
	accounts, err = providers.List(ctx, staleOwner)
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
