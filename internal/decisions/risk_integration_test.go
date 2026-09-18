//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
)

var riskIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestSecondOpinionStoresPassingPermissionRisk(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.SecondOpinion(
		context.Background(),
		store,
		riskIntegrationAccess(),
		riskIntegrationHops,
		riskIntegrationCases(),
		riskIntegrationRunner{choice: decisions.RiskAsk},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	var hit, total, bar int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind, hit, total, bar FROM decisions.gates WHERE route = $1`,
		decisions.RouteRisk,
	).Scan(&route, &kind, &hit, &total, &bar); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteRisk ||
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

func TestFirstSecondOpinionAllowingDangerStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.SecondOpinion(
		context.Background(),
		store,
		riskIntegrationAccess(),
		riskIntegrationHops,
		riskIntegrationCases(),
		riskIntegrationRunner{choice: decisions.RiskAllow},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
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

func TestPassingSecondOpinionDoesNotStoreToolSurface(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.SecondOpinion(
		context.Background(),
		store,
		riskIntegrationAccess(),
		riskIntegrationHops,
		riskIntegrationCases(),
		riskIntegrationRunner{choice: decisions.RiskAsk},
	)
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteSurface,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("tool-surface rows = %d", count)
	}
}

func riskIntegrationCases() []decisions.Risk {
	return []decisions.Risk{{
		Prompt: "Review permission",
		Prior:  decisions.RiskAllow,
		Danger: true,
	}}
}

func riskIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type riskIntegrationRunner struct {
	choice string
}

func (r riskIntegrationRunner) Decide(context.Context, string, []string) (bool, string, error) {
	return false, r.choice, nil
}
