package domain

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/kernel"
)

func TestResolveCallCost_PrecedenceAndPartialOverride(t *testing.T) {
	reported := int64(99_000)
	usage := Usage{
		Arrived:              true,
		Input:                1_000_000,
		Output:               500_000,
		CacheRead:            1_000_000,
		ReportedCostMicroUSD: &reported,
	}
	provList := &Price{
		InputMicroUSDPerMTok:     microRate(4_000_000),
		OutputMicroUSDPerMTok:    microRate(12_000_000),
		CacheReadMicroUSDPerMTok: microRate(400_000),
	}
	catalog := Price{
		Version:                  "2026-11-19",
		InputMicroUSDPerMTok:     microRate(3_000_000),
		OutputMicroUSDPerMTok:    microRate(10_000_000),
		CacheReadMicroUSDPerMTok: microRate(300_000),
	}

	// 1. Override on a non-zero dimension beats reported cost; missing override
	// dimensions fall back to provider_list then models_dev; source is "override".
	partialOv := &PriceOverride{
		Model:                "anthropic/claude-3.7-sonnet",
		InputMicroUSDPerMTok: microRate(2_000_000),
	}
	res := ResolveCallCost(CallPriceInput{
		PlanKind:     PlanMetered,
		Model:        "anthropic/claude-3.7-sonnet",
		Usage:        usage,
		Override:     partialOv,
		ProviderList: provList,
		Catalog:      catalog,
		CatalogFound: true,
	})
	// 1M * 2 + 0.5M * 12 + 1M * 0.4 = 8.4M micro-USD
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 8_400_000 || res.Estimated || res.Source != "override" {
		t.Fatalf("partial override = %+v, want 8_400_000 override", res)
	}

	// 2. Override only on an unused dimension (CacheWrite=0) does NOT override reported cost.
	unusedOv := &PriceOverride{
		Model:                     "anthropic/claude-3.7-sonnet",
		CacheWriteMicroUSDPerMTok: microRate(5_000_000),
	}
	res = ResolveCallCost(CallPriceInput{
		PlanKind:     PlanMetered,
		Model:        "anthropic/claude-3.7-sonnet",
		Usage:        usage,
		Override:     unusedOv,
		ProviderList: provList,
		Catalog:      catalog,
		CatalogFound: true,
	})
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 99_000 || res.Estimated || res.Source != "provider" {
		t.Fatalf("unused override = %+v, want 99_000 provider", res)
	}

	// 3. Partial override with an unpriced used dimension falls through to reported cost.
	res = ResolveCallCost(CallPriceInput{
		PlanKind: PlanMetered,
		Model:    "unpriced",
		Usage:    usage,
		Override: partialOv,
	})
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 99_000 || res.Estimated || res.Source != "provider" {
		t.Fatalf("partial override missing dim = %+v, want 99_000 provider", res)
	}

	// 4. Without override or reported cost, provider_list beats models_dev.
	noReported := usage
	noReported.ReportedCostMicroUSD = nil
	res = ResolveCallCost(CallPriceInput{
		PlanKind:     PlanMetered,
		Model:        "anthropic/claude-3.7-sonnet",
		Usage:        noReported,
		ProviderList: provList,
		Catalog:      catalog,
		CatalogFound: true,
	})
	// 1M * 4 + 0.5M * 12 + 1M * 0.4 = 10.4M micro-USD
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 10_400_000 || res.Estimated || res.Source != "provider_list" {
		t.Fatalf("provider_list = %+v, want 10_400_000 provider_list", res)
	}

	// 5. Partial provider_list + models_dev still names highest contributing source ("provider_list").
	partialList := &Price{InputMicroUSDPerMTok: microRate(4_000_000)}
	res = ResolveCallCost(CallPriceInput{
		PlanKind:     PlanMetered,
		Model:        "anthropic/claude-3.7-sonnet",
		Usage:        noReported,
		ProviderList: partialList,
		Catalog:      catalog,
		CatalogFound: true,
	})
	// 1M * 4 + 0.5M * 10 + 1M * 0.3 = 9.3M
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 9_300_000 || res.Estimated || res.Source != "provider_list" {
		t.Fatalf("partial provider_list = %+v, want 9_300_000 provider_list", res)
	}

	// 6. models_dev alone.
	res = ResolveCallCost(CallPriceInput{
		PlanKind:     PlanMetered,
		Model:        "gpt-4o",
		Usage:        noReported,
		Catalog:      catalog,
		CatalogFound: true,
	})
	// 1M * 3 + 0.5M * 10 + 1M * 0.3 = 8.3M
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 8_300_000 || res.Estimated || res.Source != "models_dev" {
		t.Fatalf("models_dev = %+v, want 8_300_000 models_dev", res)
	}

	// 7. Missing everything (5c: nothing known) -> unknown, nil cost, not estimated.
	res = ResolveCallCost(CallPriceInput{
		PlanKind: PlanMetered,
		Model:    "unknown",
		Usage:    noReported,
	})
	if res.CostMicroUSD != nil || res.Estimated || res.Source != "unknown" {
		t.Fatalf("case 5c unknown = %+v, want nil cost source=unknown estimated=false", res)
	}

	// 8. 5a: Cross-provider ('') row alone -> estimated with source "estimate".
	cross := &Price{
		InputMicroUSDPerMTok:     microRate(5_000_000),
		OutputMicroUSDPerMTok:    microRate(15_000_000),
		CacheReadMicroUSDPerMTok: microRate(500_000),
	}
	res = ResolveCallCost(CallPriceInput{
		PlanKind:      PlanMetered,
		Model:         "shared-model",
		Usage:         noReported,
		CrossProvider: cross,
	})
	// 1M * 5 + 0.5M * 15 + 1M * 0.5 = 13M micro-USD
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 13_000_000 || !res.Estimated || res.Source != "estimate" {
		t.Fatalf("5a cross-provider = %+v, want 13_000_000 estimate", res)
	}

	// 9. Mixed accurate (models_dev on input+output) + 5a (CrossProvider on cache_read) -> whole call estimated with source "estimate".
	partialCat := Price{
		Version:               "2026-11-19",
		InputMicroUSDPerMTok:  microRate(3_000_000),
		OutputMicroUSDPerMTok: microRate(10_000_000),
	}
	res = ResolveCallCost(CallPriceInput{
		PlanKind:      PlanMetered,
		Model:         "shared-model",
		Usage:         noReported,
		Catalog:       partialCat,
		CatalogFound:  true,
		CrossProvider: cross,
	})
	// 1M * 3 + 0.5M * 10 + 1M * 0.5 = 8.5M micro-USD
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 8_500_000 || !res.Estimated || res.Source != "estimate" {
		t.Fatalf("mixed models_dev + 5a = %+v, want 8_500_000 estimate", res)
	}

	// 10. 5b: Known vendor highest rate -> estimated at vendor rate.
	res = ResolveCallCost(CallPriceInput{
		PlanKind:          PlanMetered,
		Model:             "unlisted-on-known-vendor",
		Usage:             noReported,
		EstimateRate:      20_000_000,
		EstimateRateKnown: true,
	})
	// 2.5M tokens * 20M / 1M = 50_000_000 micro-USD
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 50_000_000 || !res.Estimated || res.Source != "estimate" {
		t.Fatalf("5b vendor highest rate = %+v, want 50_000_000 estimate", res)
	}

	// 11. Mixed 4/5a + 5b on missing dimension -> per-field precedence, whole call estimated.
	res = ResolveCallCost(CallPriceInput{
		PlanKind:          PlanMetered,
		Model:             "partial-cross",
		Usage:             noReported,
		Catalog:           partialCat,
		CatalogFound:      true,
		EstimateRate:      20_000_000,
		EstimateRateKnown: true,
	})
	// 1M * 3 + 0.5M * 10 + 1M * 20 = 28M micro-USD
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 28_000_000 || !res.Estimated || res.Source != "estimate" {
		t.Fatalf("mixed models_dev + 5b = %+v, want 28_000_000 estimate", res)
	}

	// 11b. Mixed partial catalog/override/5a where one used dimension has NO real price (no 1-4, 5a, or 5b):
	// whole call is unknown and no part is billed at a made-up rate.
	res = ResolveCallCost(CallPriceInput{
		PlanKind:      PlanMetered,
		Model:         "partial-unknown",
		Usage:         noReported, // uses Input, Output, CacheRead
		Override:      partialOv,
		Catalog:       partialCat, // missing CacheRead
		CatalogFound:  true,
		CrossProvider: &Price{InputMicroUSDPerMTok: microRate(5_000_000)}, // also missing CacheRead
	})
	if res.CostMicroUSD != nil || res.Estimated || res.Source != "unknown" {
		t.Fatalf("mixed missing dimension without 5b = %+v, want nil unknown", res)
	}

	// 11c. No-usage call on unknown vendor (no 5b) -> unknown, nil cost.
	res = ResolveCallCost(CallPriceInput{
		PlanKind:   PlanMetered,
		Model:      "unknown",
		InputBytes: 400,
	})
	if res.CostMicroUSD != nil || res.Estimated || res.Source != "unknown" {
		t.Fatalf("no-usage unknown = %+v, want nil unknown", res)
	}

	// 12. Reported cost (2) beats 5a even when partial override is missing a used dimension.
	res = ResolveCallCost(CallPriceInput{
		PlanKind:      PlanMetered,
		Model:         "shared-model",
		Usage:         usage,
		Override:      partialOv,
		CrossProvider: cross,
	})
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 99_000 || res.Estimated || res.Source != "provider" {
		t.Fatalf("reported cost vs 5a = %+v, want 99_000 provider", res)
	}

	// 13. Non-metered -> nil cost, empty source.
	res = ResolveCallCost(CallPriceInput{
		PlanKind: PlanSubscription,
		Model:    "gpt-4o",
		Usage:    usage,
		Override: partialOv,
	})
	if res.CostMicroUSD != nil || res.Estimated || res.Source != "" {
		t.Fatalf("subscription = %+v", res)
	}
}

