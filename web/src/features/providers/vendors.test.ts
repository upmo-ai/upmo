import { describe, expect, it } from "vitest";

import { KNOWN_PROVIDERS, TEST_PROVIDER } from "./providers";
import { adapterKey, baseUrlRequired, hostedBaseUrl } from "./rules";

// The brain's vendor table (internal/gateway/domain/vendor.go): picker id, the
// adapter key the brain dispatches that vendor to, and its base URL. The Go
// tests hold the same list, so a vendor added to one side and not the other
// fails there.
const brainVendors: ReadonlyArray<readonly [string, string, string]> = [
  ["anthropic", "anthropic", "https://api.anthropic.com"],
  ["openai", "openai_compat", "https://api.openai.com"],
  ["gemini", "openai_compat", "https://generativelanguage.googleapis.com/v1beta/openai"],
  ["nebius", "openai_compat", "https://api.studio.nebius.ai"],
  ["ollama", "openai_compat", "http://localhost:11434"],
  ["deepseek", "openai_compat", "https://api.deepseek.com"],
  ["groq", "openai_compat", "https://api.groq.com/openai"],
  ["xai", "openai_compat", "https://api.x.ai"],
  ["openrouter", "openai_compat", "https://openrouter.ai/api"],
  ["mistral", "openai_compat", "https://api.mistral.ai"],
  ["together", "openai_compat", "https://api.together.xyz"],
  ["cerebras", "openai_compat", "https://api.cerebras.ai"],
  ["fireworks", "openai_compat", "https://api.fireworks.ai/inference"],
  ["perplexity", "openai_compat", "https://api.perplexity.ai"],
  ["cohere", "openai_compat", "https://api.cohere.ai/compatibility/v1"],
  ["lmstudio", "openai_compat", "http://localhost:1234"],
];

describe("the picker's vendors", () => {
  it("are exactly the brain's vendors, in its order", () => {
    expect(KNOWN_PROVIDERS.map((provider) => provider.id)).toEqual(brainVendors.map(([id]) => id));
  });

  it("each resolve to the brain's adapter key and default base URL", () => {
    for (const [id, adapter, baseUrl] of brainVendors) {
      const provider = KNOWN_PROVIDERS.find((known) => known.id === id);
      expect([id, provider !== undefined]).toEqual([id, true]);
      expect([id, adapterKey(provider?.adapter ?? "")]).toEqual([id, adapter]);
      expect([id, provider?.defaultBaseUrl]).toEqual([id, baseUrl]);
    }
  });

  it("have a well formed default base URL: https, unless the vendor is a local runtime", () => {
    for (const provider of KNOWN_PROVIDERS) {
      const url = new URL(provider.defaultBaseUrl ?? "");
      const local = provider.defaultPlan === "local";
      expect([provider.id, url.protocol]).toEqual([provider.id, local ? "http:" : "https:"]);
      expect([provider.id, url.search + url.hash]).toEqual([provider.id, ""]);
      expect([provider.id, provider.defaultBaseUrl?.endsWith("/")]).toEqual([provider.id, false]);
    }
  });

  it("need no typed base URL when hosted, and need one when they run on the person's machine", () => {
    for (const provider of KNOWN_PROVIDERS) {
      const local = provider.defaultPlan === "local";
      expect([provider.id, baseUrlRequired(provider.adapter, provider.defaultBaseUrl)]).toEqual([provider.id, local]);
      expect([provider.id, hostedBaseUrl(provider.defaultBaseUrl) === undefined]).toEqual([provider.id, local]);
    }
  });

  it("keep the test provider on the fake adapter with no base URL", () => {
    expect(adapterKey(TEST_PROVIDER.adapter)).toBe("fake");
    expect(baseUrlRequired(TEST_PROVIDER.adapter, TEST_PROVIDER.defaultBaseUrl)).toBe(false);
  });
});
