package domain

import (
	"slices"
	"time"
)

// FieldSource names which source supplied a field on a merged account model.
type FieldSource string

const (
	SourceNone          FieldSource = "none"
	SourceOverride      FieldSource = "override"
	SourceProvider      FieldSource = "provider"
	SourceModelsDev     FieldSource = "models_dev"
	SourceOtherProvider FieldSource = "other_provider"
)

// ModelOverride holds optional per-account or workspace overrides (UPM-354).
// Nil fields leave the next source in precedence order active.
type ModelOverride struct {
	Kind           *ModelKind
	Inputs         []string
	Outputs        []string
	Tools          *bool
	Structured     *bool
	Reasoning      *bool
	ContextWindow  *int
	MaxOutput      *int
	InputPerMTok   *float64
	OutputPerMTok  *float64
	CacheReadMTok  *float64
	CacheWriteMTok *float64
	Lifecycle      *string
	RetiresAt      *time.Time
}

// ModelSources records which source supplied each field of a MergedModel.
type ModelSources struct {
	Kind           FieldSource `json:"kind"`
	Inputs         FieldSource `json:"inputs"`
	Outputs        FieldSource `json:"outputs"`
	Tools          FieldSource `json:"tools"`
	Structured     FieldSource `json:"structured"`
	Reasoning      FieldSource `json:"reasoning"`
	ContextWindow  FieldSource `json:"context_window"`
	MaxOutput      FieldSource `json:"max_output"`
	InputPerMTok   FieldSource `json:"input_usd_per_mtok"`
	OutputPerMTok  FieldSource `json:"output_usd_per_mtok"`
	CacheReadMTok  FieldSource `json:"cache_read_usd_per_mtok"`
	CacheWriteMTok FieldSource `json:"cache_write_usd_per_mtok"`
	Lifecycle      FieldSource `json:"lifecycle"`
	RetiresAt      FieldSource `json:"retires_at"`
}

// MergedModel is one account model with each field resolved in precedence order
// (override, provider's account_models_seen, models.dev catalog) alongside
// each field's recorded source.
type MergedModel struct {
	Model          string       `json:"model"`
	Kind           ModelKind    `json:"kind"`
	Inputs         []string     `json:"inputs"`
	Outputs        []string     `json:"outputs"`
	Tools          bool         `json:"tools"`
	Structured     bool         `json:"structured"`
	Reasoning      bool         `json:"reasoning"`
	ContextWindow  int          `json:"context_window"`
	MaxOutput      *int         `json:"max_output,omitempty"`
	InputPerMTok   *float64     `json:"input_usd_per_mtok,omitempty"`
	OutputPerMTok  *float64     `json:"output_usd_per_mtok,omitempty"`
	CacheReadMTok  *float64     `json:"cache_read_usd_per_mtok,omitempty"`
	CacheWriteMTok *float64     `json:"cache_write_usd_per_mtok,omitempty"`
	Lifecycle      string       `json:"lifecycle,omitempty"`
	RetiresAt      *time.Time   `json:"retires_at,omitempty"`
	Sources        ModelSources `json:"sources"`
}

