package domain

import (
	"net/url"
	"strings"
	"testing"
)

// pickerVendors is the vendor list of the web picker (KNOWN_PROVIDERS in
// web/src/features/providers/providers.tsx), in its order. The web tests hold
// the same list, so a vendor added to one side and not the other fails there.
var pickerVendors = []string{
	"anthropic", "openai", "gemini", "nebius", "ollama", "deepseek", "groq", "xai",
	"openrouter", "mistral", "together", "cerebras", "fireworks", "perplexity", "cohere", "lmstudio",
}

func TestEveryPickerVendorResolvesToAnAdapterAndAURLPair(t *testing.T) {
	got := Vendors()
	if len(got) != len(pickerVendors) {
		t.Fatalf("%d vendors, want %d", len(got), len(pickerVendors))
	}
	for i, key := range pickerVendors {
		v, ok := VendorFor(key)
		if !ok || v.Key != key || got[i].Key != key {
			t.Errorf("vendor %q: found=%v key=%q listed=%q", key, ok, v.Key, got[i].Key)
			continue
		}
		if v.Adapter != AdapterOpenAI && v.Adapter != AdapterAnthropic {
			t.Errorf("%s: adapter %q is not one the gateway dispatches to", key, v.Adapter)
		}
		plan := PlanMetered
		if v.Local {
			plan = PlanLocal
		}
		if clean, err := CleanBaseURL(plan, v.BaseURL); err != nil || clean != v.BaseURL {
			t.Errorf("%s: base URL %q is not one an account may save: %q, %v", key, v.BaseURL, clean, err)
		}
		paths := []string{v.ChatPath, v.ModelsPath}
		if v.Adapter == AdapterOpenAI {
			paths = append(paths, v.EmbedPath)
		} else if v.EmbedPath != "" {
			t.Errorf("%s: a native adapter has no embed path, got %q", key, v.EmbedPath)
		}
		for _, p := range paths {
			if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "?#") || strings.HasSuffix(p, "/") {
				t.Errorf("%s: path %q is not a clean absolute path", key, p)
			}
			u, err := url.Parse(v.BaseURL + p)
			if err != nil || u.Host == "" || u.RawQuery != "" {
				t.Errorf("%s: %q does not join into a URL: %v", key, v.BaseURL+p, err)
			}
		}
	}
}

func TestHostedVendorHostsAreUnique(t *testing.T) {
	// The host is how the compatible adapter tells hosted vendors apart, so two
	// of them may not share one. A local runtime is judged by no host.
	seen := map[string]string{}
	for _, v := range Vendors() {
		if v.Local {
			continue
		}
		host := hostOf(v.BaseURL)
		if host == "" {
			t.Errorf("%s: no host in %q", v.Key, v.BaseURL)
		}
		if other, dup := seen[host]; dup {
			t.Errorf("%s and %s share the host %s, so the host cannot tell them apart", v.Key, other, host)
		}
		seen[host] = v.Key
	}
}

func TestVendorForIgnoresCaseAndSpace(t *testing.T) {
	if v, ok := VendorFor("  Anthropic "); !ok || v.Key != "anthropic" {
		t.Fatalf("got %+v, %v", v, ok)
	}
	for _, key := range []string{"", "custom", "fake", "openai_compat"} {
		if _, ok := VendorFor(key); ok {
			t.Errorf("%q is not a picker vendor", key)
		}
	}
}

func TestVendorsReturnsACopy(t *testing.T) {
	list := Vendors()
	list[0].Key = "changed"
	if again := Vendors(); again[0].Key != "anthropic" {
		t.Fatalf("first vendor = %q", again[0].Key)
	}
}

func TestGeminiStaysOnTheCompatiblePathWithoutV1(t *testing.T) {
	v, ok := VendorFor("gemini")
	if !ok || v.Adapter != AdapterOpenAI ||
		v.BaseURL != "https://generativelanguage.googleapis.com/v1beta/openai" || v.ChatPath != "/chat/completions" {
		t.Fatalf("gemini = %+v", v)
	}
	if strings.Contains(v.ChatPath, "/v1") || strings.Contains(v.ModelsPath, "/v1") {
		t.Fatalf("gemini paths carry /v1: %+v", v)
	}
}

func TestAnthropicIsTheNativeVendor(t *testing.T) {
	v, ok := VendorFor("anthropic")
	if !ok || v.Adapter != AdapterAnthropic || v.BaseURL != "https://api.anthropic.com" ||
		v.ChatPath != "/v1/messages" || v.ModelsPath != "/v1/models" {
		t.Fatalf("anthropic = %+v", v)
	}
}

func TestCompatPathsFollowTheHost(t *testing.T) {
	legacy := legacyPaths
	cases := []struct {
		name, base string
		chat       string
		models     string
		embed      string
	}{
		{"openai", "https://api.openai.com", "/v1/chat/completions", "/v1/models", "/v1/embeddings"},
		{"gemini", "https://generativelanguage.googleapis.com/v1beta/openai", "/chat/completions", "/models", "/embeddings"},
		{"cohere", "https://api.cohere.ai/compatibility/v1", "/chat/completions", "/models", "/embeddings"},
		{"deepseek", "https://api.deepseek.com", "/chat/completions", "/models", "/embeddings"},
		{"groq", "https://api.groq.com/openai", "/v1/chat/completions", "/v1/models", "/v1/embeddings"},
		{"upper case and a port", "https://API.Deepseek.com:443/", "/chat/completions", "/models", "/embeddings"},
		{"a root dot", "https://api.deepseek.com./", "/chat/completions", "/models", "/embeddings"},
		{"anthropic keeps the compatible endpoint", "https://api.anthropic.com", legacy.ChatPath, legacy.ModelsPath, legacy.EmbedPath},
		{"a proxy", "https://llm.example.com", legacy.ChatPath, legacy.ModelsPath, legacy.EmbedPath},
		{"a local runtime", "http://localhost:11434", legacy.ChatPath, legacy.ModelsPath, legacy.EmbedPath},
		{"a private address", "http://10.0.0.5:8080", legacy.ChatPath, legacy.ModelsPath, legacy.EmbedPath},
		{"no base", "", legacy.ChatPath, legacy.ModelsPath, legacy.EmbedPath},
		{"not a URL", "::", legacy.ChatPath, legacy.ModelsPath, legacy.EmbedPath},
	}
	for _, tc := range cases {
		got := CompatPaths(tc.base)
		if got.ChatPath != tc.chat || got.ModelsPath != tc.models || got.EmbedPath != tc.embed {
			t.Errorf("%s: %q gives %q %q %q", tc.name, tc.base, got.ChatPath, got.ModelsPath, got.EmbedPath)
		}
	}
}

func TestCompatPathsKnowEveryHostedCompatibleVendor(t *testing.T) {
	for _, v := range Vendors() {
		got := CompatPaths(v.BaseURL)
		if v.Adapter == AdapterOpenAI && !v.Local {
			if got.Key != v.Key {
				t.Errorf("%s: default base URL resolves to %q", v.Key, got.Key)
			}
			continue
		}
		if got.Key != "" {
			t.Errorf("%s: resolves to vendor %q, want the legacy paths", v.Key, got.Key)
		}
	}
}
