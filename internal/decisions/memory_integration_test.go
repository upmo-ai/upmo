//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

var memoryIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestMemoryCheckStoresPassingPrecision(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.MemoryCheck(
		context.Background(),
		store,
		memoryIntegrationAccess(),
		memoryIntegrationHops,
		100,
		[]decisions.Turn{{Prompt: "Read memory?", Yes: true}},
		memoryIntegrationRunner{yes: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	var hit, total int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind, hit, total FROM decisions.gates WHERE route = $1`,
		decisions.RouteMemory,
	).Scan(&route, &kind, &hit, &total); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteMemory || kind != decisions.KindYesNo || hit != 1 || total != 1 {
		t.Fatalf("stored route = %q kind = %q hit = %d total = %d", route, kind, hit, total)
	}
}

func TestFirstMemoryCheckAllNoStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.MemoryCheck(
		context.Background(),
		store,
		memoryIntegrationAccess(),
		memoryIntegrationHops,
		100,
		[]decisions.Turn{{Prompt: "Read memory?", Yes: true}},
		memoryIntegrationRunner{yes: false},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
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

func TestPassingMemoryCheckDoesNotStoreDispatch(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.MemoryCheck(
		context.Background(),
		store,
		memoryIntegrationAccess(),
		memoryIntegrationHops,
		100,
		[]decisions.Turn{{Prompt: "Read memory?", Yes: true}},
		memoryIntegrationRunner{yes: true},
	)
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteDispatch,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("agent-dispatch rows = %d", count)
	}
}

func memoryIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type memoryIntegrationRunner struct {
	yes bool
}

func (r memoryIntegrationRunner) Decide(context.Context, string, []string) (bool, string, error) {
	return r.yes, "", nil
}
