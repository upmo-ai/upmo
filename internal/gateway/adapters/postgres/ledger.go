package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/kernel"
)

func uuidOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func microOrNil(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func intOrNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// Price looks up the exact (vendor, model) catalog row. Money rates come only
// from that exact row; when it is absent (and vendor != ""), non-money fields
// such as ContextWindow fall back to the empty-vendor (”) row while found
// stays false. Missing is not an error.
func (s Store) Price(ctx context.Context, vendor, model string) (domain.Price, bool, error) {
	vendor = domain.VendorKey(vendor)
	if vendor == "" {
		row, ok, err := s.lookupPriceRow(ctx, "", model)
		return row, ok, err
	}
	exact, exactFound, cross, err := s.lookupCatalogPrices(ctx, vendor, model)
	if err != nil {
		return domain.Price{}, false, err
	}
	if exactFound {
		return exact, true, nil
	}
	if cross != nil {
		return domain.Price{ContextWindow: cross.ContextWindow}, false, nil
	}
	return domain.Price{}, false, nil
}

func (s Store) lookupPriceRow(ctx context.Context, vendor, model string) (domain.Price, bool, error) {
	var p domain.Price
	err := s.Pool.QueryRow(ctx, `
		SELECT version,
			round(input_usd_per_mtok * $3)::bigint,
			round(output_usd_per_mtok * $3)::bigint,
			round(cache_read_usd_per_mtok * $3)::bigint,
			round(cache_write_usd_per_mtok * $3)::bigint,
			context_window
		FROM gateway.prices
		WHERE model = $2
		  AND vendor = $1`, vendor, model, kernel.MicroUSDPerDollar).Scan(
		&p.Version,
		&p.InputMicroUSDPerMTok, &p.OutputMicroUSDPerMTok,
		&p.CacheReadMicroUSDPerMTok, &p.CacheWriteMicroUSDPerMTok,
		&p.ContextWindow,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Price{}, false, nil
		}
		return domain.Price{}, false, err
	}
	tiers, err := s.lookupPriceTiers(ctx, vendor, model)
	if err != nil {
		return domain.Price{}, false, err
	}
	p.Tiers = tiers
	return p, true, nil
}

func (s Store) lookupPriceTiers(ctx context.Context, vendor, model string) ([]domain.PriceTier, error) {
	rows, err := s.Pool.Query(ctx, `SELECT context_min_tokens,
		round(input_usd_per_mtok * $3)::bigint, round(output_usd_per_mtok * $3)::bigint,
		round(cache_read_usd_per_mtok * $3)::bigint, round(cache_write_usd_per_mtok * $3)::bigint
		FROM gateway.price_tiers WHERE vendor=$1 AND model=$2 ORDER BY context_min_tokens`, vendor, model, kernel.MicroUSDPerDollar)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tiers []domain.PriceTier
	for rows.Next() {
		var t domain.PriceTier
		if err := rows.Scan(&t.ContextMinTokens, &t.Price.InputMicroUSDPerMTok, &t.Price.OutputMicroUSDPerMTok, &t.Price.CacheReadMicroUSDPerMTok, &t.Price.CacheWriteMicroUSDPerMTok); err != nil {
			return nil, err
		}
		tiers = append(tiers, t)
	}
	return tiers, rows.Err()
}