// MergeModel resolves every field for one account model from override, seen
// (provider's account_models_seen), and catalog (models.dev). Any input may be
// nil.
func MergeModel(model string, override *ModelOverride, seen *SeenModel, catalog *CatalogRow) MergedModel {
	out := MergedModel{
		Model:   model,
		Inputs:  []string{},
		Outputs: []string{},
		Sources: ModelSources{
			Kind:           SourceNone,
			Inputs:         SourceNone,
			Outputs:        SourceNone,
			Tools:          SourceNone,
			Structured:     SourceNone,
			Reasoning:      SourceNone,
			ContextWindow:  SourceNone,
			MaxOutput:      SourceNone,
			InputPerMTok:   SourceNone,
			OutputPerMTok:  SourceNone,
			CacheReadMTok:  SourceNone,
			CacheWriteMTok: SourceNone,
			Lifecycle:      SourceNone,
			RetiresAt:      SourceNone,
		},
	}

	// Kind: override > models_dev.
	switch {
	case override != nil && override.Kind != nil && *override.Kind != "":
		out.Kind = *override.Kind
		out.Sources.Kind = SourceOverride
	case catalog != nil && catalog.Kind != "":
		out.Kind = catalog.Kind
		out.Sources.Kind = SourceModelsDev
	}

	// Inputs: override > provider > models_dev.
	switch {
	case override != nil && override.Inputs != nil:
		out.Inputs = NormalizeInputs(override.Inputs)
		if out.Inputs == nil {
			out.Inputs = []string{}
		}
		out.Sources.Inputs = SourceOverride
	case seen != nil && seen.Modalities != nil && len(seen.Modalities.Input) > 0:
		if norm := NormalizeInputs(seen.Modalities.Input); len(norm) > 0 {
			out.Inputs = norm
			out.Sources.Inputs = SourceProvider
		}
	}
	if out.Sources.Inputs == SourceNone && catalog != nil && len(catalog.Inputs) > 0 {
		out.Inputs = slices.Clone(catalog.Inputs)
		out.Sources.Inputs = SourceModelsDev
	}

	// Outputs: override > provider > models_dev.
	switch {
	case override != nil && override.Outputs != nil:
		out.Outputs = NormalizeOutputs(override.Outputs)
		if out.Outputs == nil {
			out.Outputs = []string{}
		}
		out.Sources.Outputs = SourceOverride
	case seen != nil && seen.Modalities != nil && len(seen.Modalities.Output) > 0:
		if norm := NormalizeOutputs(seen.Modalities.Output); len(norm) > 0 {
			out.Outputs = norm
			out.Sources.Outputs = SourceProvider
		}
	}
	if out.Sources.Outputs == SourceNone && catalog != nil && len(catalog.Outputs) > 0 {
		out.Outputs = slices.Clone(catalog.Outputs)
		out.Sources.Outputs = SourceModelsDev
	}

	// Tools, Structured, Reasoning: override > models_dev.
	switch {
	case override != nil && override.Tools != nil:
		out.Tools = *override.Tools
		out.Sources.Tools = SourceOverride
	case catalog != nil:
		out.Tools = slices.Contains(catalog.Caps, CapTools)
		out.Sources.Tools = SourceModelsDev
	}

	switch {
	case override != nil && override.Structured != nil:
		out.Structured = *override.Structured
		out.Sources.Structured = SourceOverride
	case catalog != nil:
		out.Structured = slices.Contains(catalog.Caps, CapStructured)
		out.Sources.Structured = SourceModelsDev
	}

	switch {
	case override != nil && override.Reasoning != nil:
		out.Reasoning = *override.Reasoning
		out.Sources.Reasoning = SourceOverride
	case catalog != nil:
		out.Reasoning = catalog.Reasoning
		out.Sources.Reasoning = SourceModelsDev
	}

	// ContextWindow: override > provider > models_dev.
	switch {
	case override != nil && override.ContextWindow != nil && *override.ContextWindow > 0:
		out.ContextWindow = *override.ContextWindow
		out.Sources.ContextWindow = SourceOverride
	case seen != nil && seen.ContextLength > 0:
		out.ContextWindow = seen.ContextLength
		out.Sources.ContextWindow = SourceProvider
	case catalog != nil && catalog.ContextWindow > 0:
		out.ContextWindow = catalog.ContextWindow
		out.Sources.ContextWindow = SourceModelsDev
	}

	// MaxOutput: override > models_dev.
	switch {
	case override != nil && override.MaxOutput != nil && *override.MaxOutput > 0:
		v := *override.MaxOutput
		out.MaxOutput = &v
		out.Sources.MaxOutput = SourceOverride
	case catalog != nil && catalog.MaxOutput > 0:
		v := catalog.MaxOutput
		out.MaxOutput = &v
		out.Sources.MaxOutput = SourceModelsDev
	}

	// Pricing rates: override > provider (per-token USD -> USD/MTok) > models_dev
	// (or other_provider when catalog is the cross-provider '' fallback row).
	var catIn, catOut, catCR, catCW *float64
	catPriceSource := SourceModelsDev
	if catalog != nil {
		catIn = catalog.Price.InputPerMTok
		catOut = catalog.Price.OutputPerMTok
		catCR = catalog.Price.CacheReadMTok
		catCW = catalog.Price.CacheWriteMTok
		if catalog.Vendor == "" {
			catPriceSource = SourceOtherProvider
		}
	}
	var seenPrice Price
	if seen != nil {
		seenPrice, _ = ParseSeenPricingMicroUSD(seen.Pricing)
	}
	out.InputPerMTok, out.Sources.InputPerMTok = resolveRate(
		overrideRate(override, func(o *ModelOverride) *float64 { return o.InputPerMTok }),
		seenPrice.InputPerMTok,
		catIn,
		catPriceSource,
	)
	out.OutputPerMTok, out.Sources.OutputPerMTok = resolveRate(
		overrideRate(override, func(o *ModelOverride) *float64 { return o.OutputPerMTok }),
		seenPrice.OutputPerMTok,
		catOut,
		catPriceSource,
	)
	out.CacheReadMTok, out.Sources.CacheReadMTok = resolveRate(
		overrideRate(override, func(o *ModelOverride) *float64 { return o.CacheReadMTok }),
		seenPrice.CacheReadMTok,
		catCR,
		catPriceSource,
	)
	out.CacheWriteMTok, out.Sources.CacheWriteMTok = resolveRate(
		overrideRate(override, func(o *ModelOverride) *float64 { return o.CacheWriteMTok }),
		seenPrice.CacheWriteMTok,
		catCW,
		catPriceSource,
	)

	// Lifecycle and RetiresAt: override > models_dev.
	switch {
	case override != nil && override.Lifecycle != nil && normalizeLifecycle(*override.Lifecycle) != "":
		out.Lifecycle = normalizeLifecycle(*override.Lifecycle)
		out.Sources.Lifecycle = SourceOverride
	case catalog != nil && catalog.Lifecycle != "":
		out.Lifecycle = catalog.Lifecycle
		out.Sources.Lifecycle = SourceModelsDev
	}

	switch {
	case override != nil && override.RetiresAt != nil:
		t := *override.RetiresAt
		out.RetiresAt = &t
		out.Sources.RetiresAt = SourceOverride
	case catalog != nil && catalog.RetiresAt != nil:
		t := *catalog.RetiresAt
		out.RetiresAt = &t
		out.Sources.RetiresAt = SourceModelsDev
	}

	return out
}

func overrideRate(o *ModelOverride, get func(*ModelOverride) *float64) *float64 {
	if o == nil {
		return nil
	}
	return get(o)
}

func resolveRate(ov, prov, cat *float64, catSource FieldSource) (*float64, FieldSource) {
	switch {
	case ov != nil:
		v := *ov
		return &v, SourceOverride
	case prov != nil:
		v := *prov
		return &v, SourceProvider
	case cat != nil:
		v := *cat
		return &v, catSource
	default:
		return nil, SourceNone
	}
}
