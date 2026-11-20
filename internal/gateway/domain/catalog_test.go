package domain

import (
	"bytes"
	_ "embed"
	"reflect"
	"slices"
	"strings"
	"testing"
)

//go:embed testdata/models_dev.json
var recordedModelsDev []byte

func TestParseCatalog(t *testing.T) {
	const body = `{
		"anthropic": {
			"models": {
				"claude-test": {
					"tool_call": true,
					"structured_output": true,
					"reasoning": true,
					"status": "active",
					"modalities": {"input": ["text", "image", "pdf"], "output": ["text"]},
					"limit": {"context": 200000, "output": 16000},
					"cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75}
				}
			}
		},
		"other": {
			"models": {
				"claude-test": {
					"tool_call": true,
					"modalities": {"input": ["text"]}
				},
				"embed-only": {
					"type": "embedding",
					"tool_call": false,
					"modalities": {"input": ["text"]},
					"cost": {"input": 0.1, "output": 0}
				}
			}
		}
	}`
	rows, err := ParseCatalog(strings.NewReader(body), "v1")
	if err != nil {
		t.Fatal(err)
	}
	// Expect 3 rows: ("", "claude-test"), ("", "embed-only"), ("anthropic", "claude-test").
	if len(rows) != 3 {
		t.Fatalf("rows = %d: %+v", len(rows), rows)
	}
	fbClaude, ok := findRow(rows, "", "claude-test")
	if !ok {
		t.Fatalf("missing fallback claude-test: %+v", rows)
	}
	if !containsAll(fbClaude.Caps, CapTools, CapStructured) || slices.Contains(fbClaude.Caps, CapVision) {
		t.Fatalf("caps = %v (vision must live on Inputs, not Caps)", fbClaude.Caps)
	}
	if !slices.Equal(fbClaude.Inputs, []string{InputText, InputImage, InputFile}) {
		t.Fatalf("inputs = %v", fbClaude.Inputs)
	}
	if !slices.Equal(fbClaude.Outputs, []string{OutputText}) {
		t.Fatalf("outputs = %v", fbClaude.Outputs)
	}
	if fbClaude.Kind != KindChat || fbClaude.MaxOutput != 16000 || !fbClaude.Reasoning || fbClaude.Lifecycle != LifecycleActive {
		t.Fatalf("fbClaude = %+v", fbClaude)
	}
	if fbClaude.Price.InputPerMTok == nil || *fbClaude.Price.InputPerMTok != 3 {
		t.Fatalf("input price = %v", fbClaude.Price.InputPerMTok)
	}
	if fbClaude.ContextWindow != 200000 || fbClaude.Price.ContextWindow != 200000 {
		t.Fatalf("claude context window = %d / %d, want 200000", fbClaude.ContextWindow, fbClaude.Price.ContextWindow)
	}

	anthClaude, ok := findRow(rows, "anthropic", "claude-test")
	if !ok || anthClaude.Vendor != "anthropic" || anthClaude.MaxOutput != 16000 {
		t.Fatalf("anthClaude = %+v (ok=%v)", anthClaude, ok)
	}

	embed, ok := findRow(rows, "", "embed-only")
	if !ok || embed.Kind != KindEmbedding || len(embed.Caps) != 0 {
		t.Fatalf("embed = %+v (ok=%v)", embed, ok)
	}
	if embed.ContextWindow != DefaultContextWindow || embed.Price.ContextWindow != DefaultContextWindow {
		t.Fatalf("default context window = %d / %d, want %d", embed.ContextWindow, embed.Price.ContextWindow, DefaultContextWindow)
	}
	// "other" has no picker vendor mapping, so it contributes only to the fallback row.
	if _, ok := findRow(rows, "other", "embed-only"); ok {
		t.Fatal("unknown provider should not create a vendor-scoped row")
	}
}

func TestParseCatalogFixture(t *testing.T) {
	rows, err := ParseCatalog(bytes.NewReader(recordedModelsDev), "fixture-v1")
	if err != nil {
		t.Fatal(err)
	}

	// Same model id sold by two providers at different prices.
	oaiGPT4o, ok := findRow(rows, "openai", "gpt-4o")
	if !ok || oaiGPT4o.Price.InputPerMTok == nil || *oaiGPT4o.Price.InputPerMTok != 2.5 {
		t.Fatalf("openai gpt-4o = %+v (ok=%v)", oaiGPT4o, ok)
	}
	orGPT4o, ok := findRow(rows, "openrouter", "gpt-4o")
	if !ok || orGPT4o.Price.InputPerMTok == nil || *orGPT4o.Price.InputPerMTok != 2.75 {
		t.Fatalf("openrouter gpt-4o = %+v (ok=%v)", orGPT4o, ok)
	}
	if _, ok := findRow(rows, "", "gpt-4o"); !ok {
		t.Fatal("missing fallback row for gpt-4o")
	}

	// Google maps to picker vendor "gemini".
	gemFlash, ok := findRow(rows, "gemini", "gemini-2.5-flash")
	if !ok || !slices.Equal(gemFlash.Inputs, []string{InputText, InputImage, InputAudio, InputFile}) {
		t.Fatalf("gemini flash = %+v (ok=%v)", gemFlash, ok)
	}

	// Unmapped provider ("amazon-bedrock") contributes only to fallback.
	if _, ok := findRow(rows, "amazon-bedrock", "bedrock-only-model"); ok {
		t.Fatal("unmapped provider must not emit vendor row")
	}
	if _, ok := findRow(rows, "", "bedrock-only-model"); !ok {
		t.Fatal("missing fallback row for bedrock-only-model")
	}

	// Embedding model and deprecated lifecycle with retirement date.
	emb, ok := findRow(rows, "openai", "text-embedding-3-small")
	if !ok || emb.Kind != KindEmbedding || len(emb.Outputs) != 0 {
		t.Fatalf("embedding row = %+v (ok=%v)", emb, ok)
	}
	dep, ok := findRow(rows, "openai", "gpt-3.5-turbo")
	if !ok || dep.Lifecycle != LifecycleDeprecated || dep.RetiresAt == nil || dep.RetiresAt.Format("2006-01-02") != "2026-12-31" {
		t.Fatalf("deprecated row = %+v (ok=%v)", dep, ok)
	}

	// Image-input chat model.
	sonnet, ok := findRow(rows, "anthropic", "claude-sonnet-4")
	if !ok || sonnet.Kind != KindChat || !slices.Contains(sonnet.Inputs, InputImage) || !slices.Contains(sonnet.Inputs, InputFile) {
		t.Fatalf("sonnet = %+v (ok=%v)", sonnet, ok)
	}
}

