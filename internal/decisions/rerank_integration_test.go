//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/decisions"
)

var rerankIntegrationHops = []decisions.Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestRerankIntegrationStoresPerfectRanking(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Rerank(
		context.Background(),
		store,
		rerankIntegrationAccess(),
		rerankIntegrationHops,
		100,
		rerankIntegrationRankings(),
		rerankIntegrationRanker{ids: []string{"chunk_a", "chunk_b"}},
	)
	if err != nil {
		t.Fatal(err)
	}

	var route, kind string
	var hit, total, bar int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT route, kind, hit, total, bar FROM decisions.gates WHERE route = $1`,
		decisions.RouteRerank,
	).Scan(&route, &kind, &hit, &total, &bar); err != nil {
		t.Fatal(err)
	}
	if route != decisions.RouteRerank ||
		kind != decisions.KindRank ||
		hit != 100 ||
		total != 100 ||
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

func TestFirstReversedRerankStoresNoRoute(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Rerank(
		context.Background(),
		store,
		rerankIntegrationAccess(),
		rerankIntegrationHops,
		100,
		rerankIntegrationRankings(),
		rerankIntegrationRanker{ids: []string{"chunk_b", "chunk_a"}},
	)
	if !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("error = %v", err)
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

func TestPassingRerankDoesNotStoreChannelTriage(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pool}
	err := decisions.Rerank(
		context.Background(),
		store,
		rerankIntegrationAccess(),
		rerankIntegrationHops,
		100,
		rerankIntegrationRankings(),
		rerankIntegrationRanker{ids: []string{"chunk_a", "chunk_b"}},
	)
	if err != nil {
		t.Fatal(err)
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

func rerankIntegrationRankings() []decisions.Ranking {
	return []decisions.Ranking{{
		Query: "Find the answer",
		Passages: []decisions.Passage{
			{ID: "chunk_a", Text: "Relevant text", Relevance: 1},
			{ID: "chunk_b", Text: "Other text", Relevance: 0},
		},
	}}
}

func rerankIntegrationAccess() decisions.Access {
	return decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
}

type rerankIntegrationRanker struct {
	ids []string
}

func (r rerankIntegrationRanker) Order(context.Context, string, []decisions.Passage) ([]string, error) {
	return append([]string(nil), r.ids...), nil
}
