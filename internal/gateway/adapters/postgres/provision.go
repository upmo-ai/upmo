package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// ErrHouseProviderConflict reports a name collision with a non-house shared provider.
var ErrHouseProviderConflict = errors.New("gateway: shared provider with that name already exists")

// HouseProviderInput is one managed house provider account to upsert.
type HouseProviderInput struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Adapter  string   `json:"adapter"`
	PlanKind string   `json:"plan_kind"`
	BaseURL  string   `json:"base_url"`
	APIKey   string   `json:"api_key"`
	Models   []string `json:"models"`
}

// HouseProvider is the provisioned house provider account (never carrying secrets).
type HouseProvider struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Provider   string   `json:"provider"`
	Adapter    string   `json:"adapter"`
	PlanKind   string   `json:"plan_kind"`
	BaseURL    string   `json:"base_url,omitempty"`
	Status     string   `json:"status"`
	Models     []string `json:"models"`
	Visibility string   `json:"visibility"`
	House      bool     `json:"house"`
	HasKey     bool     `json:"has_key"`
}

// UsageTotals holds aggregated monthly token and micro-USD totals.
type UsageTotals struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CostMicroUSD     int64 `json:"cost_micro_usd"`
	UnknownCostCount int64 `json:"unknown_cost_count"`
}

