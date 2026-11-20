package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/kernel"
)

// PriceOverride holds per-account, per-model rate overrides in integer
// micro-USD per million tokens. Nil fields leave the next source active.
type PriceOverride struct {
	AccountID                 string     `json:"account_id,omitempty"`
	Model                     string     `json:"model"`
	InputMicroUSDPerMTok      *int64     `json:"input_micro_usd_per_mtok"`
	OutputMicroUSDPerMTok     *int64     `json:"output_micro_usd_per_mtok"`
	CacheReadMicroUSDPerMTok  *int64     `json:"cache_read_micro_usd_per_mtok"`
	CacheWriteMicroUSDPerMTok *int64     `json:"cache_write_micro_usd_per_mtok"`
	UpdatedBy                 string     `json:"updated_by,omitempty"`
	UpdatedAt                 *time.Time `json:"updated_at,omitempty"`
}

// OverrideWarning flags an override rate that differs from the reference price
// by more than 20%.
type OverrideWarning struct {
	Field         string      `json:"field"`
	OverrideMicro int64       `json:"override_micro_usd_per_mtok"`
	BaseMicro     int64       `json:"base_micro_usd_per_mtok"`
	BaseSource    FieldSource `json:"base_source"`
}

// CleanPriceOverride validates a price override's model id and rate bounds.
func CleanPriceOverride(in PriceOverride) (PriceOverride, error) {
	model, err := CleanModelID(in.Model)
	if err != nil {
		return PriceOverride{}, err
	}
	checkRate := func(name string, v *int64) (*int64, error) {
		if v == nil {
			return nil, nil
		}
		if *v < 0 || *v > MaxMicroUSD {
			return nil, Invalid(fmt.Sprintf("%s rate must be between 0 and %d micro-USD per million tokens.", name, MaxMicroUSD))
		}
		cp := *v
		return &cp, nil
	}
	inRate, err := checkRate("Input", in.InputMicroUSDPerMTok)
	if err != nil {
		return PriceOverride{}, err
	}
	outRate, err := checkRate("Output", in.OutputMicroUSDPerMTok)
	if err != nil {
		return PriceOverride{}, err
	}
	crRate, err := checkRate("Cache read", in.CacheReadMicroUSDPerMTok)
	if err != nil {
		return PriceOverride{}, err
	}
	cwRate, err := checkRate("Cache write", in.CacheWriteMicroUSDPerMTok)
	if err != nil {
		return PriceOverride{}, err
	}
	if inRate == nil && outRate == nil && crRate == nil && cwRate == nil {
		return PriceOverride{}, Invalid("Set at least one price override rate.")
	}
	return PriceOverride{
		AccountID:                 in.AccountID,
		Model:                     model,
		InputMicroUSDPerMTok:      inRate,
		OutputMicroUSDPerMTok:     outRate,
		CacheReadMicroUSDPerMTok:  crRate,
		CacheWriteMicroUSDPerMTok: cwRate,
		UpdatedBy:                 in.UpdatedBy,
		UpdatedAt:                 in.UpdatedAt,
	}, nil
}

// ToModelOverride converts micro-USD/MTok rates into a ModelOverride for display
// merging.
func (o *PriceOverride) ToModelOverride() *ModelOverride {
	if o == nil {
		return nil
	}
	return &ModelOverride{
		InputPerMTok:   USDFromMicro(o.InputMicroUSDPerMTok),
		OutputPerMTok:  USDFromMicro(o.OutputMicroUSDPerMTok),
		CacheReadMTok:  USDFromMicro(o.CacheReadMicroUSDPerMTok),
		CacheWriteMTok: USDFromMicro(o.CacheWriteMicroUSDPerMTok),
	}
}

