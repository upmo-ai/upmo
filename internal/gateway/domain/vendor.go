package domain

import (
	"net/url"
	"slices"
	"strings"
)

// Vendor is what the gateway knows about one provider in the picker: the
// adapter that reaches it, the address it lives at, and the paths added to an
// account's base URL to call it. The web picker holds the same table (its
// default base URLs and adapter keys), and both are tested against the same
// vendor list.
type Vendor struct {
	// Key is the picker id.
	Key     string
	Name    string
	Adapter string
	// BaseURL is the vendor's root without a trailing slash. A path below the
	// host that every call needs (such as /v1beta/openai) is part of it.
	BaseURL string
	// LegacyHosts are hosts the vendor lived on before a move. Accounts saved
	// on one still resolve to this vendor, so they keep their compat paths and
	// pricing after the BaseURL changes. Entries are bare, lower-case host
	// names as hostOf returns them.
	LegacyHosts   []string
	SuggestedPlan string
	ChatPath      string
	ModelsPath    string
	// EmbedPath is empty for a vendor on a native adapter.
	EmbedPath string
	// Local marks a runtime on the person's own machine. Its BaseURL is only
	// a suggestion, and its host says nothing about which vendor it is.
	Local bool
}

var vendors = []Vendor{
	{Key: "anthropic", Name: "Anthropic", Adapter: AdapterAnthropic, BaseURL: "https://api.anthropic.com", SuggestedPlan: PlanMetered, ChatPath: "/v1/messages", ModelsPath: "/v1/models"},
	{Key: "openai", Name: "OpenAI", Adapter: AdapterOpenAI, BaseURL: "https://api.openai.com", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "gemini", Name: "Google Gemini", Adapter: AdapterOpenAI, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", SuggestedPlan: PlanMetered, ChatPath: "/chat/completions", ModelsPath: "/models", EmbedPath: "/embeddings"},
	{Key: "nebius", Name: "Nebius", Adapter: AdapterOpenAI, BaseURL: "https://api.tokenfactory.nebius.com", LegacyHosts: []string{"api.studio.nebius.ai"}, SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "ollama", Name: "Ollama", Adapter: AdapterOpenAI, BaseURL: "http://localhost:11434", SuggestedPlan: PlanLocal, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings", Local: true},
	{Key: "deepseek", Name: "DeepSeek", Adapter: AdapterOpenAI, BaseURL: "https://api.deepseek.com", SuggestedPlan: PlanMetered, ChatPath: "/chat/completions", ModelsPath: "/models", EmbedPath: "/embeddings"},
	{Key: "groq", Name: "Groq", Adapter: AdapterOpenAI, BaseURL: "https://api.groq.com/openai", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "xai", Name: "xAI", Adapter: AdapterOpenAI, BaseURL: "https://api.x.ai", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "openrouter", Name: "OpenRouter", Adapter: AdapterOpenAI, BaseURL: "https://openrouter.ai/api", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "mistral", Name: "Mistral", Adapter: AdapterOpenAI, BaseURL: "https://api.mistral.ai", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "together", Name: "Together AI", Adapter: AdapterOpenAI, BaseURL: "https://api.together.xyz", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "cerebras", Name: "Cerebras", Adapter: AdapterOpenAI, BaseURL: "https://api.cerebras.ai", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "fireworks", Name: "Fireworks AI", Adapter: AdapterOpenAI, BaseURL: "https://api.fireworks.ai/inference", SuggestedPlan: PlanMetered, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "perplexity", Name: "Perplexity", Adapter: AdapterOpenAI, BaseURL: "https://api.perplexity.ai", SuggestedPlan: PlanMetered, ChatPath: "/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "cohere", Name: "Cohere", Adapter: AdapterOpenAI, BaseURL: "https://api.cohere.ai/compatibility/v1", SuggestedPlan: PlanMetered, ChatPath: "/chat/completions", ModelsPath: "/models", EmbedPath: "/embeddings"},
	{Key: "lmstudio", Name: "LM Studio", Adapter: AdapterOpenAI, BaseURL: "http://localhost:1234", SuggestedPlan: PlanLocal, ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings", Local: true},
}

// modelsDevVendors maps models.dev top-level provider ids to picker vendor
// keys. Providers not in this table contribute only to the model-only fallback
// row in the catalogue.
var modelsDevVendors = map[string]string{
	"anthropic":    "anthropic",
	"openai":       "openai",
	"google":       "gemini",
	"nebius":       "nebius",
	"ollama":       "ollama",
	"deepseek":     "deepseek",
	"groq":         "groq",
	"xai":          "xai",
	"openrouter":   "openrouter",
	"mistral":      "mistral",
	"togetherai":   "together",
	"cerebras":     "cerebras",
	"fireworks-ai": "fireworks",
	"perplexity":   "perplexity",
	"cohere":       "cohere",
	"lmstudio":     "lmstudio",
}

// VendorForModelsDev maps a models.dev provider id to its picker vendor key.
// Unknown providers return ("", false).
func VendorForModelsDev(providerID string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(providerID))
	key, ok := modelsDevVendors[id]
	return key, ok
}

// VendorKey normalises a provider label into a canonical vendor key.
func VendorKey(raw string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	if v, ok := VendorFor(key); ok {
		return v.Key
	}
	return key
}

// legacyPaths are the paths every OpenAI-compatible account used before
// vendors had their own. An account on a host that is no known vendor's keeps
// them, so a proxy or a self-hosted server works as it did.
var legacyPaths = Vendor{ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"}

// Vendors returns every picker vendor, in picker order.
func Vendors() []Vendor {
	out := make([]Vendor, len(vendors))
	for i, v := range vendors {
		out[i] = v.detached()
	}
	return out
}

// VendorFor finds a vendor by its picker id, without regard to case.
func VendorFor(key string) (Vendor, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, v := range vendors {
		if v.Key == key {
			return v.detached(), true
		}
	}
	return Vendor{}, false
}

// detached returns v with its LegacyHosts copied, so a vendor handed to a
// caller shares no slice with the table.
func (v Vendor) detached() Vendor {
	v.LegacyHosts = slices.Clone(v.LegacyHosts)
	return v
}

// hostOf is the lower-case host name of a URL, without port, or "" when the
// text is not a URL with a host.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

// livesOnHost reports whether host is one v is reached on: the host of its
// BaseURL or a legacy host it moved away from.
func (v Vendor) livesOnHost(host string) bool {
	return hostOf(v.BaseURL) == host || slices.Contains(v.LegacyHosts, host)
}

// CompatPaths returns the OpenAI-compatible paths to call under baseURL. The
// vendor is told by the host: a base URL on a hosted vendor's host, including
// a legacy host it moved away from, gets that vendor's paths, so the account
// needs no stored vendor and a saved URL that is edited to another vendor
// follows. Any other host (a proxy, a local runtime, a vendor with its own
// native adapter such as anthropic, whose older accounts still use the
// compatible endpoint) gets the legacy /v1 paths.
func CompatPaths(baseURL string) Vendor {
	host := hostOf(baseURL)
	if host == "" {
		return legacyPaths
	}
	for _, v := range vendors {
		if !v.Local && v.Adapter == AdapterOpenAI && v.livesOnHost(host) {
			return v.detached()
		}
	}
	return legacyPaths
}

// PricingVendor returns the canonical catalogue vendor key for the endpoint
// actually called by account, never trusting its mutable provider label.
func PricingVendor(account Account) string {
	return PricingVendorFor(account.Adapter, account.BaseURL)
}

// PricingVendorFor returns the canonical catalogue vendor key for the endpoint
// actually called by adapter and baseURL:
//   - AdapterOpenAI resolves to the hosted vendor whose host (its BaseURL
//     host or a legacy one) matches baseURL, regardless of that vendor's
//     default adapter;
//   - AdapterAnthropic resolves to "anthropic" only on Anthropic's host;
//   - local vendors, unrecognised hosts, and other adapters return "".
func PricingVendorFor(adapter, baseURL string) string {
	host := hostOf(baseURL)
	if host == "" {
		return ""
	}
	switch adapter {
	case AdapterOpenAI:
		for _, v := range vendors {
			if !v.Local && v.livesOnHost(host) {
				return v.Key
			}
		}
		return ""
	case AdapterAnthropic:
		for _, v := range vendors {
			if !v.Local && v.Adapter == AdapterAnthropic && v.livesOnHost(host) {
				return v.Key
			}
		}
		return ""
	default:
		return ""
	}
}
