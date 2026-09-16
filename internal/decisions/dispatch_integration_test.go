//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	integrationUser   = "018f3b2c-7a10-7c4a-8e11-111111111111"
	integrationAgentA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	integrationAgentB = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

var integrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestDispatchStoresItsChoiceRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.Dispatch(
		context.Background(),
		store,
		integrationAccess(),
		integrationHops,
		100,
		integrationDispatchCases(),
		integrationChoice{choice: integrationAgentA},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind FROM decisions.gates WHERE route = $1`,
		decisions.RouteDispatch,
	).Scan(&route, &kind); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteDispatch || kind != decisions.KindChoice {
		t.Fatalf("stored route = %q kind = %q", route, kind)
	}
}

func TestIntentStoresItsRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.Intent(
		context.Background(),
		store,
		integrationAccess(),
		integrationHops,
		100,
		integrationIntentCases(),
		integrationChoice{choice: "write"},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route string
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route FROM decisions.gates WHERE route = $1`,
		decisions.RouteIntent,
	).Scan(&route); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteIntent {
		t.Fatalf("stored route = %q", route)
	}
}

func TestFirstDispatchMissStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.Dispatch(
		context.Background(),
		store,
		integrationAccess(),
		integrationHops,
		100,
		integrationDispatchCases(),
		integrationChoice{choice: integrationAgentB},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
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
		t.Fatalf("dispatch rows = %d", count)
	}
}

func TestAcceptingIntentDoesNotStoreDispatch(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	err := decisions.Intent(
		context.Background(),
		store,
		integrationAccess(),
		integrationHops,
		100,
		integrationIntentCases(),
		integrationChoice{choice: "write"},
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
		t.Fatalf("dispatch rows = %d", count)
	}
}

func integrationAccess() decisions.Access {
	return decisions.Access{UserID: integrationUser, Allow: "settings:manage"}
}

func integrationDispatchCases() []decisions.Labelled {
	return []decisions.Labelled{{
		Prompt:  "Choose an agent",
		Options: []string{integrationAgentA, integrationAgentB},
		Label:   integrationAgentA,
	}}
}

func integrationIntentCases() []decisions.Labelled {
	return []decisions.Labelled{{
		Prompt:  "Choose an intent",
		Options: []string{"read", "write"},
		Label:   "write",
	}}
}

type integrationChoice struct {
	choice string
}

func (r integrationChoice) Decide(context.Context, string, []string) (bool, string, error) {
	return false, r.choice, nil
}
