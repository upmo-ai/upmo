package domain

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/kernel"
)

// CacheHitAlertBelow is the cache hit rate that should raise an alert (UPM-26).
const CacheHitAlertBelow = 0.7

// DefaultContextWindow is the fallback context window size in tokens when a
// model's catalog entry does not specify one.
const DefaultContextWindow = 128000

const tokensPerMillion int64 = 1_000_000

// MaxMicroUSD caps both per-million-token catalog/override rates and
// provider-reported per-call costs at $10,000.
const MaxMicroUSD int64 = 10_000_000_000

const xaiTicksPerMicroUSD int64 = 10_000

const (
	SourceChat       = "chat"
	SourceTask       = "task"
	SourceAutomation = "automation"
	SourceKnowledge  = "knowledge"
	SourceMemory     = "memory"
	SourceSystem     = "system"
)

// AnonymousUserID is the user id an erased person's usage rows carry. It is
// the nil UUID: non-null, so a non-system row keeps a payer, and never the id
// of a real person.
const AnonymousUserID = "00000000-0000-0000-0000-000000000000"

// ValidSource reports whether source is one of the ledger attribution sources.
func ValidSource(source string) bool {
	switch source {
	case SourceChat, SourceTask, SourceAutomation, SourceKnowledge, SourceMemory, SourceSystem:
		return true
	default:
		return false
	}
}

// Principal is the actor triple for a model call, plus an optional parent
// run used to roll tokens up to a task and optional TeamID to narrow team
// route and account access to a single team.
type Principal struct {
	User         string
	Agent        string
	Run          string
	Parent       string
	TeamID       string
	Source       string
	SourceID     string
	AutomationID string
}

// Usage is the normalised token counts for one attempt. Arrived is false
// when the provider returned no usage block. ReportedCostMicroUSD holds an
// optional validated provider-reported call cost in micro-USD and stays runtime-only.
type Usage struct {
	Arrived              bool   `json:"arrived"`
	Input                int    `json:"input_tokens"`
	Output               int    `json:"output_tokens"`
	CacheRead            int    `json:"cache_read"`
	CacheWrite           int    `json:"cache_write"`
	CacheWrite1h         int    `json:"-"`
	Reasoning            int    `json:"reasoning"`
	ReportedCostMicroUSD *int64 `json:"-"`
}

// Tokens is every counted token on this attempt. Reasoning is a breakdown
// of Output, not an extra dimension, so it is not added again.
func (u Usage) Tokens() int {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite
}

// Price is one catalog row for a model. A nil rate means that dimension
// has no price.
type Price struct {
	Version                   string
	InputPerMTok              *float64
	OutputPerMTok             *float64
	CacheReadMTok             *float64
	CacheWriteMTok            *float64
	InputMicroUSDPerMTok      *int64
	OutputMicroUSDPerMTok     *int64
	CacheReadMicroUSDPerMTok  *int64
	CacheWriteMicroUSDPerMTok *int64
	ContextWindow             int
	Tiers                     []PriceTier
}

// PriceTier is a context threshold and its optional per-token rates.
type PriceTier struct {
	ContextMinTokens int
	Price            Price
}

// Entry is one ledger row, written per attempt.
type Entry struct {
	At               time.Time
	Route            string
	LinkPosition     int
	AccountID        string
	Model            string
	PlanKind         string
	Class            FailureClass
	Principal        Principal
	Usage            Usage
	CostMicroUSD     *int64
	ListCostMicroUSD *int64
	Estimated        bool
	LatencyMS        *int
	ChargedToLimit   bool
	PriceSource      string
	PriceVersion     string
	// OwnerTeamID is the team through which the person reached a shared
	// account, as it was when the attempt ran. It is empty for their own
	// account and for a workspace-wide one.
	OwnerTeamID string
}

// CallPriceInput carries every input needed to resolve one attempt's cost and
// winning price_source.
type CallPriceInput struct {
	PlanKind          string
	Model             string
	Usage             Usage
	Override          *PriceOverride
	ProviderList      *Price
	Catalog           Price
	CatalogFound      bool
	CrossProvider     *Price
	ZeroCostAllowed   bool
	EstimateRate      int64
	EstimateRateKnown bool
	InputBytes        int
	OutputBytes       int
	KnownZero         bool
}

