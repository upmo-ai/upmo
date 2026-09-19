//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
)

var tierIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestTierRouteStoresPassingModelTier(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.TierRoute(
		context.Background(),
		store,
		tierIntegrationAccess(),
		tierIntegrationHops,
		100,
		tierIntegrationCanaries(),
		tierIntegrationRunner{yes: false, choice: "tier_b"},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	var hit, total, bar int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind, hit, total, bar FROM decisions.gates WHERE route = $1`,
		decisions.RouteTier,
	).Scan(&route, &kind, &hit, &total, &bar); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteTier ||
		kind != decisions.KindChoice ||
		hit != 1 ||
		total != 1 ||
		bar != 100 {
		t.Fatalf(
			"stored route = %q kind = %q hit = %d total = %d bar = %d",
			route,
			kind,
			hit,
			total,
			bar,
		)
	}
}

func TestFirstTierRouteMissingParityStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.TierRoute(
		context.Background(),
		store,
		tierIntegrationAccess(),
		tierIntegrationHops,
		100,
		tierIntegrationCanaries(),
		tierIntegrationRunner{yes: false, choice: "tier_a"},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteTier,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("model-tier rows = %d", count)
	}
}

func TestPassingTierRouteDoesNotStorePermissionRisk(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.TierRoute(
		context.Background(),
		store,
		tierIntegrationAccess(),
		tierIntegrationHops,
		100,
		tierIntegrationCanaries(),
		tierIntegrationRunner{yes: false, choice: "tier_b"},
	)
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteRisk,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("permission-risk rows = %d", count)
	}
}

func tierIntegrationCanaries() []decisions.Canary {
	return []decisions.Canary{{
		Goal:      "Route task",
		Tiers:     []string{"tier_a", "tier_b"},
		Reference: "tier_b",
		Succeeded: true,
	}}
}

func tierIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type tierIntegrationRunner struct {
	yes    bool
	choice string
}

func (r tierIntegrationRunner) Decide(context.Context, string, []string) (bool, string, error) {
	return r.yes, r.choice, nil
}
