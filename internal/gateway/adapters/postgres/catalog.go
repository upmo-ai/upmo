package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// ReplaceCatalog writes a refreshed catalog in one transaction.
func (s Store) ReplaceCatalog(ctx context.Context, version string, rows []domain.CatalogRow) error {
	if s.Pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM gateway.prices`); err != nil {
		return err
	}
	for _, row := range rows {
		ctxWindow := row.ContextWindow
		if ctxWindow <= 0 {
			ctxWindow = row.Price.ContextWindow
		}
		if ctxWindow <= 0 {
			ctxWindow = domain.DefaultContextWindow
		}
		caps := row.Caps
		if caps == nil {
			caps = []string{}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO gateway.prices (
				model, version, input_usd_per_mtok, output_usd_per_mtok,
				cache_read_usd_per_mtok, cache_write_usd_per_mtok, capabilities, context_window
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			row.Model, row.Version,
			numericOrNil(row.Price.InputPerMTok),
			numericOrNil(row.Price.OutputPerMTok),
			numericOrNil(row.Price.CacheReadMTok),
			numericOrNil(row.Price.CacheWriteMTok),
			caps,
			ctxWindow,
		)
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway.catalog_meta (id, version) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version`, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Capabilities returns the catalog caps for a model. ok is false when absent.
func (s Store) Capabilities(ctx context.Context, model string) ([]string, bool, error) {
	var caps []string
	err := s.Pool.QueryRow(ctx, `
		SELECT capabilities FROM gateway.prices WHERE model = $1`, model).Scan(&caps)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return caps, true, nil
}
