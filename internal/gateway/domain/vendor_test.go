package domain

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

// pickerVendors pins the order served by GET /api/providers/vendors. The web
// picker reads that endpoint and maps the response to tiles with pickerVendors
// in web/src/features/providers/vendors.ts.
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

// savedBases is every base URL an account of v may be saved on: the vendor's
// default plus one per legacy host from before a move.
func savedBases(v Vendor) []string {
	bases := []string{v.BaseURL}
	for _, h := range v.LegacyHosts {
		bases = append(bases, "https://"+h)
	}
	return bases
}

func TestHostedVendorHostsAreUnique(t *testing.T) {
	// The host is how the compatible adapter tells hosted vendors apart, so two
	// of them may not share one, and a legacy host may not collide with any
	// host another vendor answers on. A local runtime is judged by no host.
	seen := map[string]string{}
	for _, v := range Vendors() {
		if v.Local {
			continue
		}
		hosts := append([]string{hostOf(v.BaseURL)}, v.LegacyHosts...)
		for _, host := range hosts {
			if host == "" {
				t.Errorf("%s: empty host among %v", v.Key, hosts)
				continue
			}
			if other, dup := seen[host]; dup {
				t.Errorf("%s and %s share the host %s, so the host cannot tell them apart", v.Key, other, host)
			}
			seen[host] = v.Key
		}
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

func TestLegacyHostsAreCopiedWhenAVendorIsHandedOut(t *testing.T) {
	// Vendors, VendorFor and CompatPaths hand Vendor values out of the table.
	// Their LegacyHosts must be copies, or a caller editing a returned slice
	// would rewrite the alias data every later call reads.
	checked := 0
	for _, v := range Vendors() {
		if len(v.LegacyHosts) == 0 {
			continue
		}
		checked++
		want := slices.Clone(v.LegacyHosts)

		v.LegacyHosts[0] = "mutated.example.com"
		if again, ok := VendorFor(v.Key); !ok || !slices.Equal(again.LegacyHosts, want) {
			t.Errorf("%s: VendorFor legacy hosts = %v after editing the Vendors() copy, want %v", v.Key, again.LegacyHosts, want)
		}

		fromLookup, ok := VendorFor(v.Key)
		if !ok {
			t.Fatalf("%s disappeared from the table", v.Key)
		}
		fromLookup.LegacyHosts[0] = "mutated.example.com"
		if again, ok := VendorFor(v.Key); !ok || !slices.Equal(again.LegacyHosts, want) {
			t.Errorf("%s: VendorFor legacy hosts = %v after editing a returned vendor, want %v", v.Key, again.LegacyHosts, want)
		}

		base := "https://" + want[0]
		paths := CompatPaths(base)
		if paths.Key != v.Key {
			t.Fatalf("CompatPaths(%q) = %q, want %s", base, paths.Key, v.Key)
		}
		paths.LegacyHosts[0] = "mutated.example.com"
		if got := CompatPaths(base); got.Key != v.Key || slices.Contains(got.LegacyHosts, "mutated.example.com") {
			t.Errorf("%s: CompatPaths(%q) after editing a returned vendor = %+v, want %s with %v", v.Key, base, got, v.Key, want)
		}
	}
	if checked == 0 {
		t.Fatal("no vendor carries a legacy host, so the copies went untested")
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
		{"perplexity", "https://api.perplexity.ai", "/chat/completions", "/v1/models", "/v1/embeddings"},
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
		for _, base := range savedBases(v) {
			got := CompatPaths(base)
			if v.Adapter == AdapterOpenAI && !v.Local {
				if got.Key != v.Key {
					t.Errorf("%s: base URL %q resolves to %q", v.Key, base, got.Key)
				}
				continue
			}
			if got.Key != "" {
				t.Errorf("%s: base URL %q resolves to vendor %q, want the legacy paths", v.Key, base, got.Key)
			}
		}
	}
}

func TestAMovedVendorStillAnswersOnItsLegacyHost(t *testing.T) {
	// Nebius moved from api.studio.nebius.ai to api.tokenfactory.nebius.com.
	// Accounts saved on the old host keep the vendor's compat paths and pricing
	// vendor, so the move cannot silently drop pricing for them.
	moved, ok := VendorFor("nebius")
	if !ok || moved.BaseURL != "https://api.tokenfactory.nebius.com" {
		t.Fatalf("nebius = %+v, ok = %v", moved, ok)
	}
	now := CompatPaths(moved.BaseURL)
	for _, host := range moved.LegacyHosts {
		base := "https://" + host
		if got := CompatPaths(base); got.Key != moved.Key || got.ChatPath != now.ChatPath ||
			got.ModelsPath != now.ModelsPath || got.EmbedPath != now.EmbedPath {
			t.Errorf("CompatPaths(%q) = %+v, want the %s paths %+v", base, got, moved.Key, now)
		}
		if got := PricingVendorFor(AdapterOpenAI, base); got != moved.Key {
			t.Errorf("PricingVendorFor(%q) = %q, want %q", base, got, moved.Key)
		}
	}
}

func TestSavedLegacyHostsKeepTheirVendorPathsAndPricing(t *testing.T) {
	// Pinned with literal hosts, not LegacyHosts, so removing a legacy host
	// from the vendor table fails here.
	cases := []struct{ vendor, legacy, current string }{
		{"nebius", "https://api.studio.nebius.ai", "https://api.tokenfactory.nebius.com"},
		{"together", "https://api.together.xyz", "https://api.together.ai"},
	}
	for _, tc := range cases {
		want := CompatPaths(tc.current)
		if want.Key != tc.vendor {
			t.Fatalf("%s: current host %q resolves to %q", tc.vendor, tc.current, want.Key)
		}
		got := CompatPaths(tc.legacy)
		if got.Key != tc.vendor || got.ChatPath != want.ChatPath ||
			got.ModelsPath != want.ModelsPath || got.EmbedPath != want.EmbedPath {
			t.Errorf("%s: CompatPaths(%q) = %+v, want the paths of %q: %+v", tc.vendor, tc.legacy, got, tc.current, want)
		}
		if got, want := PricingVendorFor(AdapterOpenAI, tc.legacy), PricingVendorFor(AdapterOpenAI, tc.current); got != tc.vendor || got != want {
			t.Errorf("%s: PricingVendorFor(%q) = %q, current host gives %q, want %q", tc.vendor, tc.legacy, got, want, tc.vendor)
		}
	}
}

func TestVendorMetadataAndModelsDevMapping(t *testing.T) {
	for _, v := range Vendors() {
		if v.Name == "" {
			t.Errorf("%s: empty vendor Name", v.Key)
		}
		if v.Local && v.SuggestedPlan != PlanLocal {
			t.Errorf("%s: local vendor suggested plan = %q", v.Key, v.SuggestedPlan)
		}
		if !v.Local && v.SuggestedPlan != PlanMetered {
			t.Errorf("%s: hosted vendor suggested plan = %q", v.Key, v.SuggestedPlan)
		}
	}
	cases := map[string]string{
		"anthropic":    "anthropic",
		"openai":       "openai",
		"google":       "gemini",
		"togetherai":   "together",
		"fireworks-ai": "fireworks",
		"openrouter":   "openrouter",
	}
	for md, want := range cases {
		got, ok := VendorForModelsDev(md)
		if !ok || got != want {
			t.Errorf("VendorForModelsDev(%q) = (%q, %v), want (%q, true)", md, got, ok, want)
		}
	}
	for _, unmapped := range []string{"amazon-bedrock", "gemini", "together", "fireworks"} {
		if got, ok := VendorForModelsDev(unmapped); ok || got != "" {
			t.Fatalf("unmapped models.dev provider %q mapped to (%q, %v)", unmapped, got, ok)
		}
	}
}

func TestPricingVendor(t *testing.T) {
	// Hosted OpenAI-compatible vendors resolve by host, on their default base
	// URL and on every legacy host, ignoring the mutable Provider label.
	for _, v := range Vendors() {
		for _, base := range savedBases(v) {
			acct := Account{Provider: "openai", Adapter: v.Adapter, BaseURL: base}
			want := v.Key
			if v.Local {
				want = ""
			}
			if got := PricingVendor(acct); got != want {
				t.Errorf("%s (%s, %s): PricingVendor = %q, want %q", v.Key, v.Adapter, base, got, want)
			}
		}
	}

	cases := []struct {
		name    string
		account Account
		want    string
	}{
		{
			name:    "openai label edited to openrouter host",
			account: Account{Provider: "openai", Adapter: AdapterOpenAI, BaseURL: "https://openrouter.ai/api"},
			want:    "openrouter",
		},
		{
			name:    "uppercase host with port and trailing dot",
			account: Account{Provider: "openai", Adapter: AdapterOpenAI, BaseURL: "https://API.Deepseek.com.:443/"},
			want:    "deepseek",
		},
		{
			name:    "native anthropic on anthropic host",
			account: Account{Provider: "custom", Adapter: AdapterAnthropic, BaseURL: "https://api.anthropic.com"},
			want:    "anthropic",
		},
		{
			name:    "native anthropic on custom proxy host",
			account: Account{Provider: "anthropic", Adapter: AdapterAnthropic, BaseURL: "https://proxy.example.com"},
			want:    "",
		},
		{
			name:    "openai_compat on anthropic host maps to anthropic",
			account: Account{Provider: "anthropic", Adapter: AdapterOpenAI, BaseURL: "https://api.anthropic.com"},
			want:    "anthropic",
		},
		{
			name:    "openai label on custom host",
			account: Account{Provider: "openai", Adapter: AdapterOpenAI, BaseURL: "https://llm.example.com/v1"},
			want:    "",
		},
		{
			name:    "local loopback",
			account: Account{Provider: "openai", Adapter: AdapterOpenAI, BaseURL: "http://127.0.0.1:11434"},
			want:    "",
		},
		{
			name:    "fake adapter",
			account: Account{Provider: "openai", Adapter: AdapterFake, BaseURL: "https://api.openai.com"},
			want:    "",
		},
		{
			name:    "empty base URL",
			account: Account{Provider: "openai", Adapter: AdapterOpenAI, BaseURL: ""},
			want:    "",
		},
	}
	for _, tc := range cases {
		if got := PricingVendor(tc.account); got != tc.want {
			t.Errorf("%s: PricingVendor = %q, want %q", tc.name, got, tc.want)
		}
	}
}