// UsageRow holds monthly token and micro-USD totals for one (model, kind) bucket.
type UsageRow struct {
	Model            string `json:"model"`
	Kind             string `json:"kind"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	CostMicroUSD     int64  `json:"cost_micro_usd"`
	UnknownCostCount int64  `json:"unknown_cost_count"`
}

// MonthlyUsage is the anonymized workspace usage summary for one UTC month.
type MonthlyUsage struct {
	Month     string      `json:"month"`
	Totals    UsageTotals `json:"totals"`
	Breakdown []UsageRow  `json:"breakdown"`
}

type cleanHouseProvider struct {
	name     string
	provider string
	adapter  string
	planKind string
	baseURL  string
	apiKey   string
	models   []string
}

func validateHouseProviders(inputs []HouseProviderInput, knownAdapter func(string) bool) ([]cleanHouseProvider, error) {
	if len(inputs) == 0 {
		return nil, domain.Invalid("Provide at least one provider account.")
	}
	seen := make(map[string]bool, len(inputs))
	out := make([]cleanHouseProvider, 0, len(inputs))
	for _, in := range inputs {
		name, err := domain.CleanName(in.Name)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, domain.Invalid("Duplicate provider account name in request.")
		}
		seen[name] = true

		adapter := strings.TrimSpace(in.Adapter)
		if knownAdapter == nil || !knownAdapter(adapter) {
			return nil, domain.Invalid("Choose a supported adapter.")
		}
		providerRaw := strings.TrimSpace(in.Provider)
		if providerRaw == "" {
			providerRaw = adapter
		}
		provider, err := domain.CleanLabel(providerRaw)
		if err != nil {
			return nil, err
		}
		planKind, err := domain.CleanPlan(strings.TrimSpace(in.PlanKind))
		if err != nil {
			return nil, err
		}
		baseURL, err := domain.CleanBaseURL(planKind, in.BaseURL)
		if err != nil {
			return nil, err
		}
		models, err := domain.CleanModels(in.Models)
		if err != nil {
			return nil, err
		}
		if err := domain.CheckScripted(adapter, models); err != nil {
			return nil, err
		}
		apiKey, err := domain.CleanKey(in.APIKey)
		if err != nil {
			return nil, err
		}
		out = append(out, cleanHouseProvider{
			name:     name,
			provider: provider,
			adapter:  adapter,
			planKind: planKind,
			baseURL:  baseURL,
			apiKey:   apiKey,
			models:   models,
		})
	}
	return out, nil
}

// UpsertHouseProviders upserts managed house provider accounts (house=true,
// visibility='everyone') using the owner pool and seals credentials with keys.
func UpsertHouseProviders(
	ctx context.Context,
	owner *pgxpool.Pool,
	keys domain.Keyring,
	knownAdapter func(string) bool,
	inputs []HouseProviderInput,
	keyID string,
) ([]HouseProvider, error) {
	cleaned, err := validateHouseProviders(inputs, knownAdapter)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		return nil, fmt.Errorf("gateway: no database")
	}

	tx, err := owner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out := make([]HouseProvider, 0, len(cleaned))
	adaptersSeen := make([]string, 0, len(cleaned))
	plansSeen := make([]string, 0, len(cleaned))
	var targetIDs []string

	for _, item := range cleaned {
		var (
			id          string
			house       bool
			credRef     string
			prevAdapter string
			prevBaseURL string
			status      string
			hasCred     bool
		)
		err := tx.QueryRow(ctx, `
			SELECT a.id::text, a.house, a.credential_ref, a.adapter, a.base_url, a.status,
			       EXISTS (SELECT 1 FROM gateway.credentials AS c WHERE c.ref = a.credential_ref)
			FROM gateway.provider_accounts AS a
			WHERE a.name = $1 AND a.visibility <> 'private'
			FOR UPDATE OF a`, item.name).
			Scan(&id, &house, &credRef, &prevAdapter, &prevBaseURL, &status, &hasCred)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		isNew := errors.Is(err, pgx.ErrNoRows)
		if !isNew && !house {
			return nil, ErrHouseProviderConflict
		}
		if err := domain.NeedKey(item.planKind, item.apiKey, !isNew && hasCred); err != nil {
			return nil, err
		}
		if isNew {
			credRef, err = newHouseCredentialRef()
			if err != nil {
				return nil, err
			}
			status = domain.StatusNotConnected
			err = tx.QueryRow(ctx, `
				INSERT INTO gateway.provider_accounts
					(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility, team_ids)
				VALUES (NULL, $1, $2, $3, $4, $5, true, $6, $7, 'everyone', '{}'::uuid[])
				RETURNING id::text`,
				item.provider, item.name, item.adapter, credRef, item.planKind, item.baseURL, status).Scan(&id)
			if err != nil {
				return nil, mapWrite(err)
			}
		} else {
			reset := item.apiKey != "" || prevAdapter != item.adapter || prevBaseURL != item.baseURL
			if reset {
				status = domain.StatusNotConnected
			}
			if _, err := tx.Exec(ctx, `
				UPDATE gateway.provider_accounts
				SET provider = $2,
				    adapter = $3,
				    plan_kind = $4,
				    base_url = $5,
				    status = $6,
				    checked_at = CASE WHEN $7 THEN NULL ELSE checked_at END,
				    visibility = 'everyone',
				    team_ids = '{}'::uuid[],
				    updated_at = now()
				WHERE id = $1::uuid`,
				id, item.provider, item.adapter, item.planKind, item.baseURL, status, reset); err != nil {
				return nil, mapWrite(err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM gateway.provider_models WHERE account_id = $1::uuid`, id); err != nil {
				return nil, err
			}
		}

		if err := putModels(ctx, tx, id, item.models); err != nil {
			return nil, err
		}
		if item.apiKey != "" {
			if !keys.Ready() {
				return nil, domain.ErrNoMasterKey
			}
			sealed, err := keys.Seal(credRef, item.apiKey)
			if err != nil {
				return nil, err
			}
			if err := putSealed(ctx, tx, credRef, sealed); err != nil {
				return nil, err
			}
			hasCred = true
		}

		targetIDs = append(targetIDs, id)
		if !slices.Contains(adaptersSeen, item.adapter) {
			adaptersSeen = append(adaptersSeen, item.adapter)
		}
		if !slices.Contains(plansSeen, item.planKind) {
			plansSeen = append(plansSeen, item.planKind)
		}
		models := item.models
		if models == nil {
			models = []string{}
		}
		out = append(out, HouseProvider{
			ID:         id,
			Name:       item.name,
			Provider:   item.provider,
			Adapter:    item.adapter,
			PlanKind:   item.planKind,
			BaseURL:    item.baseURL,
			Status:     status,
			Models:     models,
			Visibility: domain.VisibilityEveryone,
			House:      true,
			HasKey:     hasCred,
		})
	}

	sort.Strings(adaptersSeen)
	sort.Strings(plansSeen)
	targetID := "house"
	if len(targetIDs) == 1 {
		targetID = targetIDs[0]
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionProviderUpsert,
		Outcome:    audit.Success,
		TargetType: "provider",
		TargetID:   targetID,
		Changes: audit.Changes{
			Fields: []string{"adapters", "count", "key_id", "plan_kinds", "visibility"},
			Values: map[string]audit.Change{
				"key_id":     {To: keyID},
				"count":      {To: len(out)},
				"adapters":   {To: adaptersSeen},
				"plan_kinds": {To: plansSeen},
				"visibility": {To: domain.VisibilityEveryone},
			},
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func newHouseCredentialRef() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("gateway: credential ref: %w", err)
	}
	return "house_" + hex.EncodeToString(raw[:]), nil
}

