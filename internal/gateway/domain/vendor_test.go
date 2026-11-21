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
	"azure", "bedrock-mantle",
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
		base := v.BaseURL
		if base == "" {
			if len(v.HostPatterns) == 0 {
				t.Errorf("%s: empty base URL without host patterns", key)
				continue
			}
			base = savedBases(v)[0]
		} else if clean, err := CleanBaseURL(plan, base); err != nil || clean != base {
			t.Errorf("%s: base URL %q is not one an account may save: %q, %v", key, base, clean, err)
		}
		paths := []string{v.ChatPath, v.ModelsPath}
		if v.Adapter == AdapterOpenAI {
			if v.EmbedPath != "" {
				paths = append(paths, v.EmbedPath)
			}
		} else if v.EmbedPath != "" {
			t.Errorf("%s: a native adapter has no embed path, got %q", key, v.EmbedPath)
		}
		for _, p := range paths {
			if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "?#") || strings.HasSuffix(p, "/") {
				t.Errorf("%s: path %q is not a clean absolute path", key, p)
			}
			u, err := url.Parse(base + p)
			if err != nil || u.Host == "" || u.RawQuery != "" {
				t.Errorf("%s: %q does not join into a URL: %v", key, base+p, err)
			}
		}
	}
}

// savedBases gives a representative base URL for every way an account of v
// may be saved: its default, or one documented base per host pattern.
func savedBases(v Vendor) []string {
	if v.BaseURL != "" {
		return []string{v.BaseURL}
	}
	var bases []string
	for _, pattern := range v.HostPatterns {
		base := "https://" + strings.Replace(string(pattern), "*", "sample", 1)
		switch v.Key {
		case "azure":
			base += "/openai/v1"
		case "bedrock-mantle":
			base += "/v1"
		}
		bases = append(bases, base)
	}
	return bases
}

