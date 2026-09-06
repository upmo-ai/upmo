package postgres

import (
	"context"
	"fmt"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

func (f Facts) InReindex(ctx context.Context, userID string, fn func(app.ReindexTx) error) error {
	return f.inPersonTx(ctx, userID, func(tx personTx) error {
		return fn(tx)
	})
}

func (p personTx) Embedded(ctx context.Context) ([]app.Embedded, error) {
	rows, err := p.tx.Query(ctx, `
		SELECT id::text, claim, embedding_model
		FROM memory.facts
		WHERE user_id = $1::uuid AND embedding IS NOT NULL`, p.userID)
	if err != nil {
		return nil, fmt.Errorf("memory: embedded facts: %w", err)
	}
	defer rows.Close()

	var embedded []app.Embedded
	for rows.Next() {
		var row app.Embedded
		if err := rows.Scan(&row.ID, &row.Claim, &row.Model); err != nil {
			return nil, fmt.Errorf("memory: embedded facts: %w", err)
		}
		embedded = append(embedded, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: embedded facts: %w", err)
	}
	return embedded, nil
}

func (p personTx) ClearPin(ctx context.Context) error {
	if _, err := p.tx.Exec(ctx, `
		DELETE FROM memory.embed_models
		WHERE user_id = $1::uuid`, p.userID); err != nil {
		return fmt.Errorf("memory: clear embed model: %w", err)
	}
	return nil
}

func (p personTx) SetVector(ctx context.Context, id, model string, vector []float32) error {
	tag, err := p.tx.Exec(ctx, `
		UPDATE memory.facts
		SET embedding = $3::halfvec(1024), embedding_model = $4
		WHERE user_id = $1::uuid AND id = $2::uuid`,
		p.userID, id, formatHalfvec(vector), model)
	if err != nil {
		return fmt.Errorf("memory: set vector: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("memory: fact %s not found", id)
	}
	return nil
}