// CallPriceResult is the resolved cost, estimate flag, and winning price_source.
type CallPriceResult struct {
	CostMicroUSD *int64
	Estimated    bool
	Source       string
}

// ResolveCallCost resolves a single attempt's cost and price_source using pure
// precedence:
// 1. override -> 2. provider reported cost -> 3. provider_list -> 4. exact (vendor, model) models_dev ->
// 5a. cross-provider models.dev (”) -> 5b. vendor highest rate -> unknown.
// Non-metered plans return nil cost and empty source; knownZero attempts return 0 ("not_billed").
// Any dimension filled from 5a or 5b makes the whole call estimated with source "estimate".
// If any used dimension has no real price (no 1-4, 5a, or 5b) and there is no valid
// reported cost, the whole call returns nil cost with source "unknown".
func ResolveCallCost(in CallPriceInput) CallPriceResult {
	if in.Usage.CacheWrite1h < 0 {
		in.Usage.CacheWrite1h = 0
	}
	if in.Usage.CacheWrite1h > in.Usage.CacheWrite {
		in.Usage.CacheWrite1h = max(0, in.Usage.CacheWrite)
	}
	if in.PlanKind != PlanMetered {
		return CallPriceResult{}
	}
	if in.KnownZero {
		zero := int64(0)
		return CallPriceResult{CostMicroUSD: &zero, Source: "not_billed"}
	}
	var vendorEst *int64
	if in.EstimateRateKnown && in.EstimateRate > 0 {
		v := in.EstimateRate
		vendorEst = &v
	}
	var catPtr *Price
	if in.CatalogFound {
		catPtr = &in.Catalog
	}
	contextTokens := saturatingPositiveSum(int64(in.Usage.Input), int64(in.Usage.CacheRead), int64(in.Usage.CacheWrite))
	var selectedTier *PriceTier
	tierPrice := &in.Catalog
	crossPtr := in.CrossProvider
	tierFromCross := false
	if !in.CatalogFound && in.CrossProvider != nil {
		tierPrice = in.CrossProvider
	}
	if in.Usage.Arrived {
		for i := range tierPrice.Tiers {
			tier := &tierPrice.Tiers[i]
			if int64(tier.ContextMinTokens) < contextTokens && (selectedTier == nil || tier.ContextMinTokens > selectedTier.ContextMinTokens) {
				selectedTier = tier
			}
		}
		if in.CatalogFound && selectedTier == nil && in.CrossProvider != nil {
			for i := range in.CrossProvider.Tiers {
				tier := &in.CrossProvider.Tiers[i]
				if int64(tier.ContextMinTokens) < contextTokens && (selectedTier == nil || tier.ContextMinTokens > selectedTier.ContextMinTokens) {
					selectedTier = tier
					tierPrice = in.CrossProvider
					tierFromCross = true
					catPtr = nil
					crossPtr = in.CrossProvider
				}
			}
		}
	}
	if selectedTier != nil {
		price := *tierPrice
		price.InputMicroUSDPerMTok = chooseTierRate(selectedTier.Price.InputMicroUSDPerMTok, price.InputMicroUSDPerMTok)
		price.OutputMicroUSDPerMTok = chooseTierRate(selectedTier.Price.OutputMicroUSDPerMTok, price.OutputMicroUSDPerMTok)
		price.CacheReadMicroUSDPerMTok = chooseTierRate(selectedTier.Price.CacheReadMicroUSDPerMTok, price.CacheReadMicroUSDPerMTok)
		price.CacheWriteMicroUSDPerMTok = chooseTierRate(selectedTier.Price.CacheWriteMicroUSDPerMTok, price.CacheWriteMicroUSDPerMTok)
		if in.CatalogFound && !tierFromCross {
			catPtr = &price
		} else {
			crossPtr = &price
		}
	}
	if in.Usage.Arrived {
		// Provider-reported pricing reflects the selected context tier, while a
		// flat override only covers base rates and can only estimate this call.
		if selectedTier != nil && in.Usage.ReportedCostMicroUSD != nil {
			rc := *in.Usage.ReportedCostMicroUSD
			if rc >= 0 && rc <= MaxMicroUSD && (rc > 0 || in.ZeroCostAllowed) {
				return CallPriceResult{CostMicroUSD: &rc, Source: "provider"}
			}
		}
		hasOverrideOnUsed := in.Override != nil &&
			((in.Usage.Input > 0 && in.Override.InputMicroUSDPerMTok != nil) ||
				(in.Usage.Output > 0 && in.Override.OutputMicroUSDPerMTok != nil) ||
				(in.Usage.CacheRead > 0 && in.Override.CacheReadMicroUSDPerMTok != nil) ||
				(in.Usage.CacheWrite > 0 && in.Override.CacheWriteMicroUSDPerMTok != nil))
		if hasOverrideOnUsed {
			if cost, _, _, _, _, ok := sumDimensionCost(in.Usage, in.Override, in.ProviderList, catPtr, nil, nil); ok {
				if selectedTier != nil {
					if cost > MaxMicroUSD {
						cost = MaxMicroUSD
					}
					return CallPriceResult{CostMicroUSD: &cost, Estimated: true, Source: "estimate"}
				}
				return CallPriceResult{CostMicroUSD: &cost, Source: "override"}
			}
		}
		if in.Usage.ReportedCostMicroUSD != nil {
			rc := *in.Usage.ReportedCostMicroUSD
			if rc >= 0 && rc <= MaxMicroUSD && (rc > 0 || in.ZeroCostAllowed) {
				return CallPriceResult{CostMicroUSD: &rc, Source: "provider"}
			}
		}
		if selectedTier == nil && in.Usage.CacheWrite1h == 0 && !hasOverrideOnUsed && (in.ProviderList != nil || in.CatalogFound) {
			if cost, usedProvList, _, _, _, ok := sumDimensionCost(in.Usage, nil, in.ProviderList, catPtr, nil, nil); ok {
				src := "models_dev"
				if usedProvList || (!in.CatalogFound && in.ProviderList != nil) {
					src = "provider_list"
				}
				return CallPriceResult{CostMicroUSD: &cost, Source: src}
			}
		}
		pricingUsage := in.Usage
		if pricingUsage.CacheWrite1h > 0 && !hasOverrideOnUsed {
			pricingUsage.CacheWrite -= pricingUsage.CacheWrite1h
		}
		hasBilledTokens := pricingUsage.Input > 0 || pricingUsage.Output > 0 || pricingUsage.CacheRead > 0 || pricingUsage.CacheWrite > 0 || in.Usage.CacheWrite1h > 0
		if hasBilledTokens {
			cost, usedProvList, usedCross, usedFallback, missingRate, ok := sumDimensionCost(pricingUsage, in.Override, in.ProviderList, catPtr, crossPtr, vendorEst)
			if ok {
				if in.Usage.CacheWrite1h > 0 && !hasOverrideOnUsed {
					rate := firstRate(ratePtr(in.ProviderList), ratePtr(catPtr), ratePtr(crossPtr), vendorEst)
					if rate == nil {
						return CallPriceResult{Source: "unknown"}
					}
					if *rate < 0 {
						return CallPriceResult{Source: "unknown"}
					}
					var oneHourCost int64
					if *rate > 0 {
						oneHourCost = cappedEstimateAmount(int64(in.Usage.CacheWrite1h), *rate*2)
					}
					if cost > MaxMicroUSD-oneHourCost {
						cost = MaxMicroUSD
					} else {
						cost += oneHourCost
					}
					return CallPriceResult{CostMicroUSD: &cost, Estimated: true, Source: "estimate"}
				}
				if cost > MaxMicroUSD {
					cost = MaxMicroUSD
				}
				if selectedTier != nil && in.CatalogFound && !hasOverrideOnUsed && !usedProvList && !usedCross && !usedFallback {
					return CallPriceResult{CostMicroUSD: &cost, Source: "models_dev"}
				}
				return CallPriceResult{CostMicroUSD: &cost, Estimated: true, Source: "estimate"}
			}
			if missingRate {
				return CallPriceResult{Source: "unknown"}
			}
			capped := MaxMicroUSD
			return CallPriceResult{
				CostMicroUSD: &capped,
				Estimated:    true,
				Source:       "estimate",
			}
		}
		if crossPtr != nil {
			zero := int64(0)
			return CallPriceResult{CostMicroUSD: &zero, Estimated: true, Source: "estimate"}
		}
		if vendorEst == nil {
			return CallPriceResult{Source: "unknown"}
		}
		tokens := saturatingPositiveSum(
			int64(in.Usage.Input),
			int64(in.Usage.Output),
			int64(in.Usage.CacheRead),
			int64(in.Usage.CacheWrite),
		)
		if tokens < 1 {
			tokens = 1
		}
		amount := cappedEstimateAmount(tokens, *vendorEst)
		return CallPriceResult{
			CostMicroUSD: &amount,
			Estimated:    true,
			Source:       "estimate",
		}
	}
	inBytes := max(int64(in.InputBytes), 0)
	outBytes := max(int64(in.OutputBytes), 0)
	estIn := inBytes/4 + (inBytes%4+3)/4
	estOut := saturatingPositiveSum(outBytes/4+(outBytes%4+3)/4, int64(in.Usage.Output))
	if estIn == 0 && estOut == 0 {
		estIn = 1
	}
	cost, _, _, _, missingRate, ok := sumDimensionCostCounts(estIn, estOut, 0, 0, in.Override, in.ProviderList, catPtr, crossPtr, vendorEst)
	if ok {
		if cost > MaxMicroUSD {
			cost = MaxMicroUSD
		}
		return CallPriceResult{
			CostMicroUSD: &cost,
			Estimated:    true,
			Source:       "estimate",
		}
	}
	if missingRate {
		return CallPriceResult{Source: "unknown"}
	}
	capped := MaxMicroUSD
	return CallPriceResult{
		CostMicroUSD: &capped,
		Estimated:    true,
		Source:       "estimate",
	}
}

