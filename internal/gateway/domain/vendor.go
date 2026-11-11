package domain

import (
	"net/url"
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
	Adapter string
	// BaseURL is the vendor's root without a trailing slash. A path below the
	// host that every call needs (such as /v1beta/openai) is part of it.
	BaseURL    string
	ChatPath   string
	ModelsPath string
	// EmbedPath is empty for a vendor on a native adapter.
	EmbedPath string
	// Local marks a runtime on the person's own machine. Its BaseURL is only
	// a suggestion, and its host says nothing about which vendor it is.
	Local bool
}

var vendors = []Vendor{
	{Key: "anthropic", Adapter: AdapterAnthropic, BaseURL: "https://api.anthropic.com", ChatPath: "/v1/messages", ModelsPath: "/v1/models"},
	{Key: "openai", Adapter: AdapterOpenAI, BaseURL: "https://api.openai.com", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "gemini", Adapter: AdapterOpenAI, BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", ChatPath: "/chat/completions", ModelsPath: "/models", EmbedPath: "/embeddings"},
	{Key: "nebius", Adapter: AdapterOpenAI, BaseURL: "https://api.studio.nebius.ai", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "ollama", Adapter: AdapterOpenAI, BaseURL: "http://localhost:11434", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings", Local: true},
	{Key: "deepseek", Adapter: AdapterOpenAI, BaseURL: "https://api.deepseek.com", ChatPath: "/chat/completions", ModelsPath: "/models", EmbedPath: "/embeddings"},
	{Key: "groq", Adapter: AdapterOpenAI, BaseURL: "https://api.groq.com/openai", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "xai", Adapter: AdapterOpenAI, BaseURL: "https://api.x.ai", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "openrouter", Adapter: AdapterOpenAI, BaseURL: "https://openrouter.ai/api", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "mistral", Adapter: AdapterOpenAI, BaseURL: "https://api.mistral.ai", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "together", Adapter: AdapterOpenAI, BaseURL: "https://api.together.xyz", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "cerebras", Adapter: AdapterOpenAI, BaseURL: "https://api.cerebras.ai", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "fireworks", Adapter: AdapterOpenAI, BaseURL: "https://api.fireworks.ai/inference", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"},
	{Key: "perplexity", Adapter: AdapterOpenAI, BaseURL: "https://api.perplexity.ai", ChatPath: "/chat/completions", ModelsPath: "/models", EmbedPath: "/embeddings"},
	{Key: "cohere", Adapter: AdapterOpenAI, BaseURL: "https://api.cohere.ai/compatibility/v1", ChatPath: "/chat/completions", ModelsPath: "/models", EmbedPath: "/embeddings"},
	{Key: "lmstudio", Adapter: AdapterOpenAI, BaseURL: "http://localhost:1234", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings", Local: true},
}

// legacyPaths are the paths every OpenAI-compatible account used before
// vendors had their own. An account on a host that is no known vendor's keeps
// them, so a proxy or a self-hosted server works as it did.
var legacyPaths = Vendor{ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models", EmbedPath: "/v1/embeddings"}

// Vendors returns every picker vendor, in picker order.
func Vendors() []Vendor {
	return append([]Vendor(nil), vendors...)
}

// VendorFor finds a vendor by its picker id, without regard to case.
func VendorFor(key string) (Vendor, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, v := range vendors {
		if v.Key == key {
			return v, true
		}
	}
	return Vendor{}, false
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

// CompatPaths returns the OpenAI-compatible paths to call under baseURL. The
// vendor is told by the host: a base URL on a hosted vendor's host gets that
// vendor's paths, so the account needs no stored vendor and a saved URL that
// is edited to another vendor follows. Any other host (a proxy, a local
// runtime, a vendor with its own native adapter such as anthropic, whose
// older accounts still use the compatible endpoint) gets the legacy /v1 paths.
func CompatPaths(baseURL string) Vendor {
	host := hostOf(baseURL)
	if host == "" {
		return legacyPaths
	}
	for _, v := range vendors {
		if !v.Local && v.Adapter == AdapterOpenAI && hostOf(v.BaseURL) == host {
			return v
		}
	}
	return legacyPaths
}
