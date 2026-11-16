package domain

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// CatalogRow is one model after a models.dev refresh.
type CatalogRow struct {
	Model         string
	Version       string
	Caps          []string
	Price         Price
	ContextWindow int
}

// ParseCatalog reads a models.dev api.json document into catalog rows.
// Capabilities map to CapTools, CapVision and CapStructured. Providers and
// models are read in sorted order, so the rows and the winning price for a
// duplicate model id are the same on every run. Duplicate model ids keep the
// first price and union the capabilities.
func ParseCatalog(r io.Reader, version string) ([]CatalogRow, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, fmt.Errorf("gateway: catalog version")
	}
	var root map[string]apiProvider
	if err := json.NewDecoder(r).Decode(&root); err != nil {
		return nil, fmt.Errorf("gateway: catalog: %w", err)
	}
	byModel := map[string]*CatalogRow{}
	order := []string{}
	for _, name := range slices.Sorted(maps.Keys(root)) {
		provider := root[name]
		for _, key := range slices.Sorted(maps.Keys(provider.Models)) {
			model := provider.Models[key]
			id := strings.TrimSpace(key)
			if id == "" {
				continue
			}
			caps := model.caps()
			if row, ok := byModel[id]; ok {
				row.Caps = unionCaps(row.Caps, caps)
				continue
			}
			ctxWindow := DefaultContextWindow
			if model.Limit != nil && model.Limit.Context > 0 {
				ctxWindow = model.Limit.Context
			}
			price := Price{Version: version, ContextWindow: ctxWindow}
			if model.Cost != nil {
				price.InputPerMTok = floatPtr(model.Cost.Input)
				price.OutputPerMTok = floatPtr(model.Cost.Output)
				if model.Cost.CacheRead != nil {
					price.CacheReadMTok = floatPtr(*model.Cost.CacheRead)
				}
				if model.Cost.CacheWrite != nil {
					price.CacheWriteMTok = floatPtr(*model.Cost.CacheWrite)
				}
			}
			row := &CatalogRow{Model: id, Version: version, Caps: caps, Price: price, ContextWindow: ctxWindow}
			byModel[id] = row
			order = append(order, id)
		}
	}
	out := make([]CatalogRow, 0, len(order))
	for _, id := range order {
		out = append(out, *byModel[id])
	}
	return out, nil
}

type apiProvider struct {
	Models map[string]apiModel `json:"models"`
}

type apiModel struct {
	ToolCall         bool          `json:"tool_call"`
	StructuredOutput bool          `json:"structured_output"`
	Cost             *apiCost      `json:"cost"`
	Limit            *apiLimit     `json:"limit"`
	Modalities       apiModalities `json:"modalities"`
}

type apiLimit struct {
	Context int `json:"context"`
}

type apiCost struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

type apiModalities struct {
	Input []string `json:"input"`
}

func (m apiModel) caps() []string {
	var out []string
	if m.ToolCall {
		out = append(out, CapTools)
	}
	if m.StructuredOutput {
		out = append(out, CapStructured)
	}
	for _, in := range m.Modalities.Input {
		if strings.EqualFold(strings.TrimSpace(in), "image") {
			out = append(out, CapVision)
			break
		}
	}
	return out
}

func unionCaps(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range append(slices.Clone(a), b...) {
		c = strings.ToLower(strings.TrimSpace(c))
		if !knownCap(c) || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func floatPtr(v float64) *float64 {
	return &v
}
