package domain

import (
	"slices"
	"testing"
	"time"
)

func TestMergeModelSourcePrecedence(t *testing.T) {
	catIn := 3.0
	catOut := 15.0
	catCR := 0.3
	catCW := 3.75
	retireCat := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	catalog := &CatalogRow{
		Vendor:        "openrouter",
		Model:         "claude-sonnet-4",
		Version:       "v1",
		Kind:          KindChat,
		Inputs:        []string{InputText, InputImage},
		Outputs:       []string{OutputText},
		Caps:          []string{CapTools, CapStructured},
		ContextWindow: 200000,
		MaxOutput:     16000,
		Reasoning:     true,
		Lifecycle:     LifecycleActive,
		RetiresAt:     &retireCat,
		Price: Price{
			Version:        "v1",
			InputPerMTok:   &catIn,
			OutputPerMTok:  &catOut,
			CacheReadMTok:  &catCR,
			CacheWriteMTok: &catCW,
			ContextWindow:  200000,
		},
	}

	seen := &SeenModel{
		ID:            "claude-sonnet-4",
		ContextLength: 128000,
		Modalities: &ModelModalities{
			Input:  []string{"text", "image", "pdf"},
			Output: []string{"text", "audio"},
		},
		Pricing: map[string]string{
			"prompt":            "0.000004",
			"completion":        "0.000020",
			"input_cache_read":  "0.0000005",
			"input_cache_write": "0.000005",
		},
	}

	// 1. None: all inputs nil -> all sources are SourceNone.
	none := MergeModel("m-none", nil, nil, nil)
	if none.Sources.Kind != SourceNone ||
		none.Sources.Inputs != SourceNone ||
		none.Sources.Outputs != SourceNone ||
		none.Sources.Tools != SourceNone ||
		none.Sources.Structured != SourceNone ||
		none.Sources.Reasoning != SourceNone ||
		none.Sources.ContextWindow != SourceNone ||
		none.Sources.MaxOutput != SourceNone ||
		none.Sources.InputPerMTok != SourceNone ||
		none.Sources.OutputPerMTok != SourceNone ||
		none.Sources.CacheReadMTok != SourceNone ||
		none.Sources.CacheWriteMTok != SourceNone ||
		none.Sources.Lifecycle != SourceNone ||
		none.Sources.RetiresAt != SourceNone {
		t.Fatalf("expected all SourceNone, got %+v", none.Sources)
	}

	// 2. ModelsDev only -> every field supplied by catalog has SourceModelsDev.
	fromCat := MergeModel("claude-sonnet-4", nil, nil, catalog)
	if fromCat.Sources.Kind != SourceModelsDev ||
		fromCat.Sources.Inputs != SourceModelsDev ||
		fromCat.Sources.Outputs != SourceModelsDev ||
		fromCat.Sources.Tools != SourceModelsDev ||
		fromCat.Sources.Structured != SourceModelsDev ||
		fromCat.Sources.Reasoning != SourceModelsDev ||
		fromCat.Sources.ContextWindow != SourceModelsDev ||
		fromCat.Sources.MaxOutput != SourceModelsDev ||
		fromCat.Sources.InputPerMTok != SourceModelsDev ||
		fromCat.Sources.OutputPerMTok != SourceModelsDev ||
		fromCat.Sources.CacheReadMTok != SourceModelsDev ||
		fromCat.Sources.CacheWriteMTok != SourceModelsDev ||
		fromCat.Sources.Lifecycle != SourceModelsDev ||
		fromCat.Sources.RetiresAt != SourceModelsDev {
		t.Fatalf("expected all SourceModelsDev, got %+v", fromCat.Sources)
	}
	if fromCat.ContextWindow != 200000 || *fromCat.InputPerMTok != 3.0 || *fromCat.MaxOutput != 16000 {
		t.Fatalf("unexpected catalog values: %+v", fromCat)
	}

	// 3. Provider beats models.dev on fields it supplies; models.dev fills the rest.
	fromProv := MergeModel("claude-sonnet-4", nil, seen, catalog)
	if fromProv.Sources.Inputs != SourceProvider ||
		fromProv.Sources.Outputs != SourceProvider ||
		fromProv.Sources.ContextWindow != SourceProvider ||
		fromProv.Sources.InputPerMTok != SourceProvider ||
		fromProv.Sources.OutputPerMTok != SourceProvider ||
		fromProv.Sources.CacheReadMTok != SourceProvider ||
		fromProv.Sources.CacheWriteMTok != SourceProvider {
		t.Fatalf("expected provider to beat models_dev: %+v", fromProv.Sources)
	}
	if fromProv.Sources.Kind != SourceModelsDev ||
		fromProv.Sources.Tools != SourceModelsDev ||
		fromProv.Sources.Structured != SourceModelsDev ||
		fromProv.Sources.Reasoning != SourceModelsDev ||
		fromProv.Sources.MaxOutput != SourceModelsDev ||
		fromProv.Sources.Lifecycle != SourceModelsDev ||
		fromProv.Sources.RetiresAt != SourceModelsDev {
		t.Fatalf("expected models_dev fallback for remaining fields: %+v", fromProv.Sources)
	}
	if !slices.Equal(fromProv.Inputs, []string{InputText, InputImage, InputFile}) {
		t.Fatalf("provider inputs = %v", fromProv.Inputs)
	}
	if fromProv.ContextWindow != 128000 || *fromProv.InputPerMTok != 4.0 || *fromProv.OutputPerMTok != 20.0 {
		t.Fatalf("provider values = %+v", fromProv)
	}

	// 4. Override beats both provider and models.dev on every overridden field.
	ovKind := KindImage
	ovBool := false
	ovCtx := 64000
	ovMaxOut := 8000
	ovIn := 1.25
	ovOut := 6.5
	ovCR := 0.1
	ovCW := 1.5
	ovLifecycle := LifecycleDeprecated
	ovRetire := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	override := &ModelOverride{
		Kind:           &ovKind,
		Inputs:         []string{InputText},
		Outputs:        []string{OutputImage},
		Tools:          &ovBool,
		Structured:     &ovBool,
		Reasoning:      &ovBool,
		ContextWindow:  &ovCtx,
		MaxOutput:      &ovMaxOut,
		InputPerMTok:   &ovIn,
		OutputPerMTok:  &ovOut,
		CacheReadMTok:  &ovCR,
		CacheWriteMTok: &ovCW,
		Lifecycle:      &ovLifecycle,
		RetiresAt:      &ovRetire,
	}
	fromOv := MergeModel("claude-sonnet-4", override, seen, catalog)
	if fromOv.Sources.Kind != SourceOverride ||
		fromOv.Sources.Inputs != SourceOverride ||
		fromOv.Sources.Outputs != SourceOverride ||
		fromOv.Sources.Tools != SourceOverride ||
		fromOv.Sources.Structured != SourceOverride ||
		fromOv.Sources.Reasoning != SourceOverride ||
		fromOv.Sources.ContextWindow != SourceOverride ||
		fromOv.Sources.MaxOutput != SourceOverride ||
		fromOv.Sources.InputPerMTok != SourceOverride ||
		fromOv.Sources.OutputPerMTok != SourceOverride ||
		fromOv.Sources.CacheReadMTok != SourceOverride ||
		fromOv.Sources.CacheWriteMTok != SourceOverride ||
		fromOv.Sources.Lifecycle != SourceOverride ||
		fromOv.Sources.RetiresAt != SourceOverride {
		t.Fatalf("expected all SourceOverride, got %+v", fromOv.Sources)
	}
	if fromOv.Kind != KindImage || !slices.Equal(fromOv.Inputs, []string{InputText}) ||
		!slices.Equal(fromOv.Outputs, []string{OutputImage}) ||
		fromOv.Tools || fromOv.Structured || fromOv.Reasoning ||
		fromOv.ContextWindow != 64000 || *fromOv.MaxOutput != 8000 ||
		*fromOv.InputPerMTok != 1.25 || fromOv.Lifecycle != LifecycleDeprecated {
		t.Fatalf("unexpected override values: %+v", fromOv)
	}
}
