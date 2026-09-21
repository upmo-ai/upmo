//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
)

var confidenceIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestConfidenceIntegrationStoresPassingPrecision(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Confidence(
		context.Background(),
		store,
		confidenceIntegrationAccess(),
		confidenceIntegrationHops,
		66,
		confidenceIntegrationOutputs(),
		&confidenceIntegrationRunner{answers: []bool{true, true, true, false}},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	var hit, total, bar int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind, hit, total, bar FROM decisions.gates WHERE route = $1`,
		decisions.RouteConfidence,
	).Scan(&route, &kind, &hit, &total, &bar); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteConfidence ||
		kind != decisions.KindYesNo ||
		hit != 2 ||
		total != 3 ||
		bar != 66 {
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

func TestFirstConfidenceAllNoStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Confidence(
		context.Background(),
		store,
		confidenceIntegrationAccess(),
		confidenceIntegrationHops,
		66,
		confidenceIntegrationOutputs(),
		&confidenceIntegrationRunner{answers: []bool{false, false, false, false}},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteConfidence,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("result-confidence rows = %d", count)
	}
}

func TestPassingConfidenceDoesNotStoreRerank(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Confidence(
		context.Background(),
		store,
		confidenceIntegrationAccess(),
		confidenceIntegrationHops,
		66,
		confidenceIntegrationOutputs(),
		&confidenceIntegrationRunner{answers: []bool{true, true, true, false}},
	)
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM decisions.gates WHERE route = $1`,
		decisions.RouteRerank,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rerank rows = %d", count)
	}
}

func confidenceIntegrationOutputs() []decisions.Output {
	return []decisions.Output{
		{Summary: "One", Yes: true},
		{Summary: "Two", Yes: true},
		{Summary: "Three", Yes: false},
		{Summary: "Four", Yes: true},
	}
}

func confidenceIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type confidenceIntegrationRunner struct {
	answers []bool
	calls   int
}

func (r *confidenceIntegrationRunner) Decide(context.Context, string, []string) (bool, string, error) {
	answer := false
	if r.calls < len(r.answers) {
		answer = r.answers[r.calls]
	}
	r.calls++
	return answer, "", nil
}