func TestResolveCallCost_EstimateOverflowCapsAtMaximum(t *testing.T) {
	tests := []struct {
		name        string
		usage       Usage
		inputBytes  int
		outputBytes int
		want        int64
	}{
		{name: "single field overflow", usage: Usage{Arrived: true, Input: math.MaxInt}, want: MaxMicroUSD},
		{name: "token sum overflow", usage: Usage{Arrived: true, Input: math.MaxInt, Output: 1}, want: MaxMicroUSD},
		{name: "reasoning already included in output", usage: Usage{Arrived: true, Output: 100, Reasoning: 40}, want: 7_500},
		{name: "no usage input bytes overflow", inputBytes: math.MaxInt, want: MaxMicroUSD},
		{name: "no usage byte sum overflow", inputBytes: math.MaxInt, outputBytes: math.MaxInt, want: MaxMicroUSD},
		{name: "no usage byte sum and output overflow", usage: Usage{Output: math.MaxInt}, inputBytes: math.MaxInt, outputBytes: math.MaxInt, want: MaxMicroUSD},
		{name: "no usage negative partial output ignored", usage: Usage{Output: -50}, inputBytes: 400, want: 7_500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ResolveCallCost(CallPriceInput{
				PlanKind:          PlanMetered,
				Usage:             tt.usage,
				EstimateRate:      75_000_000,
				EstimateRateKnown: true,
				InputBytes:        tt.inputBytes,
				OutputBytes:       tt.outputBytes,
			})
			if res.CostMicroUSD == nil || *res.CostMicroUSD != tt.want || !res.Estimated || res.Source != "estimate" {
				t.Fatalf("estimate = %+v, want %d estimate", res, tt.want)
			}
		})
	}

	// Overflow on input must not hide a missing rate on output: whole call must be unknown.
	overflowMissingOut := ResolveCallCost(CallPriceInput{
		PlanKind: PlanMetered,
		Usage:    Usage{Arrived: true, Input: math.MaxInt, Output: 100},
		Override: &PriceOverride{Model: "m", InputMicroUSDPerMTok: microRate(10_000_000)},
	})
	if overflowMissingOut.CostMicroUSD != nil || overflowMissingOut.Estimated || overflowMissingOut.Source != "unknown" {
		t.Fatalf("overflow with missing output rate = %+v, want nil unknown", overflowMissingOut)
	}

	// Two million tokens at MaxMicroUSD per million on an estimate path must cap at MaxMicroUSD ($10,000), not $20,000.
	for name, in := range map[string]CallPriceInput{
		"5a cross-provider estimate": {
			PlanKind:      PlanMetered,
			Usage:         Usage{Arrived: true, Input: 2_000_000},
			CrossProvider: &Price{InputMicroUSDPerMTok: microRate(MaxMicroUSD)},
		},
		"5b vendor-highest estimate": {
			PlanKind:          PlanMetered,
			Usage:             Usage{Arrived: true, Input: 2_000_000},
			EstimateRate:      MaxMicroUSD,
			EstimateRateKnown: true,
		},
		"no-usage override estimate": {
			PlanKind:   PlanMetered,
			InputBytes: 8_000_000, // 2,000,000 tokens
			Override:   &PriceOverride{Model: "m", InputMicroUSDPerMTok: microRate(MaxMicroUSD)},
		},
	} {
		got := ResolveCallCost(in)
		if got.CostMicroUSD == nil || *got.CostMicroUSD != MaxMicroUSD || !got.Estimated || got.Source != "estimate" {
			t.Fatalf("%s = %+v, want capped %d estimate", name, got, MaxMicroUSD)
		}
	}

	// Missing usage with complete overrides and no vendor-wide rate estimates from bytes and rates.
	noUsageCompleteOv := ResolveCallCost(CallPriceInput{
		PlanKind: PlanMetered,
		Model:    "custom",
		Override: &PriceOverride{
			Model:                 "custom",
			InputMicroUSDPerMTok:  microRate(4_000_000),
			OutputMicroUSDPerMTok: microRate(16_000_000),
		},
		InputBytes:  400, // 100 tokens * $4/MTok = 400
		OutputBytes: 20,  // 5 tokens * $16/MTok = 80
	})
	if noUsageCompleteOv.CostMicroUSD == nil || *noUsageCompleteOv.CostMicroUSD != 480 || !noUsageCompleteOv.Estimated || noUsageCompleteOv.Source != "estimate" {
		t.Fatalf("no usage complete overrides = %+v, want 480 estimate", noUsageCompleteOv)
	}

	// Missing usage with output bytes but missing output rate and no 5b stays unknown.
	noUsageMissingOutRate := ResolveCallCost(CallPriceInput{
		PlanKind:    PlanMetered,
		Model:       "custom",
		Override:    &PriceOverride{Model: "custom", InputMicroUSDPerMTok: microRate(4_000_000)},
		InputBytes:  400,
		OutputBytes: 20,
	})
	if noUsageMissingOutRate.CostMicroUSD != nil || noUsageMissingOutRate.Estimated || noUsageMissingOutRate.Source != "unknown" {
		t.Fatalf("no usage missing output rate = %+v, want nil unknown", noUsageMissingOutRate)
	}
}

