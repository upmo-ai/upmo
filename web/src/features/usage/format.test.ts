import { describe, expect, it } from "vitest";

import { formatRate, formatTokens, formatUsd } from "./format";
import { cacheHitRate, knownSpend, periodCacheHit, sampleDays, totalTokens } from "./sample";

describe("usage figures", () => {
  it("never prints unknown cost as zero", () => {
    expect(formatUsd(null)).toBe("Unknown");
    expect(formatUsd(0)).not.toBe("Unknown");
    expect(formatUsd(0)).toMatch(/0/);
    expect(formatUsd(12.47)).toMatch(/12\.47/);
  });

  it("formats tokens and rates in en-GB", () => {
    expect(formatTokens(1842000)).toBe("1,842,000");
    expect(formatRate(0.62)).toBe("62%");
  });

  it("counts cache hits as cache read over input plus cache read", () => {
    expect(cacheHitRate(100, 100)).toBe(0.5);
    expect(cacheHitRate(0, 0)).toBe(0);
    expect(cacheHitRate(0, 20)).toBe(1);
  });

  it("sums known spend and ignores a missing day as zero in the total only", () => {
    expect(sampleDays.some((day) => day.spendUsd === null)).toBe(true);
    expect(knownSpend(sampleDays)).toBeCloseTo(11.47);
    expect(totalTokens(sampleDays)).toBeGreaterThan(0);
    expect(periodCacheHit(sampleDays)).toBeGreaterThan(0);
    expect(periodCacheHit(sampleDays)).toBeLessThan(1);
  });
});
