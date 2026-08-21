package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

func uuidOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func numericOrNil(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func numericPtr(n pgtype.Numeric) *float64 {
	if !n.Valid {
		return nil
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	v := f.Float64
	return &v
}

// Price looks up one catalog row. Missing is not an error.
func (s Store) Price(ctx context.Context, model string) (domain.Price, bool, error) {
	var row struct {
		Version    string
		Input      pgtype.Numeric
		Output     pgtype.Numeric
		CacheRead  pgtype.Numeric
		CacheWrite pgtype.Numeric
	}
	err := s.Pool.QueryRow(ctx, `
		SELECT version, input_usd_per_mtok, output_usd_per_mtok,
			cache_read_usd_per_mtok, cache_write_usd_per_mtok
		FROM gateway.prices
		WHERE model = $1`, model).Scan(
		&row.Version, &row.Input, &row.Output, &row.CacheRead, &row.CacheWrite,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Price{}, false, nil
		}
		return domain.Price{}, false, err
	}
	return domain.Price{
		Version:        row.Version,
		InputPerMTok:   numericPtr(row.Input),
		OutputPerMTok:  numericPtr(row.Output),
		CacheReadMTok:  numericPtr(row.CacheRead),
		CacheWriteMTok: numericPtr(row.CacheWrite),
	}, true, nil
}

// Record writes one attempt. cost_usd stays NULL when CostUSD is nil.
func (s Store) Record(ctx context.Context, row domain.Entry) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO gateway.ledger (
			route, link_position, provider_account_id, model, plan_kind, failure_class,
			user_id, agent_id, run_id, parent_run_id,
			input_tokens, output_tokens, cache_read, cache_write, reasoning,
			usage_arrived, cost_usd, price_version
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18
		)`,
		row.Route, row.LinkPosition, uuidOrNil(row.AccountID), row.Model, row.PlanKind, string(row.Class),
		uuidOrNil(row.Principal.User), uuidOrNil(row.Principal.Agent),
		uuidOrNil(row.Principal.Run), uuidOrNil(row.Principal.Parent),
		row.Usage.Input, row.Usage.Output, row.Usage.CacheRead, row.Usage.CacheWrite, row.Usage.Reasoning,
		row.Usage.Arrived, numericOrNil(row.CostUSD), nullIfEmpty(row.PriceVersion),
	)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