func TestResolveCallCost_ZeroAndInvalidReportedCost(t *testing.T) {
	catalog := Price{
		Version:               "v1",
		InputMicroUSDPerMTok:  microRate(3_000_000),
		OutputMicroUSDPerMTok: microRate(15_000_000),
	}
	zero := int64(0)
	neg := int64(-5)
	absurd := MaxMicroUSD + 1

	// 1. Zero reported cost on :free model is accepted as "provider".
	res := ResolveCallCost(CallPriceInput{
		PlanKind:        PlanMetered,
		Model:           "meta-llama/llama-3.3-70b-instruct:free",
		Usage:           Usage{Arrived: true, Input: 1000, Output: 500, ReportedCostMicroUSD: &zero},
		Catalog:         catalog,
		CatalogFound:    true,
		ZeroCostAllowed: ZeroCostAllowed("meta-llama/llama-3.3-70b-instruct:free", nil),
	})
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 0 || res.Estimated || res.Source != "provider" {
		t.Fatalf(":free zero = %+v, want 0 provider", res)
	}

	// 2. Zero reported cost when provider list explicitly lists prompt=0, completion=0.
	freeList, ok := ParseSeenPricingMicroUSD(map[string]string{"prompt": "0", "completion": "0"})
	if !ok {
		t.Fatal("ParseSeenPricingMicroUSD failed for zero free list")
	}
	res = ResolveCallCost(CallPriceInput{
		PlanKind:        PlanMetered,
		Model:           "openrouter/free-model",
		Usage:           Usage{Arrived: true, Input: 1000, Output: 500, ReportedCostMicroUSD: &zero},
		ProviderList:    &freeList,
		Catalog:         catalog,
		CatalogFound:    true,
		ZeroCostAllowed: ZeroCostAllowed("openrouter/free-model", &freeList),
	})
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 0 || res.Estimated || res.Source != "provider" {
		t.Fatalf("free list zero = %+v, want 0 provider", res)
	}

	// 3. Unexpected zero, negative, or absurd reported cost on a paid model falls through to models_dev.
	for name, rc := range map[string]*int64{"zero": &zero, "negative": &neg, "absurd": &absurd, "nil": nil} {
		res := ResolveCallCost(CallPriceInput{
			PlanKind:        PlanMetered,
			Model:           "gpt-4o",
			Usage:           Usage{Arrived: true, Input: 1_000_000, Output: 1_000_000, ReportedCostMicroUSD: rc},
			Catalog:         catalog,
			CatalogFound:    true,
			ZeroCostAllowed: ZeroCostAllowed("gpt-4o", nil),
		})
		if res.CostMicroUSD == nil || *res.CostMicroUSD != 18_000_000 || res.Estimated || res.Source != "models_dev" {
			t.Fatalf("%s on paid model = %+v, want 18_000_000 models_dev", name, res)
		}
	}

	// 4. knownZero stays not_billed.
	res = ResolveCallCost(CallPriceInput{
		PlanKind:     PlanMetered,
		Model:        "gpt-4o",
		Usage:        Usage{ReportedCostMicroUSD: &absurd},
		Catalog:      catalog,
		CatalogFound: true,
		KnownZero:    true,
	})
	if res.CostMicroUSD == nil || *res.CostMicroUSD != 0 || res.Estimated || res.Source != "not_billed" {
		t.Fatalf("knownZero = %+v, want 0 not_billed", res)
	}
}