// ListCostMicroUSD prices subscription/local usage from its record-time catalog snapshot.
// A nil result means at least one used dimension had no known rate.
func ListCostMicroUSD(usage Usage, catalog Price) *int64 {
	if usage.CacheWrite1h < 0 {
		usage.CacheWrite1h = 0
	}
	if usage.CacheWrite1h > usage.CacheWrite {
		usage.CacheWrite1h = max(0, usage.CacheWrite)
	}
	contextTokens := saturatingPositiveSum(int64(usage.Input), int64(usage.CacheRead), int64(usage.CacheWrite))
	var selected *PriceTier
	for i := range catalog.Tiers {
		t := &catalog.Tiers[i]
		if int64(t.ContextMinTokens) < contextTokens && (selected == nil || t.ContextMinTokens > selected.ContextMinTokens) {
			selected = t
		}
	}
	if selected != nil {
		catalog.InputMicroUSDPerMTok = chooseTierRate(selected.Price.InputMicroUSDPerMTok, catalog.InputMicroUSDPerMTok)
		catalog.OutputMicroUSDPerMTok = chooseTierRate(selected.Price.OutputMicroUSDPerMTok, catalog.OutputMicroUSDPerMTok)
		catalog.CacheReadMicroUSDPerMTok = chooseTierRate(selected.Price.CacheReadMicroUSDPerMTok, catalog.CacheReadMicroUSDPerMTok)
		catalog.CacheWriteMicroUSDPerMTok = chooseTierRate(selected.Price.CacheWriteMicroUSDPerMTok, catalog.CacheWriteMicroUSDPerMTok)
	}
	usage.CacheWrite -= usage.CacheWrite1h
	counts := []int64{int64(usage.Input), int64(usage.Output), int64(usage.CacheRead), int64(usage.CacheWrite), int64(usage.CacheWrite1h)}
	rates := []*int64{catalog.InputMicroUSDPerMTok, catalog.OutputMicroUSDPerMTok, catalog.CacheReadMicroUSDPerMTok, catalog.CacheWriteMicroUSDPerMTok, catalog.InputMicroUSDPerMTok}
	maxNumerator := MaxMicroUSD*tokensPerMillion + tokensPerMillion/2
	var numerator int64
	for i, count := range counts {
		if count <= 0 {
			continue
		}
		if rates[i] == nil || *rates[i] < 0 {
			return nil
		}
		rate := *rates[i]
		if i == len(counts)-1 {
			if rate > math.MaxInt64/2 {
				return nil
			}
			rate *= 2
		}
		if rate > 0 && count > (maxNumerator-numerator)/rate {
			capped := MaxMicroUSD
			return &capped
		}
		numerator += count * rate
	}
	cost := roundHalfUp(numerator)
	if cost > MaxMicroUSD {
		cost = MaxMicroUSD
	}
	return &cost
}

