package domain

import (
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
	embed := rows[1]
	if embed.Model != "embed-only" || len(embed.Caps) != 0 {
		t.Fatalf("embed = %+v", embed)
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