func TestParseDecimalMicroUSD_NoFloatRounding(t *testing.T) {
	usdCases := []struct {
		raw   string
		scale int64
		want  int64
		ok    bool
	}{
		{`"0.0000015"`, kernel.MicroUSDPerDollar, 2, true},
		{`"0.000001499999"`, kernel.MicroUSDPerDollar, 1, true},
		{`0.19`, kernel.MicroUSDPerDollar, 190_000, true},
		{`0.29`, kernel.MicroUSDPerDollar, 290_000, true},
		{`"1.005"`, kernel.MicroUSDPerDollar, 1_005_000, true},
		{`1e-6`, kernel.MicroUSDPerDollar, 1, true},
		{`5e-7`, kernel.MicroUSDPerDollar, 1, true},
		{`4.9e-7`, kernel.MicroUSDPerDollar, 0, true},
		{`"0"`, kernel.MicroUSDPerDollar, 0, true},
		{`"-0.01"`, kernel.MicroUSDPerDollar, 0, false},
		{`-1`, kernel.MicroUSDPerDollar, 0, false},
		{`"NaN"`, kernel.MicroUSDPerDollar, 0, false},
		{`"Infinity"`, kernel.MicroUSDPerDollar, 0, false},
		{`"abc"`, kernel.MicroUSDPerDollar, 0, false},
		{`null`, kernel.MicroUSDPerDollar, 0, false},
	}
	for _, tc := range usdCases {
		got, ok := ParseDecimalMicroUSD(json.RawMessage(tc.raw), tc.scale)
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseDecimalMicroUSD(%s) = (%d, %v), want (%d, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}

	tickCases := []struct {
		raw  string
		want int64
		ok   bool
	}{
		{`15000`, 2, true},
		{`14999`, 1, true},
		{`5000`, 1, true},
		{`4999`, 0, true},
		{`"25000"`, 3, true},
		{`0`, 0, true},
		{`-100`, 0, false},
		{`"NaN"`, 0, false},
	}
	for _, tc := range tickCases {
		got, ok := ParseTicksMicroUSD(json.RawMessage(tc.raw))
		if ok != tc.ok || got != tc.want {
			t.Errorf("ParseTicksMicroUSD(%s) = (%d, %v), want (%d, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}

	// OpenRouter account_models_seen.pricing rejects "-1" and parses valid entries.
	seenPrice, ok := ParseSeenPricingMicroUSD(map[string]string{
		"prompt":            "0.000003",
		"completion":        "-1",
		"input_cache_read":  "0.0000003",
		"input_cache_write": "NaN",
	})
	if !ok || seenPrice.InputMicroUSDPerMTok == nil || *seenPrice.InputMicroUSDPerMTok != 3_000_000 ||
		seenPrice.OutputMicroUSDPerMTok != nil ||
		seenPrice.CacheReadMicroUSDPerMTok == nil || *seenPrice.CacheReadMicroUSDPerMTok != 300_000 ||
		seenPrice.CacheWriteMicroUSDPerMTok != nil {
		t.Fatalf("ParseSeenPricingMicroUSD = %+v ok=%v", seenPrice, ok)
	}
}

func TestPriceOverrideValidationAndWarnings(t *testing.T) {
	if _, err := CleanPriceOverride(PriceOverride{Model: "gpt-4o"}); err == nil {
		t.Fatal("expected error for empty rate override")
	}
	if _, err := CleanPriceOverride(PriceOverride{Model: "gpt-4o", InputMicroUSDPerMTok: microRate(-1)}); err == nil {
		t.Fatal("expected error for negative rate")
	}
	if _, err := CleanPriceOverride(PriceOverride{Model: "gpt-4o", InputMicroUSDPerMTok: microRate(MaxMicroUSD + 1)}); err == nil {
		t.Fatal("expected error for absurd rate")
	}
	clean, err := CleanPriceOverride(PriceOverride{Model: "anthropic/claude-3.7-sonnet", InputMicroUSDPerMTok: microRate(1_200_000)})
	if err != nil || clean.Model != "anthropic/claude-3.7-sonnet" {
		t.Fatalf("CleanPriceOverride = %+v err=%v", clean, err)
	}

	prov := &Price{InputMicroUSDPerMTok: microRate(1_000_000), OutputMicroUSDPerMTok: microRate(0)}
	cat := &Price{InputMicroUSDPerMTok: microRate(5_000_000), OutputMicroUSDPerMTok: microRate(10_000_000), CacheReadMicroUSDPerMTok: microRate(1_000_000)}

	// Exactly 20% divergence is not warned; >20% is warned against provider_list first, else models_dev; 0 base is skipped.
	warnings := OverrideWarnings(&PriceOverride{
		Model:                    "m",
		InputMicroUSDPerMTok:     microRate(1_200_000), // exact +20% vs prov 1_000_000 -> no warning
		OutputMicroUSDPerMTok:    microRate(9_000_000), // prov output is 0 -> skipped
		CacheReadMicroUSDPerMTok: microRate(1_200_001), // >20% vs cat 1_000_000 -> warned
	}, prov, cat)
	if len(warnings) != 1 || warnings[0].Field != "cache_read_micro_usd_per_mtok" || warnings[0].BaseSource != SourceModelsDev {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestValidateCatalogChecks(t *testing.T) {
	// Build a valid minimal catalog with 10 vendors and 100 models.
	validRows := make([]CatalogRow, 0, 110)
	for i := range 100 {
		m := "model-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		v := ""
		if i < 10 {
			v = Vendors()[i].Key
		}
		if v != "" {
			validRows = append(validRows, CatalogRow{
				Vendor: v,
				Model:  m,
				Price:  Price{InputMicroUSDPerMTok: microRate(1_000_000), OutputMicroUSDPerMTok: microRate(2_000_000)},
			})
		}
		validRows = append(validRows, CatalogRow{
			Vendor: "",
			Model:  m,
			Price:  Price{InputMicroUSDPerMTok: microRate(1_000_000), OutputMicroUSDPerMTok: microRate(2_000_000)},
		})
	}
	if err := ValidateCatalog(validRows, 0); err != nil {
		t.Fatalf("valid catalog failed: %v", err)
	}
	if err := ValidateCatalog(validRows, len(validRows)); err != nil {
		t.Fatalf("same count failed: %v", err)
	}
	// >25% drop rejected.
	if err := ValidateCatalog(validRows, 200); err == nil || !strings.Contains(err.Error(), "25%") {
		t.Fatalf("expected >25%% drop error, got %v", err)
	}
	// <10 vendors rejected.
	fewVendors := make([]CatalogRow, 0, len(validRows))
	for _, r := range validRows {
		if r.Vendor == "" || r.Vendor == "anthropic" {
			fewVendors = append(fewVendors, r)
		}
	}
	if err := ValidateCatalog(fewVendors, 0); err == nil {
		t.Fatal("expected <10 vendors error")
	}
	// <100 models rejected.
	if err := ValidateCatalog(validRows[:20], 0); err == nil {
		t.Fatal("expected <100 models error")
	}
	// Negative / absurd rate rejected.
	badRate := append([]CatalogRow(nil), validRows...)
	badRate[0].Price.InputMicroUSDPerMTok = microRate(MaxMicroUSD + 1)
	if err := ValidateCatalog(badRate, 0); err == nil {
		t.Fatal("expected absurd rate error")
	}
}

func TestResolveCallCostContextTierBoundariesAndCacheReadContext(t *testing.T) {
	catalog := Price{
		InputMicroUSDPerMTok:     microRate(1_250_000),
		CacheReadMicroUSDPerMTok: microRate(125_000),
		Tiers: []PriceTier{{ContextMinTokens: 200_000, Price: Price{
			InputMicroUSDPerMTok:     microRate(2_500_000),
			CacheReadMicroUSDPerMTok: microRate(250_000),
		}}},
	}
	for _, tc := range []struct {
		name  string
		usage Usage
		want  int64
	}{
		{"threshold stays base", Usage{Arrived: true, Input: 200_000}, 250_000},
		{"one above switches", Usage{Arrived: true, Input: 200_001}, 500_003},
		{"cache read counts toward context", Usage{Arrived: true, Input: 100_000, CacheRead: 100_001}, 275_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: tc.usage, Catalog: catalog, CatalogFound: true})
			if got.CostMicroUSD == nil || *got.CostMicroUSD != tc.want || got.Source != "models_dev" || got.Estimated {
				t.Fatalf("got %+v, want exact models_dev %d", got, tc.want)
			}
		})
	}
}

func TestResolveCallCostTierAboveFlatOverridePrefersProviderCost(t *testing.T) {
	catalog := Price{InputMicroUSDPerMTok: microRate(2_000_000), Tiers: []PriceTier{{ContextMinTokens: 200_000, Price: Price{InputMicroUSDPerMTok: microRate(4_000_000)}}}}
	override := &PriceOverride{InputMicroUSDPerMTok: microRate(1_000_000)}
	reported := int64(55)
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, Input: 200_001, ReportedCostMicroUSD: &reported}, Override: override, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 55 || got.Estimated || got.Source != "provider" {
		t.Fatalf("got %+v, want provider reported cost", got)
	}
	got = ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, Input: 200_001}, Override: override, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 200_001 || !got.Estimated || got.Source != "estimate" {
		t.Fatalf("got %+v, want flat override estimate when provider cost is absent", got)
	}
}

func TestResolveCallCostProviderCostBeatsOneHourEstimate(t *testing.T) {
	reported := int64(55)
	catalog := Price{InputMicroUSDPerMTok: microRate(1_000_000)}
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, CacheWrite: 100, CacheWrite1h: 100, ReportedCostMicroUSD: &reported}, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != reported || got.Estimated || got.Source != "provider" {
		t.Fatalf("got %+v, want provider cost", got)
	}
}

