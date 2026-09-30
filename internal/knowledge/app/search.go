package app

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/knowledge/domain"
)

const (
	// LexicalCandidates is the body match list. A prefix match alone is not on it.
	LexicalCandidates = 50
	// SearchLimit is how many of those matches a call returns.
	SearchLimit = 8
)

// Hit is evidence: the verbatim chunk. The ranking prefix is not included.
type Hit struct {
	SourceID string
	Seq      int
	Body     string
}

// Lexical is one body match. Vector may be absent.
type Lexical struct {
	SourceID string
	Seq      int
	Body     string
	Vector   []float32
}

// Finder runs the lexical query inside the folder. The folder filter is applied
// before the limit.
type Finder interface {
	Lexical(ctx context.Context, access Access, folderID, query string, limit int) ([]Lexical, error)
}

// Reranker reorders lexical hits. It cannot add a chunk the body query missed.
type Reranker interface {
	Rerank(ctx context.Context, query string, hits []Hit) ([]Hit, error)
}

// Search returns verbatim chunks from one folder. Dense order and a reranker
// only rearrange the lexical list. A failure of either leaves that list as it was.
func Search(
	ctx context.Context,
	finder Finder,
	embedder Embedder,
	reranker Reranker,
	access Access,
	folderID, query string,
) ([]Hit, error) {
	if err := checkAccess(access); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(folderID); err != nil {
		return nil, ErrFolder
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrQuery
	}
	matches, err := finder.Lexical(ctx, access, folderID, query, LexicalCandidates)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return []Hit{}, nil
	}
	matches = dense(ctx, embedder, query, matches)
	hits := make([]Hit, len(matches))
	for i, match := range matches {
		hits[i] = Hit{SourceID: match.SourceID, Seq: match.Seq, Body: match.Body}
	}
	hits = rerank(ctx, reranker, query, hits)
	if len(hits) > SearchLimit {
		hits = hits[:SearchLimit]
	}
	return hits, nil
}

func dense(ctx context.Context, embedder Embedder, query string, matches []Lexical) []Lexical {
	if embedder == nil {
		return matches
	}
	_, vectors, err := embedder.Embed(ctx, []string{query})
	if err != nil || len(vectors) != 1 || domain.VectorOK(vectors[0]) != nil {
		return matches
	}
	queryVec := vectors[0]
	type row struct {
		hit   Lexical
		score float64
		known bool
		index int
	}
	rows := make([]row, len(matches))
	for i, match := range matches {
		rows[i].hit = match
		rows[i].index = i
		if match.Vector == nil {
			continue
		}
		score, err := domain.Cosine(queryVec, match.Vector)
		if err != nil {
			continue
		}
		rows[i].score = score
		rows[i].known = true
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].known != rows[j].known {
			return rows[i].known
		}
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		return rows[i].index < rows[j].index
	})
	out := make([]Lexical, len(rows))
	for i := range rows {
		out[i] = rows[i].hit
	}
	return out
}

func rerank(ctx context.Context, reranker Reranker, query string, hits []Hit) []Hit {
	if reranker == nil || len(hits) == 0 {
		return hits
	}
	got, err := reranker.Rerank(ctx, query, hits)
	if err != nil || len(got) == 0 {
		return hits
	}
	known := map[string]Hit{}
	for _, hit := range hits {
		known[hit.SourceID+":"+strconv.Itoa(hit.Seq)] = hit
	}
	seen := map[string]bool{}
	out := make([]Hit, 0, len(got))
	for _, hit := range got {
		key := hit.SourceID + ":" + strconv.Itoa(hit.Seq)
		found, ok := known[key]
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, found)
	}
	if len(out) == 0 {
		return hits
	}
	return out
}
