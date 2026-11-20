package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/kernel"
)

// Minimum catalog counts required to accept a refresh or snapshot.
const (
	MinCatalogVendors = 10
	MinCatalogModels  = 100
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
// sorted order so duplicate model ids are deterministic across runs: vendor
// rows keep the first provider's price/limits, the model-only fallback row keeps
// the highest rate seen per dimension across all providers listing that id, and
// capabilities/modalities are unioned.
func ParseCatalog(r io.Reader, version string) ([]CatalogRow, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, fmt.Errorf("gateway: catalog version")
	}
	var root map[string]apiProvider
	dec := json.NewDecoder(r)
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("gateway: catalog: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("gateway: catalog: empty document")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("gateway: catalog: trailing data")
		}
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
			parsed, err := parseAPIModel(id, version, model)
			if err != nil {
				return nil, err
			}
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
				mergeCatalogFallback(existing, parsed)
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

// ValidateCatalog checks that a parsed catalog meets sanity bounds on every
// rate, minimum vendor and model counts, and at most a 25% row drop from
// prevCount (when prevCount > 0).
func ValidateCatalog(rows []CatalogRow, prevCount int) error {
	if len(rows) == 0 {
		return fmt.Errorf("gateway: catalog is empty")
	}
	vendors := map[string]bool{}
	models := map[string]bool{}
	checkRate := func(model, field string, micro *int64, usd *float64) error {
		if micro != nil {
			if *micro < 0 || *micro > MaxMicroUSD {
				return fmt.Errorf("gateway: catalog rate %s.%s out of bounds", model, field)
			}
			return nil
		}
		if usd != nil {
			if *usd < 0 || *usd > float64(MaxMicroUSD/kernel.MicroUSDPerDollar) {
				return fmt.Errorf("gateway: catalog rate %s.%s out of bounds", model, field)
			}
		}
		return nil
	}
	for _, row := range rows {
		if row.Vendor != "" {
			vendors[row.Vendor] = true
		}
		if row.Model != "" {
			models[row.Model] = true
		}
		if err := checkRate(row.Model, "input", row.Price.InputMicroUSDPerMTok, row.Price.InputPerMTok); err != nil {
			return err
		}
		if err := checkRate(row.Model, "output", row.Price.OutputMicroUSDPerMTok, row.Price.OutputPerMTok); err != nil {
			return err
		}
		if err := checkRate(row.Model, "cache_read", row.Price.CacheReadMicroUSDPerMTok, row.Price.CacheReadMTok); err != nil {
			return err
		}
		if err := checkRate(row.Model, "cache_write", row.Price.CacheWriteMicroUSDPerMTok, row.Price.CacheWriteMTok); err != nil {
			return err
		}
	}
	if len(vendors) < MinCatalogVendors {
		return fmt.Errorf("gateway: catalog has %d mapped vendors, want at least %d", len(vendors), MinCatalogVendors)
	}
	if len(models) < MinCatalogModels {
		return fmt.Errorf("gateway: catalog has %d models, want at least %d", len(models), MinCatalogModels)
	}
	if prevCount > 0 && len(rows)*100 < prevCount*75 {
		return fmt.Errorf("gateway: catalog shrunk from %d to %d rows (>25%% drop)", prevCount, len(rows))
	}
	return nil
}

func parseCatalogRate(raw json.RawMessage) (*int64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	micro, ok := ParseDecimalMicroUSD(trimmed, kernel.MicroUSDPerDollar)
	if !ok || micro < 0 || micro > MaxMicroUSD {
		return nil, fmt.Errorf("gateway: invalid catalog rate")
	}
	return &micro, nil
}

func parseAPIModel(id, version string, model apiModel) (CatalogRow, error) {
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
		var err error
		if price.InputMicroUSDPerMTok, err = parseCatalogRate(model.Cost.Input); err != nil {
			return CatalogRow{}, fmt.Errorf("gateway: model %s input: %w", id, err)
		}
		if price.OutputMicroUSDPerMTok, err = parseCatalogRate(model.Cost.Output); err != nil {
			return CatalogRow{}, fmt.Errorf("gateway: model %s output: %w", id, err)
		}
		if price.CacheReadMicroUSDPerMTok, err = parseCatalogRate(model.Cost.CacheRead); err != nil {
			return CatalogRow{}, fmt.Errorf("gateway: model %s cache_read: %w", id, err)
		}
		if price.CacheWriteMicroUSDPerMTok, err = parseCatalogRate(model.Cost.CacheWrite); err != nil {
			return CatalogRow{}, fmt.Errorf("gateway: model %s cache_write: %w", id, err)
		}
		price.InputPerMTok = USDFromMicro(price.InputMicroUSDPerMTok)
		price.OutputPerMTok = USDFromMicro(price.OutputMicroUSDPerMTok)
		price.CacheReadMTok = USDFromMicro(price.CacheReadMicroUSDPerMTok)
		price.CacheWriteMTok = USDFromMicro(price.CacheWriteMicroUSDPerMTok)
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
	}, nil
}

