package app

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/knowledge/domain"
	"github.com/upmo-ai/upmo/internal/platform/retrieval"
)

const (
	// LexicalCandidates is the FTS body match list. A prefix match alone is not on it.
	LexicalCandidates = 50
	// DenseCandidates is the halfvec neighbor list when embeddings exist.
	DenseCandidates = 50
	// SearchLimit is how many fused hits a call returns after optional rerank.
	SearchLimit = 8
	// RRFK is the reciprocal rank fusion constant (same as memory).
	RRFK = 60
)

// Provenance identifies whether a source chunk came from curated workspace
// material or an untrusted web-fetched source.
type Provenance string

const (
	ProvenanceCurated Provenance = "curated"
	ProvenanceWeb     Provenance = "web"
)

// Untrusted reports whether content with this provenance must be treated as
// untrusted input by the write guard. Unknown or empty provenance fails closed.
func (p Provenance) Untrusted() bool {
	return p != ProvenanceCurated
}

// Hit is evidence: the verbatim chunk. The ranking prefix is not included.
type Hit struct {
	SourceID   string
	Seq        int
	Body       string
	Provenance Provenance
}

// Lexical is one candidate chunk. Vector may be absent on FTS rows.
type Lexical struct {
	SourceID   string
	Seq        int
	Body       string
	Provenance Provenance
	Vector     []float32
}

// Finder runs folder-scoped lexical and dense queries. The folder filter is
// applied before each limit.
type Finder interface {
	Lexical(ctx context.Context, access Access, folderID, query string, limit int) ([]Lexical, error)
	Near(ctx context.Context, access Access, folderID string, vector []float32, limit int) ([]Lexical, error)
}

// Reranker reorders fused hits. It cannot add a chunk absent from the fused set.
type Reranker interface {
	Rerank(ctx context.Context, query string, hits []Hit) ([]Hit, error)
}

// Search returns verbatim chunks from one folder. FTS and halfvec lists are
// fused with reciprocal rank fusion when an embedder answers; otherwise FTS
// order stands. A nil or failing reranker leaves the fused order.
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
	if finder == nil {
		return []Hit{}, nil
	}
	lexical, err := finder.Lexical(ctx, access, folderID, query, LexicalCandidates)
	if err != nil {
		return nil, err
	}
	denseHits := near(ctx, finder, embedder, access, folderID, query)
	var fused []Hit
	switch {
	case len(denseHits) == 0:
		fused = toHits(lexical)
	case len(lexical) == 0:
		fused = toHits(denseHits)
	default:
		fused = toHits(retrieval.Fuse([][]Lexical{lexical, denseHits}, lexicalKey, lexicalLess, mergeLexical, RRFK, LexicalCandidates))
	}
	var rank func(context.Context, string, []Hit) ([]Hit, error)
	if reranker != nil {
		rank = reranker.Rerank
	}
	fused = retrieval.Rerank(ctx, query, fused, hitKey, rank)
	fused = retrieval.Pack(fused, SearchLimit, func(Hit) int { return 1 })
	return fused, nil
}

func near(
	ctx context.Context,
	finder Finder,
	embedder Embedder,
	access Access,
	folderID, query string,
) []Lexical {
	if embedder == nil {
		return nil
	}
	_, vectors, err := embedder.Embed(ctx, []string{query})
	if err != nil || len(vectors) != 1 || domain.VectorOK(vectors[0]) != nil {
		return nil
	}
	hits, err := finder.Near(ctx, access, folderID, vectors[0], DenseCandidates)
	if err != nil || len(hits) == 0 {
		return nil
	}
	return hits
}

func toHits(matches []Lexical) []Hit {
	out := make([]Hit, len(matches))
	for i, match := range matches {
		out[i] = Hit{
			SourceID:   match.SourceID,
			Seq:        match.Seq,
			Body:       match.Body,
			Provenance: match.Provenance,
		}
	}
	return out
}

type hitID struct {
	sourceID string
	seq      int
}

func hitKey(hit Hit) hitID { return hitID{sourceID: hit.SourceID, seq: hit.Seq} }

func lexicalKey(hit Lexical) hitID { return hitID{sourceID: hit.SourceID, seq: hit.Seq} }

func lexicalLess(a, b Lexical) bool {
	if a.SourceID != b.SourceID {
		return a.SourceID < b.SourceID
	}
	return a.Seq < b.Seq
}

func mergeLexical(a, b Lexical) Lexical {
	if b.Provenance.Untrusted() {
		a.Provenance = b.Provenance
	}
	return a
}
