package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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

// ScanNear is the write-time cosine check for one new vector. The id is the
// forgotten row on drop, the active row on reinforce, and empty on insert.
func ScanNear(ctx context.Context, tx pgx.Tx, userID string, vector []float32) (string, string, error) {
	emb := formatHalfvec(vector)
	forgotten, err := nearest(ctx, tx, userID, emb, true)
	if err != nil {
		return "", "", err
	}
	active, err := nearest(ctx, tx, userID, emb, false)
	if err != nil {
		return "", "", err
	}
	action, _ := domain.DecideNearDup(forgotten, active)
	switch action {
	case "drop":
		if len(forgotten) > 0 {
			return action, forgotten[0].ID, nil
		}
	case "reinforce":
		if len(active) > 0 {
			return action, active[0].ID, nil
		}
	}
	return action, "", nil
}

func formatHalfvec(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
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
