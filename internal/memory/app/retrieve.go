package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/domain"
	"github.com/upmo-ai/upmo/internal/platform/retrieval"
)

// Retrieval sizes. Each list and the fused list hold at most
// RetrieveCandidates. A call returns at most RetrieveLimit.
const (
	RetrieveCandidates = 50
	RetrieveLimit      = 8
	RRFK               = 60
)

// Candidate is one fact a retrieval list returned.
type Candidate struct {
	ID    string
	Claim string
}

// RetrievalTx searches one person's facts valid at asOf.
type RetrievalTx interface {
	NearText(ctx context.Context, asOf time.Time, vector []float32, limit int) ([]Candidate, error)
	MatchText(ctx context.Context, asOf time.Time, query string, limit int) ([]Candidate, error)
}

// RetrievalStore runs fn scoped to one person.
type RetrievalStore interface {
	InRetrieve(ctx context.Context, userID string, fn func(RetrievalTx) error) error
}

// Reranker orders claims against the query. It may be absent.
type Reranker interface {
	Rerank(ctx context.Context, query string, claims []Candidate) ([]Candidate, error)
}

// Retrieve fuses a vector list and a text list with reciprocal rank fusion,
// then reranks when a reranker answers. SearchMemory fences the result.
func Retrieve(ctx context.Context, store RetrievalStore, embedder Embedder, reranker Reranker, userID, query string, asOf time.Time) ([]Candidate, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("memory: user id")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("memory: query")
	}
	if asOf.IsZero() {
		return nil, fmt.Errorf("memory: as of")
	}

	model, vectors, err := embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) == "" {
		return nil, fmt.Errorf("memory: embed model")
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("memory: embed count %d, want 1", len(vectors))
	}
	if err := domain.Valid(vectors[0]); err != nil {
		return nil, fmt.Errorf("memory: query vector: %w", err)
	}

	var near, match []Candidate
	err = store.InRetrieve(ctx, userID, func(tx RetrievalTx) error {
		var err error
		if near, err = tx.NearText(ctx, asOf, vectors[0], RetrieveCandidates); err != nil {
			return err
		}
		match, err = tx.MatchText(ctx, asOf, query, RetrieveCandidates)
		return err
	})
	if err != nil {
		return nil, err
	}

	fused := retrieval.Fuse([][]Candidate{near, match}, func(c Candidate) string { return c.ID }, func(a, b Candidate) bool {
		return a.ID < b.ID
	}, nil, RRFK, RetrieveCandidates)
	var rank func(context.Context, string, []Candidate) ([]Candidate, error)
	if reranker != nil {
		rank = reranker.Rerank
	}
	out := retrieval.Rerank(ctx, query, fused, func(c Candidate) string { return c.ID }, rank)
	out = retrieval.Pack(out, RetrieveLimit, func(Candidate) int { return 1 })
	return out, nil
}
