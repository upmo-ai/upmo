package domain

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
)

// ModelKind is what a model is used for.
type ModelKind string

const (
	KindChat      ModelKind = "chat"
	KindEmbedding ModelKind = "embedding"
	KindImage     ModelKind = "image"
	KindRerank    ModelKind = "rerank"
)

// Allowed output modalities on a model.
const (
	OutputText  = "text"
	OutputImage = "image"
	OutputAudio = "audio"
)

// Allowed lifecycle states on a catalogue model.
const (
	LifecycleActive     = "active"
	LifecycleAlpha      = "alpha"
	LifecycleBeta       = "beta"
	LifecycleDeprecated = "deprecated"
	LifecycleRetired    = "retired"
)

// VendorModel keys a catalogue lookup by picker vendor and model id.
// An empty Vendor names the model-only fallback row.
type VendorModel struct {
	Vendor string
	Model  string
}

// CatalogRow is one model entry after a models.dev refresh. Vendor is the
// picker vendor key, or empty ("") for the model-only fallback row.
type CatalogRow struct {
	Vendor        string
	Model         string
	Version       string
	Kind          ModelKind
	Inputs        []string
	Outputs       []string
	Caps          []string
	Price         Price
	ContextWindow int
	MaxOutput     int
	Reasoning     bool
	Lifecycle     string
	RetiresAt     *time.Time
}

// ParseCatalog reads a models.dev api.json document into catalog rows.
// Providers with a matching picker vendor key produce one (vendor, model) row
// each. Every provider (including ones with no picker vendor) contributes to
// the model-only fallback row (Vendor: ""). Providers and models are read in
// sorted order so duplicate model ids are deterministic across runs: the first
// provider keeps its price/limits and capabilities/modalities are unioned.
func ParseCatalog(r io.Reader, version string) ([]CatalogRow, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, fmt.Errorf("gateway: catalog version")
	}
	var root map[string]apiProvider
	if err := json.NewDecoder(r).Decode(&root); err != nil {
		return nil, fmt.Errorf("gateway: catalog: %w", err)
	}
	byKey := map[VendorModel]*CatalogRow{}
	for _, name := range slices.Sorted(maps.Keys(root)) {
		provider := root[name]
		vendor, hasVendor := VendorForModelsDev(name)
		for _, key := range slices.Sorted(maps.Keys(provider.Models)) {
			model := provider.Models[key]
			id := strings.TrimSpace(key)
			if id == "" {
				continue
			}
			parsed := parseAPIModel(id, version, model)
			if hasVendor {
				k := VendorModel{Vendor: vendor, Model: id}
				if existing, ok := byKey[k]; ok {
					mergeCatalogDuplicate(existing, parsed)
				} else {
					cp := cloneCatalogRow(parsed)
					cp.Vendor = vendor
					byKey[k] = &cp
				}
			}
			fbKey := VendorModel{Vendor: "", Model: id}
			if existing, ok := byKey[fbKey]; ok {
				mergeCatalogDuplicate(existing, parsed)
			} else {
				cp := cloneCatalogRow(parsed)
				cp.Vendor = ""
				byKey[fbKey] = &cp
			}
		}
	}
	keys := make([]VendorModel, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b VendorModel) int {
		if c := strings.Compare(a.Vendor, b.Vendor); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})
	out := make([]CatalogRow, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byKey[k])
	}
	return out, nil
}

