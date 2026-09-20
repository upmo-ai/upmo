//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
)

const (
	filingIntegrationUser    = "018f3b2c-7a10-7c4a-8e11-111111111111"
	filingIntegrationFolderA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	filingIntegrationFolderB = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

var filingIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestFileSourceStoresItsChoiceRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.FileSource(
		context.Background(),
		store,
		filingIntegrationAccess(),
		filingIntegrationHops,
		100,
		filingIntegrationCases(),
		filingIntegrationRunner{choice: filingIntegrationFolderA},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind FROM decisions.gates WHERE route = $1`,
		decisions.RouteFiling,
	).Scan(&route, &kind); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteFiling || kind != decisions.KindChoice {
		t.Fatalf("stored route = %q kind = %q", route, kind)
	}
}

func TestFirstFileSourceMissStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.FileSource(
		context.Background(),
		store,
		filingIntegrationAccess(),
		filingIntegrationHops,
		100,
		filingIntegrationCases(),
		filingIntegrationRunner{choice: filingIntegrationFolderB},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
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

func TestFileSourceDoesNotStoreModelTier(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.FileSource(
		context.Background(),
		store,
		filingIntegrationAccess(),
		filingIntegrationHops,
		100,
		filingIntegrationCases(),
		filingIntegrationRunner{choice: filingIntegrationFolderA},
	)
	if err != nil {
		t.Fatal(err)
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

func filingIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: filingIntegrationUser,
		Allow:  "settings:manage",
	}
}

func filingIntegrationCases() []decisions.Labelled {
	return []decisions.Labelled{{
		Prompt:  "A source excerpt",
		Options: []string{filingIntegrationFolderA, filingIntegrationFolderB},
		Label:   filingIntegrationFolderA,
	}}
}

type filingIntegrationRunner struct {
	choice string
}

func (r filingIntegrationRunner) Decide(context.Context, string, []string) (bool, string, error) {
	return false, r.choice, nil
}