func TestParseCatalogIsDeterministic(t *testing.T) {
	const body = `{
		"zeta": {"models": {"shared": {"tool_call": true, "cost": {"input": 9, "output": 9}}, "b": {}}},
		"openai": {"models": {"shared": {"modalities": {"input": ["image"]}, "cost": {"input": 1, "output": 1}}, "a": {}}},
		"mid": {"models": {"shared": {"cost": {"input": 5, "output": 5}}}}
	}`
	first, err := ParseCatalog(strings.NewReader(body), "v1")
	if err != nil {
		t.Fatal(err)
	}
	fb, ok := findRow(first, "", "shared")
	if !ok || fb.Price.InputPerMTok == nil || *fb.Price.InputPerMTok != 9 {
		t.Fatalf("shared fallback price = %+v (ok=%v), want 9 (highest rate across providers)", fb, ok)
	}
	if !slices.Contains(fb.Inputs, InputImage) || !slices.Contains(fb.Caps, CapTools) {
		t.Fatalf("shared fallback did not union inputs/caps: %+v", fb)
	}
	for range 50 {
		again, err := ParseCatalog(strings.NewReader(body), "v1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("rows differ between runs:\n%+v\n%+v", first, again)
		}
	}
}

func TestParseCatalogFallbackKeepsHighestRatePerDimension(t *testing.T) {
	const body = `{
		"alpha-unmapped": {
			"models": {
				"shared-open": {
					"cost": {"input": 1, "output": 20, "cache_read": 0.1}
				}
			}
		},
		"openai": {
			"models": {
				"shared-open": {
					"cost": {"input": 5, "output": 10, "cache_read": 0.5, "cache_write": 2}
				}
			}
		},
		"openrouter": {
			"models": {
				"shared-open": {
					"cost": {"input": 3, "output": 12, "cache_read": 0.9, "cache_write": 6}
				}
			}
		}
	}`
	rows, err := ParseCatalog(strings.NewReader(body), "v1")
	if err != nil {
		t.Fatal(err)
	}
	fb, ok := findRow(rows, "", "shared-open")
	if !ok {
		t.Fatal("missing fallback row for shared-open")
	}
	if *fb.Price.InputMicroUSDPerMTok != 5_000_000 ||
		*fb.Price.OutputMicroUSDPerMTok != 20_000_000 ||
		*fb.Price.CacheReadMicroUSDPerMTok != 900_000 ||
		*fb.Price.CacheWriteMicroUSDPerMTok != 6_000_000 {
		t.Fatalf("fallback row rates = %+v, want input=5M output=20M cache_read=900k cache_write=6M", fb.Price)
	}
	oai, ok := findRow(rows, "openai", "shared-open")
	if !ok || *oai.Price.InputMicroUSDPerMTok != 5_000_000 || *oai.Price.OutputMicroUSDPerMTok != 10_000_000 {
		t.Fatalf("openai row rates mutated = %+v", oai.Price)
	}
	orRow, ok := findRow(rows, "openrouter", "shared-open")
	if !ok || *orRow.Price.InputMicroUSDPerMTok != 3_000_000 || *orRow.Price.OutputMicroUSDPerMTok != 12_000_000 {
		t.Fatalf("openrouter row rates mutated = %+v", orRow.Price)
	}
}

func TestParseCatalogNeedsVersion(t *testing.T) {
	if _, err := ParseCatalog(strings.NewReader(`{}`), " "); err == nil {
		t.Fatal("expected version error")
	}
}

func TestParseCatalogRejectsTrailingData(t *testing.T) {
	valid := `{"openai":{"models":{"gpt-4o":{"cost":{"input":2.5,"output":10}}}}}`
	if _, err := ParseCatalog(strings.NewReader(valid+"  \n\t "), "v1"); err != nil {
		t.Fatalf("trailing whitespace should succeed, got %v", err)
	}
	for name, body := range map[string]string{
		"trailing-json":    valid + `{"extra":{}}`,
		"trailing-scalar":  valid + ` 123`,
		"trailing-garbage": valid + ` garbage`,
	} {
		if _, err := ParseCatalog(strings.NewReader(body), "v1"); err == nil {
			t.Fatalf("%s: expected error for trailing data, got nil", name)
		}
	}
}

func findRow(rows []CatalogRow, vendor, model string) (CatalogRow, bool) {
	for _, r := range rows {
		if r.Vendor == vendor && r.Model == model {
			return r, true
		}
	}
	return CatalogRow{}, false
}

func containsAll(have []string, need ...string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, n := range need {
		if !set[n] {
			return false
		}
	}
	return true
}