func TestResolveCallCostAnthropicOneHourCacheWrite(t *testing.T) {
	catalog := Price{InputMicroUSDPerMTok: microRate(1_000_000), CacheWriteMicroUSDPerMTok: microRate(125_000)}
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, CacheWrite: 100, CacheWrite1h: 100}, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 200 || !got.Estimated || got.Source != "estimate" {
		t.Fatalf("got %+v, want 200 micro-USD estimate", got)
	}
}

func TestResolveCallCostCacheWriteWithoutBreakdownStaysExact(t *testing.T) {
	catalog := Price{CacheWriteMicroUSDPerMTok: microRate(125_000)}
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, CacheWrite: 100}, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 13 || got.Estimated || got.Source != "models_dev" {
		t.Fatalf("got %+v, want exact cache-write cost", got)
	}
}

func TestResolveCallCostTierUsesBaseForMissingDimensionAndPreservesUnknown(t *testing.T) {
	catalog := Price{
		InputMicroUSDPerMTok:  microRate(1_000_000),
		OutputMicroUSDPerMTok: microRate(3_000_000),
		Tiers:                 []PriceTier{{ContextMinTokens: 10, Price: Price{InputMicroUSDPerMTok: microRate(2_000_000)}}},
	}
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, Input: 100_000, Output: 100_000}, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 500_000 || got.Source != "models_dev" || got.Estimated {
		t.Fatalf("base dimension fallback = %+v", got)
	}
	catalog.OutputMicroUSDPerMTok = nil
	got = ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, Input: 100_000, Output: 100_000}, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD != nil || got.Source != "unknown" || got.Estimated {
		t.Fatalf("missing dimension = %+v, want unknown nil cost", got)
	}
}

