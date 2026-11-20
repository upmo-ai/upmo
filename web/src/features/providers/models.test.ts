import { describe, expect, it } from "vitest";

import type { CatalogModel } from "../../app/api";
import { catalogMicro, formatMicro, formatRates, listedPrice, microToUsdText, seenRateMicro, usdToMicro } from "./models";

describe("typed prices", () => {
  it("turn USD text into whole micro-USD without float drift", () => {
    expect(usdToMicro("2.50")).toBe(2_500_000);
    expect(usdToMicro("0.1")).toBe(100_000);
    expect(usdToMicro("0.29")).toBe(290_000);
    expect(usdToMicro("1.000001")).toBe(1_000_001);
    expect(usdToMicro(" 15 ")).toBe(15_000_000);
    expect(usdToMicro(".5")).toBe(500_000);
    expect(usdToMicro("0")).toBe(0);
  });

  it("refuse text that is not a plain non-negative price with at most six decimals", () => {
    for (const text of ["", ".", "-1", "1e3", "$2", "2,50", "abc", "0.0000001"]) {
      expect([text, usdToMicro(text)]).toEqual([text, null]);
    }
    expect(usdToMicro("1.2500000")).toBe(1_250_000);
  });

  it("round trip micro-USD to field text and to a label", () => {
    expect(microToUsdText(2_500_000)).toBe("2.5");
    expect(microToUsdText(75_000)).toBe("0.075");
    expect(microToUsdText(15_000_000)).toBe("15");
    expect(formatMicro(2_500_000)).toBe("$2.50");
    expect(formatMicro(75_000)).toBe("$0.075");
    expect(formatMicro(0)).toBe("$0.00");
    expect(usdToMicro(microToUsdText(1_000_001))).toBe(1_000_001);
  });
});

describe("listed prices", () => {
  it("read a provider's per-token text rates as micro-USD per million tokens", () => {
    expect(seenRateMicro({ prompt: "0.000003" }, "prompt")).toBe(3_000_000);
    expect(seenRateMicro({ prompt: "0.0000000375" }, "prompt")).toBe(37_500);
    expect(seenRateMicro({ prompt: "-1" }, "prompt")).toBeNull();
    expect(seenRateMicro({}, "prompt")).toBeNull();
    expect(seenRateMicro(undefined, "prompt")).toBeNull();
  });

  it("round catalogue rates to whole micro-USD", () => {
    expect(catalogMicro(0.075)).toBe(75_000);
    expect(catalogMicro(2.5)).toBe(2_500_000);
    expect(catalogMicro(null)).toBeNull();
  });

  it("take the provider's list first, then models.dev, and say unknown otherwise", () => {
    const catalog: CatalogModel[] = [
      {
        model: "model-b",
        kind: "chat",
        context_window: 0,
        input_usd_per_mtok: 1,
        output_usd_per_mtok: 4,
        cache_read_usd_per_mtok: null,
        cache_write_usd_per_mtok: null,
      },
    ];
    expect(listedPrice({ id: "model-a", pricing: { prompt: "0.000002", completion: "0.000008" } }, catalog)).toEqual({
      input: 2_000_000,
      output: 8_000_000,
      source: "provider",
    });
    expect(listedPrice({ id: "model-b" }, catalog)).toEqual({ input: 1_000_000, output: 4_000_000, source: "models_dev" });
    expect(listedPrice({ id: "model-c" }, catalog)).toEqual({ input: null, output: null, source: "none" });
  });

  it("word a pair of rates", () => {
    expect(formatRates(2_500_000, 10_000_000)).toBe("in $2.50 · out $10.00 / MTok");
    expect(formatRates(null, null)).toBe("Unpriced");
    expect(formatRates(0, 0)).toBe("Free");
    expect(formatRates(null, 1_000_000)).toBe("in N/A · out $1.00 / MTok");
  });
});