func ratePtr(p *Price) *int64 {
	if p == nil {
		return nil
	}
	return p.InputMicroUSDPerMTok
}
func firstRate(rates ...*int64) *int64 {
	for _, rate := range rates {
		if rate != nil {
			return rate
		}
	}
	return nil
}

func chooseTierRate(tier, base *int64) *int64 {
	if tier != nil {
		return tier
	}
	return base
}

func cappedEstimateAmount(tokens, rate int64) int64 {
	amount := MaxMicroUSD
	if tokens <= (math.MaxInt64-tokensPerMillion/2)/rate {
		amount = roundHalfUp(tokens * rate)
		if amount > MaxMicroUSD {
			amount = MaxMicroUSD
		}
	}
	if amount < 1 {
		amount = 1
	}
	return amount
}

func saturatingPositiveSum(counts ...int64) int64 {
	var total int64
	for _, count := range counts {
		if count <= 0 {
			continue
		}
		if total > math.MaxInt64-count {
			return math.MaxInt64
		}
		total += count
	}
	return total
}

func sumDimensionCost(usage Usage, ov *PriceOverride, prov, cat, cross *Price, fallbackEst *int64) (cost int64, usedProvList, usedCross, usedFallback, missingRate, ok bool) {
	return sumDimensionCostCounts(int64(usage.Input), int64(usage.Output), int64(usage.CacheRead), int64(usage.CacheWrite), ov, prov, cat, cross, fallbackEst)
}

