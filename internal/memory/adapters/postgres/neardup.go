package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/memory/domain"
)

// NearDup finds the nearest forgotten vector for this person, then the nearest
// active one, using the pgvector cosine distance on halfvec(1024). No index:
// a personal set is scanned exactly.
func NearDup(ctx context.Context, tx pgx.Tx, userID, embedding string) (string, string, error) {
	forgotten, err := nearest(ctx, tx, userID, embedding, true)
	if err != nil {
		return "", "", err
	}
	active, err := nearest(ctx, tx, userID, embedding, false)
	if err != nil {
		return "", "", err
	}
	action, reason := domain.DecideNearDup(forgotten, active)
	return action, reason, nil
}

func nearest(ctx context.Context, tx pgx.Tx, userID, embedding string, forgotten bool) ([]domain.Hit, error) {
	var hit domain.Hit
	err := tx.QueryRow(ctx, `
		SELECT id::text, trust, 1 - (embedding <=> $2::halfvec(1024))
		FROM memory.facts
		WHERE user_id = $1::uuid
		  AND embedding IS NOT NULL
		  AND (invalid_at IS NOT NULL) = $3
		ORDER BY embedding <=> $2::halfvec(1024)
		LIMIT 1
	`, userID, embedding, forgotten).Scan(&hit.ID, &hit.Trust, &hit.Cosine)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: nearest fact: %w", err)
	}
	return []domain.Hit{hit}, nil
}
