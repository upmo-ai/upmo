package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

func (f Facts) InRetrieve(ctx context.Context, userID string, fn func(app.RetrievalTx) error) error {
	return f.inPersonTx(ctx, userID, func(tx personTx) error {
		return fn(tx)
	})
}

func (p personTx) NearText(ctx context.Context, asOf time.Time, vector []float32, limit int) ([]app.Candidate, error) {
	if _, err := p.tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, fmt.Errorf("memory: set iterative scan: %w", err)
	}
	if _, err := p.tx.Exec(ctx, `SET LOCAL hnsw.ef_search = 100`); err != nil {
		return nil, fmt.Errorf("memory: set hnsw search: %w", err)
	}
	rows, err := p.tx.Query(ctx, `
		SELECT id::text, claim
		FROM memory.facts
		WHERE user_id = $1::uuid
		  AND embedding IS NOT NULL
		  AND valid_from <= $2
		  AND (invalid_at IS NULL OR invalid_at > $2)
		ORDER BY embedding <=> $3::halfvec(1024)
		LIMIT $4`,
		p.userID, asOf, formatHalfvec(vector), limit)
	if err != nil {
		return nil, fmt.Errorf("memory: near text: %w", err)
	}
	defer rows.Close()

	var candidates []app.Candidate
	for rows.Next() {
		var candidate app.Candidate
		if err := rows.Scan(&candidate.ID, &candidate.Claim); err != nil {
			return nil, fmt.Errorf("memory: near text: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: near text: %w", err)
	}
	return candidates, nil
}

func (p personTx) MatchText(ctx context.Context, asOf time.Time, query string, limit int) ([]app.Candidate, error) {
	rows, err := p.tx.Query(ctx, `
		SELECT id::text, claim
		FROM memory.facts
		WHERE user_id = $1::uuid
		  AND valid_from <= $2
		  AND (invalid_at IS NULL OR invalid_at > $2)
		  AND claim_tsv @@ websearch_to_tsquery('english', $3)
		ORDER BY ts_rank(claim_tsv, websearch_to_tsquery('english', $3)) DESC,
		         id::text ASC
		LIMIT $4`,
		p.userID, asOf, query, limit)
	if err != nil {
		return nil, fmt.Errorf("memory: match text: %w", err)
	}
	defer rows.Close()

	var candidates []app.Candidate
	for rows.Next() {
		var candidate app.Candidate
		if err := rows.Scan(&candidate.ID, &candidate.Claim); err != nil {
			return nil, fmt.Errorf("memory: match text: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: match text: %w", err)
	}
	return candidates, nil
}