// QueryMonthlyUsage aggregates gateway.ledger for [start, end) by model and
// kind (task, chat, direct) without returning any user_id or content fields.
func QueryMonthlyUsage(ctx context.Context, owner *pgxpool.Pool, month string, start, end time.Time) (MonthlyUsage, error) {
	if owner == nil {
		return MonthlyUsage{}, fmt.Errorf("gateway: no database")
	}
	rows, err := owner.Query(ctx, `
		SELECT
			model,
			CASE
				WHEN parent_run_id IS NOT NULL OR run_id IS NOT NULL THEN 'task'
				WHEN agent_id IS NOT NULL THEN 'chat'
				ELSE 'direct'
			END AS kind,
			COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
			COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
			COALESCE(SUM(cache_read), 0)::bigint AS cache_read_tokens,
			COALESCE(SUM(cache_write), 0)::bigint AS cache_write_tokens,
			COALESCE(SUM(reasoning), 0)::bigint AS reasoning_tokens,
			COALESCE(SUM(COALESCE(cost_micro_usd, 0)), 0)::bigint AS cost_micro_usd,
			COUNT(*) FILTER (WHERE estimated)::bigint AS unknown_cost_count
		FROM gateway.ledger
		WHERE at >= $1 AND at < $2
		GROUP BY 1, 2
		ORDER BY 1, 2`, start, end)
	if err != nil {
		return MonthlyUsage{}, err
	}
	defer rows.Close()

	res := MonthlyUsage{
		Month:     month,
		Breakdown: []UsageRow{},
	}
	for rows.Next() {
		var row UsageRow
		if err := rows.Scan(
			&row.Model,
			&row.Kind,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CacheReadTokens,
			&row.CacheWriteTokens,
			&row.ReasoningTokens,
			&row.CostMicroUSD,
			&row.UnknownCostCount,
		); err != nil {
			return MonthlyUsage{}, err
		}
		row.TotalTokens = row.InputTokens + row.OutputTokens + row.CacheReadTokens + row.CacheWriteTokens + row.ReasoningTokens
		res.Totals.InputTokens += row.InputTokens
		res.Totals.OutputTokens += row.OutputTokens
		res.Totals.CacheReadTokens += row.CacheReadTokens
		res.Totals.CacheWriteTokens += row.CacheWriteTokens
		res.Totals.ReasoningTokens += row.ReasoningTokens
		res.Totals.TotalTokens += row.TotalTokens
		res.Totals.CostMicroUSD += row.CostMicroUSD
		res.Totals.UnknownCostCount += row.UnknownCostCount
		res.Breakdown = append(res.Breakdown, row)
	}
	if err := rows.Err(); err != nil {
		return MonthlyUsage{}, err
	}
	return res, nil
}