func TestHostedVendorHostsAreUnique(t *testing.T) {
	// The host is how the compatible adapter tells hosted vendors apart, so
	// their base hosts and representative pattern hosts must all be distinct.
	seen := map[string]string{}
	for _, v := range Vendors() {
		if v.Local {
			continue
		}
		var hosts []string
		if v.BaseURL != "" {
			hosts = append(hosts, hostOf(v.BaseURL))
		}
		for _, pattern := range v.HostPatterns {
			hosts = append(hosts, strings.Replace(string(pattern), "*", "sample", 1))
		}
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

func TestHostPatternsAreCopiedWhenAVendorIsHandedOut(t *testing.T) {
	// Vendors, VendorFor and CompatPaths hand Vendor values out of the table.
	// Their HostPatterns must be copies, or a caller editing a returned slice
	// would rewrite the matching data every later call reads.
	checked := 0
	for _, v := range Vendors() {
		if len(v.HostPatterns) == 0 {
			continue
		}
		checked++
		want := slices.Clone(v.HostPatterns)

		v.HostPatterns[0] = "*.mutated.example.com"
		if again, ok := VendorFor(v.Key); !ok || !slices.Equal(again.HostPatterns, want) {
			t.Errorf("%s: VendorFor host patterns = %v after editing the Vendors() copy, want %v", v.Key, again.HostPatterns, want)
		}

		fromLookup, ok := VendorFor(v.Key)
		if !ok {
			t.Fatalf("%s disappeared from the table", v.Key)
		}
		fromLookup.HostPatterns[0] = "*.mutated.example.com"
		if again, ok := VendorFor(v.Key); !ok || !slices.Equal(again.HostPatterns, want) {
			t.Errorf("%s: VendorFor host patterns = %v after editing a returned vendor, want %v", v.Key, again.HostPatterns, want)
		}

		base := "https://" + strings.Replace(string(want[0]), "*", "sample", 1)
		paths := CompatPaths(base)
		if paths.Key != v.Key {
			t.Fatalf("CompatPaths(%q) = %q, want %s", base, paths.Key, v.Key)
		}
		paths.HostPatterns[0] = "*.mutated.example.com"
		if got := CompatPaths(base); got.Key != v.Key || slices.Contains(got.HostPatterns, HostPattern("*.mutated.example.com")) {
			t.Errorf("%s: CompatPaths(%q) after editing a returned vendor = %+v, want %s with %v", v.Key, base, got, v.Key, want)
		}
	}
	if checked != 2 {
		t.Fatalf("checked %d pattern vendors, want Azure and Mantle", checked)
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
	generic := genericPaths
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
		{"azure resource", "https://my-resource.openai.azure.com/openai/v1", "/chat/completions", "/models", "/embeddings"},
		{"azure Foundry with case port and root dot", "https://MY-RESOURCE.SERVICES.AI.AZURE.COM.:443/openai/v1", "/chat/completions", "/models", "/embeddings"},
		{"mantle region", "https://bedrock-mantle.us-east-1.api.aws/v1", "/chat/completions", "/models", ""},
		{"mantle region with case port and root dot", "https://BEDROCK-MANTLE.EU-WEST-1.API.AWS.:443/v1", "/chat/completions", "/models", ""},
		{"bare azure suffix", "https://openai.azure.com/openai/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"bare Foundry suffix", "https://services.ai.azure.com/openai/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"azure attacker suffix", "https://evil-openai.azure.com.attacker.net/openai/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"azure substring", "https://evilopenai.azure.com/openai/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"azure empty label", "https://.openai.azure.com/openai/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"azure multiple labels", "https://two.labels.openai.azure.com/openai/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"mantle attacker suffix", "https://bedrock-mantle.us-east-1.api.aws.attacker.net/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"mantle substring", "https://notbedrock-mantle.us-east-1.api.aws/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"mantle empty region", "https://bedrock-mantle..api.aws/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"mantle multiple region labels", "https://bedrock-mantle.us.east-1.api.aws/v1", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"anthropic uses generic compatible paths", "https://api.anthropic.com", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"a proxy", "https://llm.example.com", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"a local runtime", "http://localhost:11434", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"a private address", "http://10.0.0.5:8080", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"no base", "", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
		{"not a URL", "::", generic.ChatPath, generic.ModelsPath, generic.EmbedPath},
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
				t.Errorf("%s: base URL %q resolves to vendor %q, want the generic paths", v.Key, base, got.Key)
			}
		}
	}
}

func TestRemovedHostsUseGenericPathsAndNoPricing(t *testing.T) {
	// Pinned with literal hosts so putting either one back in the vendor table
	// fails this test.
	for _, base := range []string{"https://api.studio.nebius.ai", "https://api.together.xyz"} {
		got := CompatPaths(base)
		if got.Key != "" || got.ChatPath != genericPaths.ChatPath ||
			got.ModelsPath != genericPaths.ModelsPath || got.EmbedPath != genericPaths.EmbedPath {
			t.Errorf("CompatPaths(%q) = %+v, want the generic paths %+v", base, got, genericPaths)
		}
		if got := PricingVendorFor(AdapterOpenAI, base); got != "" {
			t.Errorf("PricingVendorFor(%q) = %q, want empty", base, got)
		}
	}
}

func TestPatternVendorsResolvePricingByHost(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://one.openai.azure.com/openai/v1", "azure"},
		{"https://one.services.ai.azure.com/openai/v1", "azure"},
		{"https://bedrock-mantle.us-east-1.api.aws/v1", "bedrock-mantle"},
	} {
		if got := PricingVendorFor(AdapterOpenAI, tc.base); got != tc.want {
			t.Errorf("PricingVendorFor(%q) = %q, want %q", tc.base, got, tc.want)
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
		"anthropic":      "anthropic",
		"openai":         "openai",
		"google":         "gemini",
		"togetherai":     "together",
		"fireworks-ai":   "fireworks",
		"openrouter":     "openrouter",
		"amazon-bedrock": "bedrock-mantle",
	}
	for md, want := range cases {
		got, ok := VendorForModelsDev(md)
		if !ok || got != want {
			t.Errorf("VendorForModelsDev(%q) = (%q, %v), want (%q, true)", md, got, ok, want)
		}
	}
	for _, unmapped := range []string{"azure", "gemini", "together", "fireworks"} {
		if got, ok := VendorForModelsDev(unmapped); ok || got != "" {
			t.Fatalf("unmapped models.dev provider %q mapped to (%q, %v)", unmapped, got, ok)
		}
	}
}

func TestPricingVendor(t *testing.T) {
	// Hosted OpenAI-compatible vendors resolve by base host or host pattern,
	// ignoring the mutable Provider label.
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
			name:    "azure attacker host",
			account: Account{Provider: "azure", Adapter: AdapterOpenAI, BaseURL: "https://evil-openai.azure.com.attacker.net/openai/v1"},
			want:    "",
		},
		{
			name:    "mantle multiple region labels",
			account: Account{Provider: "bedrock-mantle", Adapter: AdapterOpenAI, BaseURL: "https://bedrock-mantle.us.east-1.api.aws/v1"},
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
