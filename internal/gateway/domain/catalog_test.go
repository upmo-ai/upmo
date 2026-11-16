package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	const body = `{
		"anthropic": {
			"models": {
				"claude-test": {
					"tool_call": true,
					"structured_output": true,
					"modalities": {"input": ["text", "image"]},
					"limit": {"context": 200000},
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
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	claude := rows[0]
	if claude.Model != "claude-test" {
		t.Fatalf("model = %q", claude.Model)
	}
	if !containsAll(claude.Caps, CapTools, CapVision, CapStructured) {
		t.Fatalf("caps = %v", claude.Caps)
	}
	if claude.Price.InputPerMTok == nil || *claude.Price.InputPerMTok != 3 {
		t.Fatalf("input price = %v", claude.Price.InputPerMTok)
	}
	if claude.ContextWindow != 200000 || claude.Price.ContextWindow != 200000 {
		t.Fatalf("claude context window = %d / %d, want 200000", claude.ContextWindow, claude.Price.ContextWindow)
	}
	embed := rows[1]
	if embed.Model != "embed-only" || len(embed.Caps) != 0 {
		t.Fatalf("embed = %+v", embed)
	}
	if embed.ContextWindow != DefaultContextWindow || embed.Price.ContextWindow != DefaultContextWindow {
		t.Fatalf("default context window = %d / %d, want %d", embed.ContextWindow, embed.Price.ContextWindow, DefaultContextWindow)
	}
}

func TestParseCatalogIsDeterministic(t *testing.T) {
	const body = `{
		"zeta": {"models": {"shared": {"tool_call": true, "cost": {"input": 9, "output": 9}}, "b": {}}},
		"alpha": {"models": {"shared": {"modalities": {"input": ["image"]}, "cost": {"input": 1, "output": 1}}, "a": {}}},
		"mid": {"models": {"shared": {"cost": {"input": 5, "output": 5}}}}
	}`
	first, err := ParseCatalog(strings.NewReader(body), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || first[0].Model != "a" || first[1].Model != "shared" || first[2].Model != "b" {
		t.Fatalf("rows = %+v", first)
	}
	if got := first[1].Price.InputPerMTok; got == nil || *got != 1 {
		t.Fatalf("shared price = %v, want the alphabetically first provider's", got)
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

func TestParseCatalogNeedsVersion(t *testing.T) {
	if _, err := ParseCatalog(strings.NewReader(`{}`), " "); err == nil {
		t.Fatal("expected version error")
	}
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
