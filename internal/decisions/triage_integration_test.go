//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
)

var triageIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestTriageStoresItsChoiceRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Triage(
		context.Background(),
		store,
		triageIntegrationAccess(),
		triageIntegrationHops,
		100,
		[]decisions.Message{{Body: "Turn this into work", Label: decisions.TriageTask}},
		triageIntegrationRunner{choice: decisions.TriageTask},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind FROM decisions.gates WHERE route = $1`,
		decisions.RouteTriage,
	).Scan(&route, &kind); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteTriage || kind != decisions.KindChoice {
		t.Fatalf("stored route = %q kind = %q", route, kind)
	}
}

func TestFirstTriageMissStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Triage(
		context.Background(),
		store,
		triageIntegrationAccess(),
		triageIntegrationHops,
		100,
		[]decisions.Message{{Body: "Please respond", Label: decisions.TriageReply}},
		triageIntegrationRunner{choice: decisions.TriageIgnore},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteTriage,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("channel-triage rows = %d", count)
	}
}

func TestTriageDoesNotStoreFolderFiling(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Triage(
		context.Background(),
		store,
		triageIntegrationAccess(),
		triageIntegrationHops,
		100,
		[]decisions.Message{{Body: "Please respond", Label: decisions.TriageReply}},
		triageIntegrationRunner{choice: decisions.TriageReply},
	)
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteFiling,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("folder-filing rows = %d", count)
	}
}

func triageIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type triageIntegrationRunner struct {
	choice string
}

func (r triageIntegrationRunner) Decide(context.Context, string, []string) (bool, string, error) {
	return false, r.choice, nil
}