func mergeCatalogDuplicate(dst *CatalogRow, src CatalogRow) {
	dst.Caps = unionCaps(dst.Caps, src.Caps)
	dst.Inputs = unionValues(dst.Inputs, src.Inputs, allowedInputOrder)
	dst.Outputs = unionValues(dst.Outputs, src.Outputs, allowedOutputOrder)
	if !dst.Reasoning && src.Reasoning {
		dst.Reasoning = true
	}
}

func mergeCatalogFallback(dst *CatalogRow, src CatalogRow) {
	mergeCatalogDuplicate(dst, src)
	dst.Price.InputMicroUSDPerMTok = maxRatePtr(dst.Price.InputMicroUSDPerMTok, src.Price.InputMicroUSDPerMTok)
	dst.Price.OutputMicroUSDPerMTok = maxRatePtr(dst.Price.OutputMicroUSDPerMTok, src.Price.OutputMicroUSDPerMTok)
	dst.Price.CacheReadMicroUSDPerMTok = maxRatePtr(dst.Price.CacheReadMicroUSDPerMTok, src.Price.CacheReadMicroUSDPerMTok)
	dst.Price.CacheWriteMicroUSDPerMTok = maxRatePtr(dst.Price.CacheWriteMicroUSDPerMTok, src.Price.CacheWriteMicroUSDPerMTok)
	dst.Price.InputPerMTok = USDFromMicro(dst.Price.InputMicroUSDPerMTok)
	dst.Price.OutputPerMTok = USDFromMicro(dst.Price.OutputMicroUSDPerMTok)
	dst.Price.CacheReadMTok = USDFromMicro(dst.Price.CacheReadMicroUSDPerMTok)
	dst.Price.CacheWriteMTok = USDFromMicro(dst.Price.CacheWriteMicroUSDPerMTok)
}

func maxRatePtr(a, b *int64) *int64 {
	if b == nil {
		return a
	}
	if a == nil || *b > *a {
		v := *b
		return &v
	}
	return a
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func cloneCatalogRow(r CatalogRow) CatalogRow {
	r.Inputs = slices.Clone(r.Inputs)
	r.Outputs = slices.Clone(r.Outputs)
	r.Caps = slices.Clone(r.Caps)
	r.Price.InputMicroUSDPerMTok = cloneInt64Ptr(r.Price.InputMicroUSDPerMTok)
	r.Price.OutputMicroUSDPerMTok = cloneInt64Ptr(r.Price.OutputMicroUSDPerMTok)
	r.Price.CacheReadMicroUSDPerMTok = cloneInt64Ptr(r.Price.CacheReadMicroUSDPerMTok)
	r.Price.CacheWriteMicroUSDPerMTok = cloneInt64Ptr(r.Price.CacheWriteMicroUSDPerMTok)
	r.Price.InputPerMTok = USDFromMicro(r.Price.InputMicroUSDPerMTok)
	r.Price.OutputPerMTok = USDFromMicro(r.Price.OutputMicroUSDPerMTok)
	r.Price.CacheReadMTok = USDFromMicro(r.Price.CacheReadMicroUSDPerMTok)
	r.Price.CacheWriteMTok = USDFromMicro(r.Price.CacheWriteMicroUSDPerMTok)
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
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
	CacheRead  json.RawMessage `json:"cache_read"`
	CacheWrite json.RawMessage `json:"cache_write"`
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
