//go:build integration

package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

type reserveObservingChatter struct {
	pool  *pgxpool.Pool
	want  int64
	usage domain.Usage
}

func (c reserveObservingChatter) Chat(ctx context.Context, attempt app.Attempt, _ domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	var held int64
	if err := c.pool.QueryRow(ctx, `SELECT held_micro_usd FROM gateway.usage_spend WHERE kind='workspace'`).Scan(&held); err != nil {
		return domain.ChatResult{}, domain.ClassOK, err
	}
	if held < c.want {
		return domain.ChatResult{}, domain.ClassOK, context.DeadlineExceeded
	}
	return domain.ChatResult{Text: "ok", Model: attempt.Model, Usage: c.usage}, domain.ClassOK, nil
}

func TestFeature_UPM388_DispatchReserveCoversTieredOneHourCacheCall(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'e'))
	store := postgres.Store{Pool: r.owner}
	const version = "upm388-reserve-tier-v1"
	if err := store.ReplaceCatalog(ctx, version, []domain.CatalogRow{{
		Vendor: "openai", Model: "reserve-tier-call", Version: version,
		Price: domain.Price{
			InputMicroUSDPerMTok: ptrInt64(1_000_000), OutputMicroUSDPerMTok: ptrInt64(1_000_000),
			CacheWriteMicroUSDPerMTok: ptrInt64(2_000_000),
			Tiers: []domain.PriceTier{{ContextMinTokens: 200_000, Price: domain.Price{
				InputMicroUSDPerMTok: ptrInt64(15_000_000), OutputMicroUSDPerMTok: ptrInt64(2_000_000),
				CacheWriteMicroUSDPerMTok: ptrInt64(20_000_000),
			}}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	accountID := insertProviderAndRoute(t, r, "r-upm388-reserve-tier", "openai", "Reserve Tier", "ref_reserve_tier", "reserve-tier-call", true)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET visibility='everyone' WHERE id=$1::uuid`, accountID); err != nil {
		t.Fatal(err)
	}
	// 804 KB estimates 201,000 input tokens. HighestRate is 2x the tier input
	// rate, so this exact monthly limit admits one full hold and nothing more.
	wantHold := domain.ReserveEstimate(domain.PlanMetered, 30_000_000, 804_000, 0)
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits(kind,amount_micro_usd) VALUES('workspace',$1)`, wantHold); err != nil {
		t.Fatal(err)
	}
	chatter := reserveObservingChatter{pool: r.owner, want: wantHold, usage: domain.Usage{Arrived: true, Input: 200_001, CacheWrite: 1_000, CacheWrite1h: 1_000}}
	dispatch := app.Dispatch{
		Routes: store, Teams: store, Prices: store, Ledger: store, Limits: store,
		Secrets: staticSecrets{"ref_reserve_tier": "secret"},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter},
	}
	result, err := dispatch.ChatCall(ctx, "r-upm388-reserve-tier", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: strings.Repeat("x", 804_000)}}})
	if err != nil {
		t.Fatalf("ChatCall: reservation did not cover call: %v", err)
	}
	if result.CostMicroUSD == nil || *result.CostMicroUSD > wantHold {
		t.Fatalf("call cost=%v, want <= reserved hold %d", result.CostMicroUSD, wantHold)
	}
	var spent, held int64
	if err := r.owner.QueryRow(ctx, `SELECT spent_micro_usd,held_micro_usd FROM gateway.usage_spend WHERE kind='workspace'`).Scan(&spent, &held); err != nil {
		t.Fatal(err)
	}
	if spent > wantHold || held != 0 {
		t.Fatalf("after call spent=%d held=%d, limit/hold=%d/0", spent, held, wantHold)
	}
}
