//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

var surfaceIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestPrefilterStoresPassingToolSurface(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.Prefilter(
		context.Background(),
		store,
		surfaceIntegrationAccess(),
		surfaceIntegrationHops,
		surfaceIntegrationCases(),
		surfaceIntegrationFilter{kept: []string{"search_memory"}},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	var hit, total, bar int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind, hit, total, bar FROM decisions.gates WHERE route = $1`,
		decisions.RouteSurface,
	).Scan(&route, &kind, &hit, &total, &bar); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteSurface ||
		kind != decisions.KindFilter ||
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

func TestFirstPrefilterDroppingCalledToolStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.Prefilter(
		context.Background(),
		store,
		surfaceIntegrationAccess(),
		surfaceIntegrationHops,
		surfaceIntegrationCases(),
		surfaceIntegrationFilter{kept: []string{"list_tasks"}},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
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

func TestPassingPrefilterDoesNotStoreFactKeep(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.Prefilter(
		context.Background(),
		store,
		surfaceIntegrationAccess(),
		surfaceIntegrationHops,
		surfaceIntegrationCases(),
		surfaceIntegrationFilter{kept: []string{"search_memory"}},
	)
	if err != nil {
		t.Fatal(err)
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

func surfaceIntegrationCases() []decisions.Surface {
	return []decisions.Surface{{
		Prompt:  "Find memory",
		Offered: []string{"search_memory", "list_tasks"},
		Called:  []string{"search_memory"},
	}}
}

func surfaceIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type surfaceIntegrationFilter struct {
	kept []string
}

func (f surfaceIntegrationFilter) Keep(context.Context, string, []string) ([]string, error) {
	return append([]string(nil), f.kept...), nil
}
