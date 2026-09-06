package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/upmo-ai/upmo/internal/memory/domain"
)

const ReindexBatch = 64

type Embedded struct {
	ID    string
	Claim string
	Model string
}

type ReindexTx interface {
	Embedded(ctx context.Context) ([]Embedded, error)
	ClearPin(ctx context.Context) error
	SetVector(ctx context.Context, id, model string, vector []float32) error
}

type ReindexStore interface {
	InReindex(ctx context.Context, userID string, fn func(ReindexTx) error) error
}

func Reindex(ctx context.Context, store ReindexStore, embedder Embedder, userID, model string) (int, error) {
	if strings.TrimSpace(userID) == "" {
		return 0, fmt.Errorf("memory: user id")
	}
	if strings.TrimSpace(model) == "" {
		return 0, fmt.Errorf("memory: embed model")
	}

	updated := 0
	err := store.InReindex(ctx, userID, func(tx ReindexTx) error {
		rows, err := tx.Embedded(ctx)
		if err != nil {
			return err
		}
		pending := make([]Embedded, 0, len(rows))
		for _, row := range rows {
			if row.Model != model {
				pending = append(pending, row)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		if err := tx.ClearPin(ctx); err != nil {
			return err
		}

		for start := 0; start < len(pending); start += ReindexBatch {
			end := min(start+ReindexBatch, len(pending))
			batch := pending[start:end]
			claims := make([]string, len(batch))
			for i, row := range batch {
				claims[i] = row.Claim
			}
			gotModel, vectors, err := embedder.Embed(ctx, claims)
			if err != nil {
				return err
			}
			if gotModel != model {
				return fmt.Errorf("memory: embed model %q, want %q", gotModel, model)
			}
			if len(vectors) != len(batch) {
				return fmt.Errorf("memory: embed count %d, want %d", len(vectors), len(batch))
			}
			for i, vector := range vectors {
				if err := domain.Valid(vector); err != nil {
					return fmt.Errorf("memory: vector for %s: %w", batch[i].ID, err)
				}
			}
			for i, row := range batch {
				if err := tx.SetVector(ctx, row.ID, model, vectors[i]); err != nil {
					return err
				}
			}
			updated += len(batch)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}