func sumDimensionCostCounts(inTok, outTok, crTok, cwTok int64, ov *PriceOverride, prov, cat, cross *Price, fallbackEst *int64) (cost int64, usedProvList, usedCross, usedFallback, missingRate, ok bool) {
	if ov == nil && prov == nil && cat == nil && cross == nil && fallbackEst == nil {
		return 0, false, false, false, true, false
	}
	var ovIn, ovOut, ovCR, ovCW *int64
	if ov != nil {
		ovIn = ov.InputMicroUSDPerMTok
		ovOut = ov.OutputMicroUSDPerMTok
		ovCR = ov.CacheReadMicroUSDPerMTok
		ovCW = ov.CacheWriteMicroUSDPerMTok
	}
	var pIn, pOut, pCR, pCW *int64
	if prov != nil {
		pIn = prov.InputMicroUSDPerMTok
		pOut = prov.OutputMicroUSDPerMTok
		pCR = prov.CacheReadMicroUSDPerMTok
		pCW = prov.CacheWriteMicroUSDPerMTok
	}
	var cIn, cOut, cCR, cCW *int64
	if cat != nil {
		cIn = cat.InputMicroUSDPerMTok
		cOut = cat.OutputMicroUSDPerMTok
		cCR = cat.CacheReadMicroUSDPerMTok
		cCW = cat.CacheWriteMicroUSDPerMTok
	}
	var xIn, xOut, xCR, xCW *int64
	if cross != nil {
		xIn = cross.InputMicroUSDPerMTok
		xOut = cross.OutputMicroUSDPerMTok
		xCR = cross.CacheReadMicroUSDPerMTok
		xCW = cross.CacheWriteMicroUSDPerMTok
	}
	var fRate *int64
	if fallbackEst != nil && *fallbackEst > 0 {
		fRate = fallbackEst
	}
	type resolvedDim struct {
		n    int64
		rate int64
	}
	dims := [4]struct {
		n          int64
		o, p, c, x *int64
	}{
		{inTok, ovIn, pIn, cIn, xIn},
		{outTok, ovOut, pOut, cOut, xOut},
		{crTok, ovCR, pCR, cCR, xCR},
		{cwTok, ovCW, pCW, cCW, xCW},
	}
	// Resolve every used dimension's rate first so an overflow on one
	// dimension cannot hide a missing rate on a later dimension.
	var resolved [4]resolvedDim
	var count int
	for _, d := range dims {
		if d.n <= 0 {
			continue
		}
		var rate int64
		switch {
		case d.o != nil:
			rate = *d.o
		case d.p != nil:
			rate = *d.p
			usedProvList = true
		case d.c != nil:
			rate = *d.c
		case d.x != nil:
			rate = *d.x
			usedCross = true
		case fRate != nil:
			rate = *fRate
			usedFallback = true
		default:
			return 0, false, false, false, true, false
		}
		if rate < 0 {
			return 0, false, false, false, true, false
		}
		resolved[count] = resolvedDim{n: d.n, rate: rate}
		count++
	}
	var numerator int64
	for _, d := range resolved[:count] {
		if d.rate > 0 && d.n > (math.MaxInt64-tokensPerMillion/2-numerator)/d.rate {
			return 0, false, false, false, false, false
		}
		numerator += d.n * d.rate
	}
	return roundHalfUp(numerator), usedProvList, usedCross, usedFallback, false, true
}

