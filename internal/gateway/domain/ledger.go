package domain

import "math"

// CacheHitAlertBelow is the cache hit rate that should raise an alert (UPM-26).
const CacheHitAlertBelow = 0.7

// DefaultContextWindow is the fallback context window size in tokens when a
// model's catalog entry does not specify one.
const DefaultContextWindow = 128000

const tokensPerMillion = 1_000_000

// Principal is the actor triple for a model call, plus an optional parent
// run used to roll tokens up to a task.
type Principal struct {
	User   string
	Agent  string
	Run    string
	Parent string
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
	Version        string
	InputPerMTok   *float64
	OutputPerMTok  *float64
	CacheReadMTok  *float64
	CacheWriteMTok *float64
	ContextWindow  int
}

// Entry is one ledger row, written per attempt.
type Entry struct {
	Route        string
	LinkPosition int
	AccountID    string
	Model        string
	PlanKind     string
	Class        FailureClass
	Principal    Principal
	Usage        Usage
	CostUSD      *float64
	PriceVersion string
	// OwnerTeamID is the team through which the person reached a shared
	// account, as it was when the attempt ran. It is empty for their own
	// account and for a workspace-wide one.
	OwnerTeamID string
}

// CostUSD is the metered cost for an attempt. It is nil when the plan is
// not metered, when usage never arrived, or when the catalog has no price
// for a used dimension. It is never 0 for an unknown cost.
func CostUSD(plan string, usage Usage, price Price, found bool) *float64 {
	if plan != PlanMetered {
		return nil
	}
	if !usage.Arrived {
		return nil
	}
	if !found {
		return nil
	}
	sum := 0.0
	add := func(n int, rate *float64) bool {
		if n == 0 {
			return true
		}
		if rate == nil {
			return false
		}
		sum += float64(n) * (*rate) / tokensPerMillion
		return true
	}
	if !add(usage.Input, price.InputPerMTok) ||
		!add(usage.Output, price.OutputPerMTok) ||
		!add(usage.CacheRead, price.CacheReadMTok) ||
		!add(usage.CacheWrite, price.CacheWriteMTok) {
		return nil
	}
	return &sum
}

// MicroUSD converts a USD cost to rounded micro-USD (1e-6 USD). Nil stays nil.
func MicroUSD(costUSD *float64) *int64 {
	if costUSD == nil {
		return nil
	}
	v := int64(math.Round(*costUSD * tokensPerMillion))
	return &v
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
