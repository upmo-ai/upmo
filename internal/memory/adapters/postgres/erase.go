package postgres

import (
	"context"
	"fmt"
)

func (f Facts) Erase(ctx context.Context, userID, taskID string) error {
	err := f.inPersonTx(ctx, userID, func(tx personTx) error {
		_, err := tx.tx.Exec(ctx,
			`SELECT memory.erase($1::uuid, NULLIF($2, '')::uuid)`,
			userID, taskID,
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("memory: erase: %w", err)
	}
	return nil
}