// NeedsEstimate reports whether MeteredCost cannot price the call accurately
// from the catalog or as a known zero.
func NeedsEstimate(plan string, usage Usage, price Price, found, knownZero bool) bool {
	res := ResolveCallCost(CallPriceInput{
		PlanKind:     plan,
		Usage:        usage,
		Catalog:      price,
		CatalogFound: found,
		KnownZero:    knownZero,
	})
	return res.Estimated || res.Source == "unknown"
}

// MeteredCost returns a priced amount, a known zero, a flagged estimate when
// estimateRate > 0, or nil with source "unknown" when no rate is known.
func MeteredCost(plan string, usage Usage, price Price, found bool, estimateRate int64, inputBytes, outputBytes int, knownZero bool) (micro *int64, estimated bool, source string) {
	res := ResolveCallCost(CallPriceInput{
		PlanKind:          plan,
		Usage:             usage,
		Catalog:           price,
		CatalogFound:      found,
		EstimateRate:      estimateRate,
		EstimateRateKnown: estimateRate > 0,
		InputBytes:        inputBytes,
		OutputBytes:       outputBytes,
		KnownZero:         knownZero,
	})
	return res.CostMicroUSD, res.Estimated, res.Source
}

func roundHalfUp(numerator int64) int64 {
	return (numerator + tokensPerMillion/2) / tokensPerMillion
}

// ParseDecimalMicroUSD parses a non-negative JSON decimal number or quoted
// decimal string, multiplies by scale, and rounds half up to an int64 without
// floating-point arithmetic.
func ParseDecimalMicroUSD(raw json.RawMessage, scale int64) (int64, bool) {
	return parseDecimalRatio(raw, scale, 1)
}

// ParseDecimalStringMicroUSD parses a non-negative plain decimal string,
// multiplies by scale, and rounds half up to an int64 without floating-point arithmetic.
func ParseDecimalStringMicroUSD(s string, scale int64) (int64, bool) {
	return parseDecimalStringRatio(s, scale, 1)
}

// ParseTicksMicroUSD converts xAI cost_in_usd_ticks (1 USD = 10^10 ticks,
// 1 micro-USD = 10,000 ticks) to integer micro-USD with half-up rounding.
func ParseTicksMicroUSD(raw json.RawMessage) (int64, bool) {
	return parseDecimalRatio(raw, 1, xaiTicksPerMicroUSD)
}

func parseDecimalRatio(raw json.RawMessage, mul, div int64) (int64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, false
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return 0, false
		}
		return parseDecimalStringRatio(s, mul, div)
	}
	return parseDecimalStringRatio(string(trimmed), mul, div)
}

