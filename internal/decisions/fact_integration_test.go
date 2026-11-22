//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

var factIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestFactKeepStoresPassingRecall(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.FactKeep(
		context.Background(),
		store,
		factIntegrationAccess(),
		factIntegrationHops,
		100,
		[]decisions.Fact{{Claim: "Worth keeping", Yes: true}},
		factIntegrationRunner{yes: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	var hit, total int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind, hit, total FROM decisions.gates WHERE route = $1`,
		decisions.RouteFact,
	).Scan(&route, &kind, &hit, &total); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteFact || kind != decisions.KindYesNo || hit != 1 || total != 1 {
		t.Fatalf("stored route = %q kind = %q hit = %d total = %d", route, kind, hit, total)
	}
}

func TestFirstFactKeepWithoutLabeledYesStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.FactKeep(
		context.Background(),
		store,
		factIntegrationAccess(),
		factIntegrationHops,
		100,
		[]decisions.Fact{{Claim: "Not worth keeping", Yes: false}},
		factIntegrationRunner{yes: true},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteFact,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fact-keep rows = %d", count)
	}
}

func TestPassingFactKeepDoesNotStoreMemoryCheck(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.FactKeep(
		context.Background(),
		store,
		factIntegrationAccess(),
		factIntegrationHops,
		100,
		[]decisions.Fact{{Claim: "Worth keeping", Yes: true}},
		factIntegrationRunner{yes: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteMemory,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("memory-check rows = %d", count)
	}
}

func factIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type factIntegrationRunner struct {
	yes bool
}

func (r factIntegrationRunner) Decide(context.Context, string, []string) (bool, string, error) {
	return r.yes, "", nil
}