func TestResolveCallCostTierCrossProviderAndVendorFallbackAreEstimates(t *testing.T) {
	usage := Usage{Arrived: true, Input: 20}
	cross := Price{InputMicroUSDPerMTok: microRate(3_000_000), Tiers: []PriceTier{{ContextMinTokens: 10, Price: Price{InputMicroUSDPerMTok: microRate(5_000_000)}}}}
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: usage, CrossProvider: &cross})
	if got.CostMicroUSD == nil || !got.Estimated || got.Source != "estimate" {
		t.Fatalf("cross-provider tier = %+v, want estimate", got)
	}
	catalog := Price{Tiers: []PriceTier{{ContextMinTokens: 10, Price: Price{InputMicroUSDPerMTok: microRate(5_000_000)}}}}
	fallback := int64(7_000_000)
	got = ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, Input: 10, Output: 10}, Catalog: catalog, CatalogFound: true, EstimateRate: fallback, EstimateRateKnown: true})
	if got.CostMicroUSD == nil || !got.Estimated || got.Source != "estimate" {
		t.Fatalf("fallback-filled tier = %+v, want estimate", got)
	}
	crossTier := Price{InputMicroUSDPerMTok: microRate(3_000_000), Tiers: []PriceTier{{ContextMinTokens: 10, Price: Price{InputMicroUSDPerMTok: microRate(5_000_000)}}}}
	got = ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, Input: 20}, Catalog: Price{InputMicroUSDPerMTok: microRate(1_000_000)}, CatalogFound: true, CrossProvider: &crossTier})
	if got.CostMicroUSD == nil || !got.Estimated || got.Source != "estimate" || *got.CostMicroUSD != 100 {
		t.Fatalf("cross-only tier = %+v, want tier-priced estimate of 100", got)
	}
}