func parseDecimalStringRatio(s string, mul, div int64) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 || mul <= 0 || div <= 0 {
		return 0, false
	}
	// Reject negative numbers; accept an optional leading '+' sign.
	if s[0] == '-' {
		return 0, false
	}
	if s[0] == '+' {
		s = s[1:]
		if s == "" {
			return 0, false
		}
	}
	mantissa := s
	exp := 0
	if idx := strings.IndexAny(s, "eE"); idx >= 0 {
		mantissa = s[:idx]
		expStr := s[idx+1:]
		if mantissa == "" || expStr == "" {
			return 0, false
		}
		expSign := 1
		switch expStr[0] {
		case '-':
			expSign = -1
			expStr = expStr[1:]
		case '+':
			expStr = expStr[1:]
		}
		if expStr == "" || len(expStr) > 6 {
			return 0, false
		}
		for i := 0; i < len(expStr); i++ {
			if expStr[i] < '0' || expStr[i] > '9' {
				return 0, false
			}
			exp = exp*10 + int(expStr[i]-'0')
		}
		exp *= expSign
	}
	var digits strings.Builder
	digits.Grow(len(mantissa))
	seenDot := false
	fracDigits := 0
	for i := 0; i < len(mantissa); i++ {
		c := mantissa[i]
		switch {
		case c >= '0' && c <= '9':
			digits.WriteByte(c)
			if seenDot {
				fracDigits++
			}
		case c == '.':
			if seenDot {
				return 0, false
			}
			seenDot = true
		default:
			return 0, false
		}
	}
	if digits.Len() == 0 {
		return 0, false
	}
	exp10 := exp - fracDigits
	if exp10 > 30 || exp10 < -30 {
		// Check if mantissa is zero (e.g. 0.0000000000000000000000000000000)
		allZero := true
		for _, c := range digits.String() {
			if c != '0' {
				allZero = false
				break
			}
		}
		if allZero {
			return 0, true
		}
		if exp10 > 30 {
			return 0, false
		}
		// exp10 < -30 with bounded digits: non-negative < 10^-30 * something -> rounds to 0 if tiny,
		// unless digits are huge. Reject absurd exponents beyond [-30, 30] except pure zero.
		if exp10 < -64 {
			return 0, false
		}
	}
	val, ok := new(big.Int).SetString(digits.String(), 10)
	if !ok {
		return 0, false
	}
	if val.Sign() == 0 {
		return 0, true
	}
	num := new(big.Int).Mul(val, big.NewInt(mul))
	den := big.NewInt(div)
	ten := big.NewInt(10)
	if exp10 >= 0 {
		pow := new(big.Int).Exp(ten, big.NewInt(int64(exp10)), nil)
		num.Mul(num, pow)
	} else {
		pow := new(big.Int).Exp(ten, big.NewInt(int64(-exp10)), nil)
		den.Mul(den, pow)
	}
	// Half-up rounding: (num + den/2) / den
	half := new(big.Int).Rsh(den, 1)
	num.Add(num, half)
	num.Quo(num, den)
	if !num.IsInt64() {
		return 0, false
	}
	return num.Int64(), true
}

// USDFromMicro is only a response compatibility conversion; ledger pricing
// itself stays integer micro-USD.
func USDFromMicro(micro *int64) *float64 {
	if micro == nil {
		return nil
	}
	usd := float64(*micro) / float64(kernel.MicroUSDPerDollar)
	return &usd
}

// CacheHitRate is cache read divided by input plus cache read.
func CacheHitRate(input, cacheRead int) float64 {
	denom := input + cacheRead
	if denom == 0 {
		return 0
	}
	return float64(cacheRead) / float64(denom)
}

// CacheHitAlert is true when a non-empty window is below 70%.
func CacheHitAlert(input, cacheRead int) bool {
	if input+cacheRead == 0 {
		return false
	}
	return CacheHitRate(input, cacheRead) < CacheHitAlertBelow
}

// RollupTokens sums attempt tokens, used for a parent task.
func RollupTokens(rows []Usage) int {
	n := 0
	for _, row := range rows {
		n += row.Tokens()
	}
	return n
}
