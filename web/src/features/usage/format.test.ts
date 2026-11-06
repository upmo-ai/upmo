import { describe, expect, it } from "vitest";

import { createStubApi } from "../../app/api";
import { cacheHitRate, knownSpend, periodCacheHit, periodSpend, taskTokens, totalTokens } from "./figures";
import { formatDay, formatRate, formatTokens, formatUsd } from "./format";

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

  it("formats a calendar day without shifting it by the local time zone", () => {
    expect(formatDay("2026-11-01")).toBe("1 Nov");
    expect(formatDay("2026-10-31")).toBe("31 Oct");
  });

  it("counts cache hits as cache read over input plus cache read", () => {
    expect(cacheHitRate(100, 100)).toBe(0.5);
    expect(cacheHitRate(0, 0)).toBe(0);
    expect(cacheHitRate(0, 20)).toBe(1);
  });

  it("sums known spend and ignores a missing day as zero in the total only", async () => {
    const { days } = await createStubApi().usage(7);
    expect(days.some((day) => day.spend_usd === null)).toBe(true);
    const known = days.reduce((sum, day) => sum + (day.spend_usd ?? 0), 0);
    expect(knownSpend(days)).toBeCloseTo(known);
    expect(knownSpend(days)).toBeGreaterThan(0);
    expect(totalTokens(days)).toBeGreaterThan(0);
    expect(periodCacheHit(days)).toBeGreaterThan(0);
    expect(periodCacheHit(days)).toBeLessThan(1);
  });

  it("UPM-204 adds all four token fields of a task and nothing else", () => {
    const base = { task_id: "t", goal: "t", cost_usd: null };
    expect(taskTokens({ ...base, input: 1, output: 20, cache_read: 300, cache_write: 4000 })).toBe(4321);
    expect(taskTokens({ ...base, input: 7, output: 0, cache_read: 0, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 7, cache_read: 0, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 7, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 0, cache_write: 7 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 0, cache_write: 0 })).toBe(0);
  });

  it("UPM-204 reports period spend as unknown only when no day has a known cost", () => {
    const day = (spend: number | null) => ({
      date: "2026-11-01",
      spend_usd: spend,
      input: 0,
      output: 0,
      cache_read: 0,
      cache_write: 0,
      reasoning: 0,
    });
    expect(periodSpend([day(null), day(null)])).toBeNull();
    expect(periodSpend([])).toBeNull();
    expect(periodSpend([day(null), day(1.5), day(2)])).toBe(3.5);
    expect(periodSpend([day(0), day(null)])).toBe(0);
  });

  it("UPM-204 gives each period the same days for the dates they share, with unique dates", async () => {
    const api = createStubApi();
    const week = await api.usage(7);
    const quarter = await api.usage(90);
    expect(week.days).toHaveLength(7);
    expect(quarter.days).toHaveLength(90);
    expect(new Set(quarter.days.map((day) => day.date)).size).toBe(90);
    expect(quarter.days.slice(-7)).toEqual(week.days);
  });
});