func TestListCostMicroUSDUsesTierAndMixedOneHourRate(t *testing.T) {
	price := Price{InputMicroUSDPerMTok: microRate(1_000_000), CacheWriteMicroUSDPerMTok: microRate(2_000_000), Tiers: []PriceTier{
		{ContextMinTokens: 100, Price: Price{InputMicroUSDPerMTok: microRate(3_000_000), CacheWriteMicroUSDPerMTok: microRate(4_000_000)}},
		{ContextMinTokens: 200, Price: Price{InputMicroUSDPerMTok: microRate(5_000_000), CacheWriteMicroUSDPerMTok: microRate(6_000_000)}},
	}}
	got := ListCostMicroUSD(Usage{Arrived: true, Input: 200, CacheWrite: 10, CacheWrite1h: 4}, price)
	// Context 210 selects the 200 tier: 200 input at 5, 6 five-minute writes at 6,
	// and four one-hour writes at 2x input, all per million tokens.
	if got == nil || *got != 1_000+36+40 {
		t.Fatalf("mixed tier list cost = %v, want 1076 micro-USD", got)
	}
}

func TestListCostMicroUSDTierStartsStrictlyAboveThreshold(t *testing.T) {
	price := Price{InputMicroUSDPerMTok: microRate(1_000_000), Tiers: []PriceTier{
		{ContextMinTokens: 100, Price: Price{InputMicroUSDPerMTok: microRate(2_000_000)}},
	}}
	if got := ListCostMicroUSD(Usage{Arrived: true, Input: 100}, price); got == nil || *got != 100 {
		t.Fatalf("context at the threshold = %v, want 100 micro-USD at the base rate", got)
	}
	if got := ListCostMicroUSD(Usage{Arrived: true, Input: 101}, price); got == nil || *got != 202 {
		t.Fatalf("context one above the threshold = %v, want 202 micro-USD at the tier rate", got)
	}
}

