//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestRetrieveIndexesExist(t *testing.T) {
	owner := up(t)
	rows, err := owner.Query(context.Background(), `
		SELECT indexname
		FROM pg_indexes
		WHERE schemaname = 'memory'
		  AND indexname IN ('facts_embedding_hnsw', 'facts_claim_fts')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"facts_embedding_hnsw", "facts_claim_fts"} {
		if !got[name] {
			t.Errorf("missing index %s", name)
		}
	}
}

func TestRetrieveFusesCurrentFactsForPerson(t *testing.T) {
	store, asOf := retrieveStore(t)
	got, err := app.Retrieve(
		context.Background(), store, retrieveEmbedder{index: 0}, nil,
		person, "oolong", asOf,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertRetrieveClaims(t, got, "Ada prefers oolong tea", "Lee drinks coffee")
}

func TestRetrieveObeysReranker(t *testing.T) {
	store, asOf := retrieveStore(t)
	got, err := app.Retrieve(
		context.Background(), store, retrieveEmbedder{index: 0}, reverseReranker{},
		person, "oolong", asOf,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertRetrieveClaims(t, got, "Lee drinks coffee", "Ada prefers oolong tea")
}

func TestRetrieveKeepsFusedOrderWhenRerankerFails(t *testing.T) {
	store, asOf := retrieveStore(t)
	got, err := app.Retrieve(
		context.Background(), store, retrieveEmbedder{index: 0},
		errorReranker{err: errors.New("rerank failed")},
		person, "oolong", asOf,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertRetrieveClaims(t, got, "Ada prefers oolong tea", "Lee drinks coffee")
}

func retrieveStore(t *testing.T) (postgres.Facts, time.Time) {
	t.Helper()
	owner := up(t)
	ctx := context.Background()
	asOf := time.Now().UTC()
	facts := []struct {
		user      string
		hash      byte
		claim     string
		validFrom time.Time
		invalidAt any
		embedding string
	}{
		{person, 1, "Ada prefers oolong tea", asOf.Add(-time.Hour), nil, axis(0)},
		{person, 2, "Lee drinks coffee", asOf.Add(-time.Hour), nil, axis(1)},
		{person, 3, "Ada previously preferred oolong", asOf.Add(-2 * time.Hour), asOf.Add(-time.Hour), axis(0)},
		{"018f3b2c-7a10-7c4a-8e11-222222222222", 4, "Other person prefers oolong", asOf.Add(-time.Hour), nil, axis(0)},
	}
	for _, fact := range facts {
		if _, err := owner.Exec(ctx, insertRetrieveFact,
			fact.user, bytes32(fact.hash), fact.claim, fact.validFrom, fact.invalidAt, fact.embedding,
		); err != nil {
			t.Fatal(err)
		}
	}
	return postgres.Facts{Pool: pgtest.App(t, owner)}, asOf
}

func assertRetrieveClaims(t *testing.T, got []app.Candidate, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d candidates (%+v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i].Claim != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, got[i].Claim, want[i])
		}
	}
}

type retrieveEmbedder struct {
	index int
}

func (e retrieveEmbedder) Embed(context.Context, []string) (string, [][]float32, error) {
	vector := make([]float32, 1024)
	vector[e.index] = 1
	return "model-a", [][]float32{vector}, nil
}

type reverseReranker struct{}

func (reverseReranker) Rerank(_ context.Context, _ string, candidates []app.Candidate) ([]app.Candidate, error) {
	out := make([]app.Candidate, len(candidates))
	for i := range candidates {
		out[len(candidates)-1-i] = candidates[i]
	}
	return out, nil
}

type errorReranker struct {
	err error
}

func (r errorReranker) Rerank(context.Context, string, []app.Candidate) ([]app.Candidate, error) {
	return nil, r.err
}

const insertRetrieveFact = `
	INSERT INTO memory.facts (
		user_id, trust, claim, claim_hash, valid_from, invalid_at, last_confirmed_at,
		embedding, embedding_model, source_kind, source_id, source_seq, created_at
	) VALUES (
		$1, 'tool', $3, $2, $4, $5, $4,
		$6::halfvec(1024), 'model-a', 'person',
		'018f3b2c-7a10-7c4a-8e11-333333333333', 1, $4
	)`
