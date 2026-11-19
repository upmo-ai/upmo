package domain

import "github.com/upmo-ai/upmo/internal/kernel"

// CacheHitAlertBelow is the cache hit rate that should raise an alert (UPM-26).
const CacheHitAlertBelow = 0.7

// DefaultContextWindow is the fallback context window size in tokens when a
// model's catalog entry does not specify one.
const DefaultContextWindow = 128000

const tokensPerMillion int64 = 1_000_000

// EstimatedMicroUSDPerMTok is the fallback rate when the catalog has no rate
// for a provider.
const EstimatedMicroUSDPerMTok int64 = 75_000_000

const (
	SourceChat       = "chat"
	SourceTask       = "task"
	SourceAutomation = "automation"
	SourceKnowledge  = "knowledge"
	SourceMemory     = "memory"
	SourceSystem     = "system"
)

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
// when the provider returned no usage block.
type Usage struct {
	Arrived    bool `json:"arrived"`
	Input      int  `json:"input_tokens"`
	Output     int  `json:"output_tokens"`
	CacheRead  int  `json:"cache_read"`
	CacheWrite int  `json:"cache_write"`
	Reasoning  int  `json:"reasoning"`
}

// Tokens is every counted token on this attempt.
func (u Usage) Tokens() int {
	return u.Input + u.Output + u.CacheRead + u.CacheWrite + u.Reasoning
}

// Price is one catalog row for a model. A nil rate means that dimension
// has no price.
type Price struct {
	Version                   string
	Provider                  string
	InputPerMTok              *float64
	OutputPerMTok             *float64
	CacheReadMTok             *float64
	CacheWriteMTok            *float64
	InputMicroUSDPerMTok      *int64
	OutputMicroUSDPerMTok     *int64
	CacheReadMicroUSDPerMTok  *int64
	CacheWriteMicroUSDPerMTok *int64
	ContextWindow             int
}

// Entry is one ledger row, written per attempt.
type Entry struct {
	Route          string
	LinkPosition   int
	AccountID      string
	Model          string
	PlanKind       string
	Class          FailureClass
	Principal      Principal
	Usage          Usage
	CostMicroUSD   *int64
	Estimated      bool
	LatencyMS      *int
	ChargedToLimit bool
	PriceSource    string
	PriceVersion   string
	// OwnerTeamID is the team through which the person reached a shared
	// account, as it was when the attempt ran. It is empty for their own
	// account and for a workspace-wide one.
	OwnerTeamID string
}

// NeedsEstimate reports whether MeteredCost will fall back to an estimate
// instead of pricing from the catalog or recording a known zero.
func NeedsEstimate(plan string, usage Usage, price Price, found, knownZero bool) bool {
	if plan != PlanMetered || knownZero {
		return false
	}
	return pricedMicroUSD(usage, price, found) == nil
}

// MeteredCost returns a priced amount, a known zero, or a positive flagged
// estimate. Rates are integer micro-USD per million tokens and each row rounds
// half up once after its dimensions are summed. When usage did not arrive,
// estimates count ceil((inputBytes + outputBytes) / 4) + usage.Output tokens,
// with a minimum of 1.
func MeteredCost(plan string, usage Usage, price Price, found bool, estimateRate int64, inputBytes, outputBytes int, knownZero bool) (micro *int64, estimated bool, source string) {
	if plan != PlanMetered {
		return nil, false, ""
	}
	if knownZero {
		zero := int64(0)
		return &zero, false, "not_billed"
	}
	if priced := pricedMicroUSD(usage, price, found); priced != nil {
		return priced, false, "models_dev"
	}
	if estimateRate <= 0 {
		estimateRate = EstimatedMicroUSDPerMTok
	}
	tokens := int64(usage.Tokens())
	if !usage.Arrived {
		tokens = int64((inputBytes+outputBytes+3)/4) + int64(usage.Output)
	}
	if tokens < 1 {
		tokens = 1
	}
	amount := roundHalfUp(tokens * estimateRate)
	if amount < 1 {
		amount = 1
	}
	return &amount, true, "estimate"
}

func pricedMicroUSD(usage Usage, price Price, found bool) *int64 {
	if !usage.Arrived {
		return nil
	}
	if !found {
		return nil
	}
	var numerator int64
	add := func(n int, rate *int64) bool {
		if n == 0 {
			return true
		}
		if rate == nil {
			return false
		}
		numerator += int64(n) * *rate
		return true
	}
	if !add(usage.Input, price.InputMicroUSDPerMTok) ||
		!add(usage.Output, price.OutputMicroUSDPerMTok) ||
		!add(usage.CacheRead, price.CacheReadMicroUSDPerMTok) ||
		!add(usage.CacheWrite, price.CacheWriteMicroUSDPerMTok) {
		return nil
	}
	amount := roundHalfUp(numerator)
	return &amount
}

func roundHalfUp(numerator int64) int64 {
	return (numerator + tokensPerMillion/2) / tokensPerMillion
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