// EqualRates reports whether a and b have identical rate fields.
func (o PriceOverride) EqualRates(other PriceOverride) bool {
	return equalInt64Ptr(o.InputMicroUSDPerMTok, other.InputMicroUSDPerMTok) &&
		equalInt64Ptr(o.OutputMicroUSDPerMTok, other.OutputMicroUSDPerMTok) &&
		equalInt64Ptr(o.CacheReadMicroUSDPerMTok, other.CacheReadMicroUSDPerMTok) &&
		equalInt64Ptr(o.CacheWriteMicroUSDPerMTok, other.CacheWriteMicroUSDPerMTok)
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ParseSeenPricingMicroUSD converts OpenRouter-style account_models_seen.pricing
// per-token USD strings into integer micro-USD per million tokens. Negative,
// non-numeric, or out-of-bounds values (such as OpenRouter's "-1") are ignored.
func ParseSeenPricingMicroUSD(pricing map[string]string) (Price, bool) {
	if len(pricing) == 0 {
		return Price{}, false
	}
	scale := tokensPerMillion * kernel.MicroUSDPerDollar
	parseField := func(key string) *int64 {
		raw, ok := pricing[key]
		if !ok {
			return nil
		}
		micro, ok := ParseDecimalStringMicroUSD(raw, scale)
		if !ok || micro < 0 || micro > MaxMicroUSD {
			return nil
		}
		return &micro
	}
	p := Price{
		InputMicroUSDPerMTok:      parseField("prompt"),
		OutputMicroUSDPerMTok:     parseField("completion"),
		CacheReadMicroUSDPerMTok:  parseField("input_cache_read"),
		CacheWriteMicroUSDPerMTok: parseField("input_cache_write"),
	}
	p.InputPerMTok = USDFromMicro(p.InputMicroUSDPerMTok)
	p.OutputPerMTok = USDFromMicro(p.OutputMicroUSDPerMTok)
	p.CacheReadMTok = USDFromMicro(p.CacheReadMicroUSDPerMTok)
	p.CacheWriteMTok = USDFromMicro(p.CacheWriteMicroUSDPerMTok)
	hasAny := p.InputMicroUSDPerMTok != nil ||
		p.OutputMicroUSDPerMTok != nil ||
		p.CacheReadMicroUSDPerMTok != nil ||
		p.CacheWriteMicroUSDPerMTok != nil
	return p, hasAny
}

// OverrideWarnings compares each non-nil override rate against providerList
// (account_models_seen.pricing) if present, else catalog (models.dev for the
// account's own provider, never the cross-provider ” row). Rates differing by
// more than 20% (|override - base| * 5 > base) are flagged. Zero or absent base
// rates are skipped.
func OverrideWarnings(override *PriceOverride, providerList *Price, catalog *Price) []OverrideWarning {
	out := []OverrideWarning{}
	if override == nil {
		return out
	}
	check := func(field string, ov *int64, prov, cat *int64) {
		if ov == nil {
			return
		}
		var base *int64
		var src FieldSource
		switch {
		case prov != nil:
			base, src = prov, SourceProvider
		case cat != nil:
			base, src = cat, SourceModelsDev
		default:
			return
		}
		if *base <= 0 {
			return
		}
		diff := *ov - *base
		if diff < 0 {
			diff = -diff
		}
		if diff*5 > *base {
			out = append(out, OverrideWarning{
				Field:         field,
				OverrideMicro: *ov,
				BaseMicro:     *base,
				BaseSource:    src,
			})
		}
	}
	var pIn, pOut, pCR, pCW *int64
	if providerList != nil {
		pIn = providerList.InputMicroUSDPerMTok
		pOut = providerList.OutputMicroUSDPerMTok
		pCR = providerList.CacheReadMicroUSDPerMTok
		pCW = providerList.CacheWriteMicroUSDPerMTok
	}
	var cIn, cOut, cCR, cCW *int64
	if catalog != nil {
		cIn = catalog.InputMicroUSDPerMTok
		cOut = catalog.OutputMicroUSDPerMTok
		cCR = catalog.CacheReadMicroUSDPerMTok
		cCW = catalog.CacheWriteMicroUSDPerMTok
	}
	check("input_micro_usd_per_mtok", override.InputMicroUSDPerMTok, pIn, cIn)
	check("output_micro_usd_per_mtok", override.OutputMicroUSDPerMTok, pOut, cOut)
	check("cache_read_micro_usd_per_mtok", override.CacheReadMicroUSDPerMTok, pCR, cCR)
	check("cache_write_micro_usd_per_mtok", override.CacheWriteMicroUSDPerMTok, pCW, cCW)
	return out
}

// ZeroCostAllowed reports whether a provider-reported zero cost is valid for
// model (OpenRouter ":free" models or provider price lists where both prompt
// and completion are explicitly 0).
func ZeroCostAllowed(model string, providerList *Price) bool {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), ":free") {
		return true
	}
	if providerList != nil &&
		providerList.InputMicroUSDPerMTok != nil && *providerList.InputMicroUSDPerMTok == 0 &&
		providerList.OutputMicroUSDPerMTok != nil && *providerList.OutputMicroUSDPerMTok == 0 {
		return true
	}
	return false
}
