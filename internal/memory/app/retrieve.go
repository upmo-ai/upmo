package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/domain"
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
// then reranks when a reranker answers. It does not fence; Search does.
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

	fused := fuse(near, match)
	out := rerank(ctx, reranker, query, fused)
	if len(out) > RetrieveLimit {
		out = out[:RetrieveLimit]
	}
	return out, nil
}

func fuse(lists ...[]Candidate) []Candidate {
	type row struct {
		candidate Candidate
		score     float64
	}
	rows := map[string]*row{}
	for _, list := range lists {
		for i, c := range list {
			r, ok := rows[c.ID]
			if !ok {
				r = &row{candidate: c}
				rows[c.ID] = r
			}
			r.score += 1 / float64(RRFK+i+1)
		}
	}
	ranked := make([]*row, 0, len(rows))
	for _, r := range rows {
		ranked = append(ranked, r)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].candidate.ID < ranked[j].candidate.ID
	})
	if len(ranked) > RetrieveCandidates {
		ranked = ranked[:RetrieveCandidates]
	}
	out := make([]Candidate, len(ranked))
	for i, r := range ranked {
		out[i] = r.candidate
	}
	return out
}

// rerank keeps the fused order when the reranker is absent, fails, or
// returns nothing. Otherwise it keeps fused IDs in the reranker's order.
func rerank(ctx context.Context, reranker Reranker, query string, fused []Candidate) []Candidate {
	if reranker == nil || len(fused) == 0 {
		return fused
	}
	got, err := reranker.Rerank(ctx, query, fused)
	if err != nil || len(got) == 0 {
		return fused
	}
	known := make(map[string]Candidate, len(fused))
	for _, c := range fused {
		known[c.ID] = c
	}
	seen := make(map[string]struct{}, len(got))
	out := make([]Candidate, 0, len(got))
	for _, c := range got {
		f, ok := known[c.ID]
		if !ok {
			continue
		}
		if _, dup := seen[c.ID]; dup {
			continue
		}
		seen[c.ID] = struct{}{}
		out = append(out, f)
	}
	if len(out) == 0 {
		return fused
	}
	return out
}