func (s Store) lookupCatalogPrices(ctx context.Context, vendor, model string) (exact domain.Price, exactFound bool, cross *domain.Price, err error) {
	vendor = domain.VendorKey(vendor)
	rows, err := s.Pool.Query(ctx, `
		SELECT vendor, version,
			round(input_usd_per_mtok * $3)::bigint,
			round(output_usd_per_mtok * $3)::bigint,
			round(cache_read_usd_per_mtok * $3)::bigint,
			round(cache_write_usd_per_mtok * $3)::bigint,
			context_window
		FROM gateway.prices
		WHERE model = $2
		  AND vendor IN ($1, '')`, vendor, model, kernel.MicroUSDPerDollar)
	if err != nil {
		return domain.Price{}, false, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rowVendor string
		var p domain.Price
		if err := rows.Scan(
			&rowVendor, &p.Version,
			&p.InputMicroUSDPerMTok, &p.OutputMicroUSDPerMTok,
			&p.CacheReadMicroUSDPerMTok, &p.CacheWriteMicroUSDPerMTok,
			&p.ContextWindow,
		); err != nil {
			return domain.Price{}, false, nil, err
		}
		switch {
		case vendor != "" && rowVendor == vendor:
			exact = p
			exactFound = true
		case rowVendor == "":
			cp := p
			cross = &cp
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Price{}, false, nil, err
	}
	if exactFound {
		exact.Tiers, err = s.lookupPriceTiers(ctx, vendor, model)
		if err != nil {
			return domain.Price{}, false, nil, err
		}
	}
	if cross != nil {
		cross.Tiers, err = s.lookupPriceTiers(ctx, "", model)
		if err != nil {
			return domain.Price{}, false, nil, err
		}
	}
	if !exactFound && cross != nil && cross.ContextWindow > 0 {
		exact.ContextWindow = cross.ContextWindow
	} else if exactFound && exact.ContextWindow <= 0 && cross != nil && cross.ContextWindow > 0 {
		exact.ContextWindow = cross.ContextWindow
	}
	return exact, exactFound, cross, nil
}

// HighestRate returns the largest per-million-token catalog rate over vendor's
// own rows, never the empty-vendor fallback rows.
func (s Store) HighestRate(ctx context.Context, vendor string) (int64, bool, error) {
	vendor = domain.VendorKey(vendor)
	if vendor == "" {
		return 0, false, nil
	}
	var rate *int64
	err := s.Pool.QueryRow(ctx, `
		SELECT max(rate) FROM (
			SELECT greatest(
				COALESCE(round(input_usd_per_mtok * $2)::bigint, 0) * 2,
				COALESCE(round(output_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_read_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_write_usd_per_mtok * $2)::bigint, 0)
			) AS rate
			FROM gateway.prices
			WHERE vendor <> '' AND lower(vendor) = lower($1)
			UNION ALL
			SELECT greatest(
				COALESCE(round(pt.input_usd_per_mtok * $2)::bigint, 0) * 2,
				COALESCE(round(pt.output_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(pt.cache_read_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(pt.cache_write_usd_per_mtok * $2)::bigint, 0)) AS rate
			FROM gateway.price_tiers pt
			WHERE pt.vendor <> '' AND lower(pt.vendor) = lower($1)
		) AS rates WHERE rate > 0`, vendor, kernel.MicroUSDPerDollar).Scan(&rate)
	if err != nil {
		return 0, false, err
	}
	if rate == nil {
		return 0, false, nil
	}
	return *rate, true, nil
}

// EstimateRate preserves the per-vendor fallback rate. HighestRate includes
// reservation headroom for possible one-hour cache writes; fallback billing
// estimates must keep using the catalog's existing rate ceiling.
func (s Store) EstimateRate(ctx context.Context, vendor string) (int64, bool, error) {
	vendor = domain.VendorKey(vendor)
	if vendor == "" {
		return 0, false, nil
	}
	var rate *int64
	err := s.Pool.QueryRow(ctx, `
		SELECT max(rate) FROM (
			SELECT greatest(
				COALESCE(round(input_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(output_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_read_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_write_usd_per_mtok * $2)::bigint, 0)) AS rate
			FROM gateway.prices WHERE vendor <> '' AND lower(vendor) = lower($1)
			UNION ALL
			SELECT greatest(
				COALESCE(round(input_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(output_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_read_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_write_usd_per_mtok * $2)::bigint, 0)) AS rate
			FROM gateway.price_tiers WHERE vendor <> '' AND lower(vendor) = lower($1)
		) rates WHERE rate > 0`, vendor, kernel.MicroUSDPerDollar).Scan(&rate)
	if err != nil {
		return 0, false, err
	}
	if rate == nil {
		return 0, false, nil
	}
	return *rate, true, nil
}

type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Record writes one attempt. cost_micro_usd stays NULL for non-metered plans, and
// owner_team_id when the account was not reached through a team.
func (s Store) Record(ctx context.Context, row domain.Entry) error {
	return insertLedger(ctx, s.Pool, row, "")
}

func insertLedger(ctx context.Context, db execer, row domain.Entry, holdID string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO gateway.ledger (
			route, link_position, provider_account_id, model, plan_kind, failure_class,
			user_id, agent_id, run_id, parent_run_id,
			input_tokens, output_tokens, cache_read, cache_write, cache_write_1h, reasoning,
			usage_arrived, cost_micro_usd, estimated, source, source_id, automation_id,
			latency_ms, charged_to_limit, price_source, price_version, owner_team_id, hold_id, list_cost_micro_usd, at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16,
			$17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, COALESCE($30, now())
		)`,
		row.Route, row.LinkPosition, uuidOrNil(row.AccountID), row.Model, row.PlanKind, string(row.Class),
		uuidOrNil(row.Principal.User), uuidOrNil(row.Principal.Agent),
		uuidOrNil(row.Principal.Run), uuidOrNil(row.Principal.Parent),
		row.Usage.Input, row.Usage.Output, row.Usage.CacheRead, row.Usage.CacheWrite, row.Usage.CacheWrite1h, row.Usage.Reasoning,
		row.Usage.Arrived, microOrNil(row.CostMicroUSD), row.Estimated, row.Principal.Source,
		uuidOrNil(row.Principal.SourceID), uuidOrNil(row.Principal.AutomationID), intOrNil(row.LatencyMS), row.ChargedToLimit,
		nullIfEmpty(row.PriceSource), nullIfEmpty(row.PriceVersion), uuidOrNil(row.OwnerTeamID), uuidOrNil(holdID), microOrNil(row.ListCostMicroUSD), timeOrNil(row.At),
	)
	return err
}

func timeOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
