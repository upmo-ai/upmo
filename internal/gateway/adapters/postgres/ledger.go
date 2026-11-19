package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/kernel"
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

// Price looks up one catalog row. Missing is not an error.
func (s Store) Price(ctx context.Context, model string) (domain.Price, bool, error) {
	var row struct {
		Version       string
		Provider      string
		Input         *int64
		Output        *int64
		CacheRead     *int64
		CacheWrite    *int64
		ContextWindow int
	}
	err := s.Pool.QueryRow(ctx, `
		SELECT version, provider,
			round(input_usd_per_mtok * $2)::bigint,
			round(output_usd_per_mtok * $2)::bigint,
			round(cache_read_usd_per_mtok * $2)::bigint,
			round(cache_write_usd_per_mtok * $2)::bigint,
			context_window
		FROM gateway.prices
		WHERE model = $1`, model, kernel.MicroUSDPerDollar).Scan(
		&row.Version, &row.Provider, &row.Input, &row.Output, &row.CacheRead, &row.CacheWrite, &row.ContextWindow,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Price{}, false, nil
		}
		return domain.Price{}, false, err
	}
	return domain.Price{
		Version:                   row.Version,
		Provider:                  row.Provider,
		InputMicroUSDPerMTok:      row.Input,
		OutputMicroUSDPerMTok:     row.Output,
		CacheReadMicroUSDPerMTok:  row.CacheRead,
		CacheWriteMicroUSDPerMTok: row.CacheWrite,
		ContextWindow:             row.ContextWindow,
	}, true, nil
}

// HighestRate returns the largest per-million-token catalog rate for provider.
func (s Store) HighestRate(ctx context.Context, provider string) (int64, bool, error) {
	var rate *int64
	err := s.Pool.QueryRow(ctx, `
		SELECT max(rate) FROM (
			SELECT greatest(
				COALESCE(round(input_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(output_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_read_usd_per_mtok * $2)::bigint, 0),
				COALESCE(round(cache_write_usd_per_mtok * $2)::bigint, 0)
			) AS rate
			FROM gateway.prices WHERE provider = $1
		) AS rates WHERE rate > 0`, provider, kernel.MicroUSDPerDollar).Scan(&rate)
	if err != nil {
		return 0, false, err
	}
	if rate == nil {
		return 0, false, nil
	}
	return *rate, true, nil
}

// Record writes one attempt. cost_micro_usd stays NULL for non-metered plans, and
// owner_team_id when the account was not reached through a team.
func (s Store) Record(ctx context.Context, row domain.Entry) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO gateway.ledger (
			route, link_position, provider_account_id, model, plan_kind, failure_class,
			user_id, agent_id, run_id, parent_run_id,
			input_tokens, output_tokens, cache_read, cache_write, reasoning,
			usage_arrived, cost_micro_usd, estimated, source, source_id, automation_id,
			latency_ms, charged_to_limit, price_source, price_version, owner_team_id
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26
		)`,
		row.Route, row.LinkPosition, uuidOrNil(row.AccountID), row.Model, row.PlanKind, string(row.Class),
		uuidOrNil(row.Principal.User), uuidOrNil(row.Principal.Agent),
		uuidOrNil(row.Principal.Run), uuidOrNil(row.Principal.Parent),
		row.Usage.Input, row.Usage.Output, row.Usage.CacheRead, row.Usage.CacheWrite, row.Usage.Reasoning,
		row.Usage.Arrived, microOrNil(row.CostMicroUSD), row.Estimated, row.Principal.Source,
		uuidOrNil(row.Principal.SourceID), uuidOrNil(row.Principal.AutomationID), intOrNil(row.LatencyMS), row.ChargedToLimit,
		nullIfEmpty(row.PriceSource), nullIfEmpty(row.PriceVersion), uuidOrNil(row.OwnerTeamID),
	)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