func parseAPIModel(id, version string, model apiModel) CatalogRow {
	outputs := NormalizeOutputs(model.Modalities.Output)
	kind := parseModelKind(model.Type, outputs)
	inputs := NormalizeInputs(model.Modalities.Input)
	if len(inputs) == 0 {
		inputs = []string{InputText}
	}
	if len(outputs) == 0 && kind == KindChat {
		outputs = []string{OutputText}
	}
	ctxWindow := DefaultContextWindow
	maxOutput := 0
	if model.Limit != nil {
		if model.Limit.Context > 0 {
			ctxWindow = model.Limit.Context
		}
		if model.Limit.Output > 0 {
			maxOutput = model.Limit.Output
		}
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
	retireRaw := strings.TrimSpace(model.RetiresAt)
	if retireRaw == "" {
		retireRaw = strings.TrimSpace(model.RetirementDate)
	}
	return CatalogRow{
		Model:         id,
		Version:       version,
		Kind:          kind,
		Inputs:        inputs,
		Outputs:       outputs,
		Caps:          model.caps(),
		Price:         price,
		ContextWindow: ctxWindow,
		MaxOutput:     maxOutput,
		Reasoning:     model.Reasoning,
		Lifecycle:     normalizeLifecycle(model.Status),
		RetiresAt:     parseRetireDate(retireRaw),
	}
}

func mergeCatalogDuplicate(dst *CatalogRow, src CatalogRow) {
	dst.Caps = unionCaps(dst.Caps, src.Caps)
	dst.Inputs = unionValues(dst.Inputs, src.Inputs, allowedInputOrder)
	dst.Outputs = unionValues(dst.Outputs, src.Outputs, allowedOutputOrder)
	if !dst.Reasoning && src.Reasoning {
		dst.Reasoning = true
	}
}

func cloneCatalogRow(r CatalogRow) CatalogRow {
	r.Inputs = slices.Clone(r.Inputs)
	r.Outputs = slices.Clone(r.Outputs)
	r.Caps = slices.Clone(r.Caps)
	if r.RetiresAt != nil {
		t := *r.RetiresAt
		r.RetiresAt = &t
	}
	return r
}

type apiProvider struct {
	Models map[string]apiModel `json:"models"`
}

type apiModel struct {
	Type             string        `json:"type"`
	ToolCall         bool          `json:"tool_call"`
	StructuredOutput bool          `json:"structured_output"`
	Reasoning        bool          `json:"reasoning"`
	Status           string        `json:"status"`
	RetiresAt        string        `json:"retires_at"`
	RetirementDate   string        `json:"retirement_date"`
	Cost             *apiCost      `json:"cost"`
	Limit            *apiLimit     `json:"limit"`
	Modalities       apiModalities `json:"modalities"`
}

type apiLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type apiCost struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

type apiModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

func (m apiModel) caps() []string {
	var out []string
	if m.ToolCall {
		out = append(out, CapTools)
	}
	if m.StructuredOutput {
		out = append(out, CapStructured)
	}
	return out
}

func parseModelKind(rawType string, outputs []string) ModelKind {
	switch strings.ToLower(strings.TrimSpace(rawType)) {
	case "embedding", "embeddings":
		return KindEmbedding
	case "image":
		return KindImage
	case "rerank", "reranker":
		return KindRerank
	case "chat":
		return KindChat
	case "":
		if len(outputs) > 0 && slices.Contains(outputs, OutputImage) && !slices.Contains(outputs, OutputText) {
			return KindImage
		}
		return KindChat
	default:
		return KindChat
	}
}

func normalizeLifecycle(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case LifecycleActive:
		return LifecycleActive
	case LifecycleAlpha:
		return LifecycleAlpha
	case LifecycleBeta:
		return LifecycleBeta
	case LifecycleDeprecated:
		return LifecycleDeprecated
	case LifecycleRetired:
		return LifecycleRetired
	default:
		return ""
	}
}

func parseRetireDate(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			return &d
		}
	}
	return nil
}

var (
	allowedInputOrder  = []string{InputText, InputImage, InputAudio, InputFile}
	allowedOutputOrder = []string{OutputText, OutputImage, OutputAudio}
)

// NormalizeInputs maps raw input modality names (such as "pdf" -> "file") to
// the allowed input values in canonical order.
func NormalizeInputs(raw []string) []string {
	seen := map[string]bool{}
	for _, item := range raw {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case InputText:
			seen[InputText] = true
		case InputImage:
			seen[InputImage] = true
		case InputAudio:
			seen[InputAudio] = true
		case InputFile, "pdf", "document":
			seen[InputFile] = true
		}
	}
	var out []string
	for _, k := range allowedInputOrder {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// NormalizeOutputs maps raw output modality names to the allowed output values
// in canonical order.
func NormalizeOutputs(raw []string) []string {
	seen := map[string]bool{}
	for _, item := range raw {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case OutputText:
			seen[OutputText] = true
		case OutputImage:
			seen[OutputImage] = true
		case OutputAudio:
			seen[OutputAudio] = true
		}
	}
	var out []string
	for _, k := range allowedOutputOrder {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out
}

func unionValues(a, b, order []string) []string {
	seen := map[string]bool{}
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		seen[v] = true
	}
	var out []string
	for _, k := range order {
		if seen[k] {
			out = append(out, k)
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
