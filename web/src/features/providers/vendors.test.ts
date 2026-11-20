import { describe, expect, it } from "vitest";

import type { ProviderVendor } from "../../app/api";
import { adapterKey, baseUrlRequired, hostedBaseUrl, keyRequired } from "./rules";
import { pickerVendor, pickerVendors, TEST_VENDOR } from "./vendors";

// Rows as GET /api/providers/vendors sends them: a hosted vendor on the
// native adapter, a hosted one on the compatible adapter, and a local runtime.
const hosted: ProviderVendor = {
  key: "anthropic",
  name: "Anthropic",
  adapter: "anthropic",
  default_base_url: "https://api.anthropic.com",
  suggested_plan: "metered",
};
const compatible: ProviderVendor = {
  key: "gemini",
  name: "Google Gemini",
  adapter: "openai_compat",
  default_base_url: "https://generativelanguage.googleapis.com/v1beta/openai",
  suggested_plan: "metered",
};
const local: ProviderVendor = {
  key: "ollama",
  name: "Ollama",
  adapter: "openai_compat",
  default_base_url: "http://localhost:11434",
  suggested_plan: "local",
};

describe("the picker's vendors", () => {
  it("are the brain's vendors in its order, with the test provider only in a dev build", () => {
    expect(pickerVendors([hosted, compatible, local], false).map((vendor) => vendor.id)).toEqual([
      "anthropic",
      "gemini",
      "ollama",
    ]);
    expect(pickerVendors([hosted], true).map((vendor) => vendor.id)).toEqual(["anthropic", "fake"]);
  });

  it("keep the brain's adapter key and default address", () => {
    for (const row of [hosted, compatible, local]) {
      const vendor = pickerVendor(row);
      expect([row.key, adapterKey(vendor.adapter), vendor.defaultBaseUrl]).toEqual([
        row.key,
        row.adapter,
        row.default_base_url,
      ]);
    }
  });

  it("start on the plan the brain suggests, and a vendor with none on metered", () => {
    expect(pickerVendor(local).suggestedPlan).toBe("local");
    expect(pickerVendor(hosted).suggestedPlan).toBe("metered");
    expect(pickerVendor({ ...compatible, suggested_plan: undefined }).suggestedPlan).toBe("metered");
  });

  it("need no typed base URL when hosted, and need one when they run on the person's machine", () => {
    for (const row of [hosted, compatible, local]) {
      const vendor = pickerVendor(row);
      const runsLocally = row.suggested_plan === "local";
      expect([row.key, baseUrlRequired(vendor.adapter, vendor.defaultBaseUrl)]).toEqual([row.key, runsLocally]);
      expect([row.key, hostedBaseUrl(vendor.defaultBaseUrl) === undefined]).toEqual([row.key, runsLocally]);
      expect([row.key, keyRequired(vendor.suggestedPlan)]).toEqual([row.key, !runsLocally]);
    }
  });

  it("keep the test provider on the fake adapter with no base URL", () => {
    expect(adapterKey(TEST_VENDOR.adapter)).toBe("fake");
    expect(baseUrlRequired(TEST_VENDOR.adapter, TEST_VENDOR.defaultBaseUrl)).toBe(false);
  });
});