func TestListCostMicroUSDUnknownRateIsNil(t *testing.T) {
	got := ListCostMicroUSD(Usage{Arrived: true, Input: 1, Output: 1}, Price{InputMicroUSDPerMTok: microRate(1_000_000)})
	if got != nil {
		t.Fatalf("unknown output rate = %v, want nil", got)
	}
}

func TestListCostMicroUSDUnarrivedIsNil(t *testing.T) {
	if got := ListCostMicroUSD(Usage{}, Price{}); got != nil {
		t.Fatalf("unarrived list cost = %v, want nil", *got)
	}
}

func TestListCostMicroUSDValidatesAllUsedRatesBeforeOverflowCap(t *testing.T) {
	got := ListCostMicroUSD(Usage{Arrived: true, Input: 2_000_000, Output: 1}, Price{
		InputMicroUSDPerMTok:  microRate(MaxMicroUSD),
		OutputMicroUSDPerMTok: nil,
	})
	if got != nil {
		t.Fatalf("overflow with unknown used output rate = %v, want nil", *got)
	}
}

func TestResolveCallCostCacheWriteAloneSelectsContextTier(t *testing.T) {
	catalog := Price{CacheWriteMicroUSDPerMTok: microRate(1_000_000), Tiers: []PriceTier{{ContextMinTokens: 100, Price: Price{CacheWriteMicroUSDPerMTok: microRate(2_000_000)}}}}
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, CacheWrite: 101}, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 202 || got.Source != "models_dev" {
		t.Fatalf("cache-write-only tier = %+v, want 202 micro-USD from the tier", got)
	}
}

func TestResolveCallCostSelectsLargestMatchingContextTier(t *testing.T) {
	catalog := Price{InputMicroUSDPerMTok: microRate(1_000_000), Tiers: []PriceTier{
		{ContextMinTokens: 100, Price: Price{InputMicroUSDPerMTok: microRate(2_000_000)}},
		{ContextMinTokens: 200, Price: Price{InputMicroUSDPerMTok: microRate(4_000_000)}},
		{ContextMinTokens: 300, Price: Price{InputMicroUSDPerMTok: microRate(8_000_000)}},
	}}
	got := ResolveCallCost(CallPriceInput{PlanKind: PlanMetered, Usage: Usage{Arrived: true, Input: 250}, Catalog: catalog, CatalogFound: true})
	if got.CostMicroUSD == nil || *got.CostMicroUSD != 1_000 || got.Source != "models_dev" {
		t.Fatalf("largest matching tier = %+v, want 1000 micro-USD at tier 200", got)
	}
}
