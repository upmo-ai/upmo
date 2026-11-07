import { describe, expect, it } from "vitest";

import { createStubApi, type UsageBucket, type UsageTask } from "../../app/api";
import {
  cacheHitRate,
  costliestTasks,
  errorRate,
  isEmptyUsage,
  knownSpend,
  periodCacheHit,
  periodSpend,
  sortTasks,
  taskShare,
  taskTokens,
} from "./figures";
import {
  formatBucket,
  formatDay,
  formatHour,
  formatLatency,
  formatPercentPoints,
  formatRate,
  formatRatePrecise,
  formatTokens,
  formatTokensCompact,
  formatUsd,
} from "./format";
import { parseUsageRange, validateUsageSearch } from "./range";

function bucket(spend: number | null, extra: Partial<UsageBucket> = {}): UsageBucket {
  return {
    at: "2026-11-01",
    requests: 0,
    errors: 0,
    input: 0,
    output: 0,
    cache_read: 0,
    cache_write: 0,
    tool_defs_est: 0,
    spend_usd: spend,
    by_provider: {},
    by_model: {},
    ...extra,
  };
}

function task(id: string, cost: number | null, tokens = 1): UsageTask {
  return { task_id: id, goal: id, input: tokens, output: 0, cache_read: 0, cache_write: 0, cost_usd: cost, by_model: [] };
}

describe("usage figures", () => {
  it("never prints unknown cost as zero", () => {
    expect(formatUsd(null)).toBe("Unknown");
    expect(formatUsd(undefined)).toBe("Unknown");
    expect(formatUsd(0)).not.toBe("Unknown");
    expect(formatUsd(0)).toMatch(/0/);
    expect(formatUsd(12.47)).toMatch(/12\.47/);
  });

  it("formats tokens and rates in en-GB", () => {
    expect(formatTokens(1842000)).toBe("1,842,000");
    expect(formatRate(0.62)).toBe("62%");
    expect(formatRatePrecise(0.0184)).toBe("1.8%");
  });

  it("formats a calendar day without shifting it by the local time zone", () => {
    expect(formatDay("2026-11-01")).toBe("1 Nov");
    expect(formatDay("2026-10-31")).toBe("31 Oct");
  });

  it("formats a full timestamp as its calendar day", () => {
    expect(formatDay("2026-11-05T00:00:00Z")).toBe("5 Nov");
    expect(formatDay("2026-11-05T23:00:00Z")).toBe("5 Nov");
    expect(formatBucket("2026-11-05T00:00:00Z", "day")).toBe("5 Nov");
  });

  it("usage analytics: formats an hour as a 24 hour clock in UTC and labels buckets by their unit", () => {
    expect(formatHour("2026-11-05T09:00:00Z")).toBe("09:00");
    expect(formatHour("2026-11-05T00:00:00Z")).toBe("00:00");
    expect(formatHour("2026-11-05T23:00:00Z")).toBe("23:00");
    expect(formatBucket("2026-11-05T14:00:00Z", "hour")).toBe("14:00");
    expect(formatBucket("2026-11-05", "day")).toBe("5 Nov");
  });

  it("counts cache hits as cache read over input plus cache read", () => {
    expect(cacheHitRate(100, 100)).toBe(0.5);
    expect(cacheHitRate(0, 0)).toBe(0);
    expect(cacheHitRate(0, 20)).toBe(1);
  });

  it("sums known spend and ignores a missing bucket as zero in the total only", async () => {
    const { buckets } = await createStubApi().usage(7);
    expect(buckets.some((item) => item.spend_usd === null)).toBe(true);
    const known = buckets.reduce((sum, item) => sum + (item.spend_usd ?? 0), 0);
    expect(knownSpend(buckets)).toBeCloseTo(known);
    expect(knownSpend(buckets)).toBeGreaterThan(0);
    expect(knownSpend([bucket(0.1), bucket(0.2), bucket(null)])).toBe(0.3);
    expect(periodCacheHit(buckets)).toBeGreaterThan(0);
    expect(periodCacheHit(buckets)).toBeLessThan(1);
  });

  it("UPM-204 adds all four token fields of a task and nothing else", () => {
    const base = { task_id: "t", goal: "t", cost_usd: null, by_model: [] };
    expect(taskTokens({ ...base, input: 1, output: 20, cache_read: 300, cache_write: 4000 })).toBe(4321);
    expect(taskTokens({ ...base, input: 7, output: 0, cache_read: 0, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 7, cache_read: 0, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 7, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 0, cache_write: 7 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 0, cache_write: 0 })).toBe(0);
  });

  it("UPM-204 reports period spend as unknown only when no bucket has a known cost", () => {
    expect(periodSpend([bucket(null), bucket(null)])).toBeNull();
    expect(periodSpend([])).toBeNull();
    expect(periodSpend([bucket(null), bucket(1.5), bucket(2)])).toBe(3.5);
    expect(periodSpend([bucket(0), bucket(null)])).toBe(0);
  });

  it("usage analytics: formats a latency as milliseconds, seconds or minutes", () => {
    expect(formatLatency(850)).toBe("850 ms");
    expect(formatLatency(0)).toBe("0 ms");
    expect(formatLatency(999.4)).toBe("999 ms");
    expect(formatLatency(1000)).toBe("1 s");
    expect(formatLatency(1234)).toBe("1.2 s");
    expect(formatLatency(14_100)).toBe("14.1 s");
    expect(formatLatency(59_940)).toBe("59.9 s");
    expect(formatLatency(59_960)).toBe("1 min");
    expect(formatLatency(65_000)).toBe("1 min 5 s");
    expect(formatLatency(120_000)).toBe("2 min");
  });

  it("usage analytics: a latency that is not known reads No data, never 0 ms", () => {
    expect(formatLatency(null)).toBe("No data");
    expect(formatLatency(undefined)).toBe("No data");
    expect(formatLatency(Number.NaN)).toBe("No data");
    expect(formatLatency(Number.POSITIVE_INFINITY)).toBe("No data");
    expect(formatLatency(Number.NEGATIVE_INFINITY)).toBe("No data");
    expect(formatLatency(0)).toBe("0 ms");
  });

  it("usage analytics: error rate is errors over requests and zero without requests", () => {
    expect(errorRate(200, 5)).toBe(0.025);
    expect(errorRate(0, 0)).toBe(0);
  });

  it("usage analytics: a range is empty only without calls and without tasks", () => {
    expect(isEmptyUsage([], [])).toBe(true);
    expect(isEmptyUsage([bucket(null)], [])).toBe(true);
    expect(isEmptyUsage([bucket(1, { requests: 1 })], [])).toBe(false);
    expect(isEmptyUsage([], [task("a", 1)])).toBe(false);
  });

  it("usage analytics: a task share is its cost over the spend, and none when either is unknown", () => {
    expect(taskShare(task("a", 2.5), 10)).toBe(0.25);
    expect(taskShare(task("a", null), 10)).toBeNull();
    expect(taskShare(task("a", 2.5), null)).toBeNull();
    expect(taskShare(task("a", 0), 10)).toBe(0);
    expect(taskShare(task("a", 1), 0)).toBeNull();
  });

  it("usage analytics: sorts tasks by cost, tokens or share, unknown last, ties in the order given", () => {
    const tasks = [task("a", 5, 100), task("b", 9, 50), task("c", null, 900), task("d", 5, 70), task("e", 1, 300)];
    const ids = (key: "cost" | "tokens" | "share", direction: "asc" | "desc") =>
      sortTasks(tasks, key, direction, 20).map((item) => item.task_id);
    expect(ids("cost", "desc")).toEqual(["b", "a", "d", "e", "c"]);
    expect(ids("cost", "asc")).toEqual(["e", "a", "d", "b", "c"]);
    expect(ids("tokens", "desc")).toEqual(["c", "e", "a", "d", "b"]);
    expect(ids("tokens", "asc")).toEqual(["b", "d", "a", "e", "c"]);
    expect(ids("share", "desc")).toEqual(["b", "a", "d", "e", "c"]);
    expect(ids("share", "asc")).toEqual(["e", "a", "d", "b", "c"]);
    expect(tasks.map((item) => item.task_id)).toEqual(["a", "b", "c", "d", "e"]);
  });

  it("usage analytics: tasks with equal tokens keep the order given, ascending and descending", () => {
    const tasks = [task("a", 1, 100), task("b", 1, 400), task("c", 1, 100), task("d", 1, 400)];
    const ids = (direction: "asc" | "desc") => sortTasks(tasks, "tokens", direction, 4).map((item) => item.task_id);
    expect(ids("desc")).toEqual(["b", "d", "a", "c"]);
    expect(ids("asc")).toEqual(["a", "c", "b", "d"]);
  });

  it("usage analytics: the costliest tasks come first, then the ones with unknown cost, up to the limit", () => {
    const tasks = [task("a", null), task("b", 1), task("c", 3), task("d", null), task("e", 2)];
    expect(costliestTasks(tasks, 8).map((item) => item.task_id)).toEqual(["c", "e", "b", "a", "d"]);
    expect(costliestTasks(tasks, 3).map((item) => item.task_id)).toEqual(["c", "e", "b"]);
  });

  it("usage analytics: reads the range from the URL and drops anything else", () => {
    expect(parseUsageRange("today")).toBe("today");
    expect(parseUsageRange(7)).toBe(7);
    expect(parseUsageRange("30")).toBe(30);
    expect(parseUsageRange(90)).toBe(90);
    expect(parseUsageRange(14)).toBeUndefined();
    expect(parseUsageRange("week")).toBeUndefined();
    expect(parseUsageRange(undefined)).toBeUndefined();
    expect(validateUsageSearch({ range: 30 })).toEqual({ range: 30 });
    expect(validateUsageSearch({ range: "nope" })).toEqual({});
  });

  it("usage polish 4113376c: compact tokens read 536k, 2.3M and 1.2B and never print a long decimal", () => {
    expect(formatTokensCompact(0)).toBe("0");
    expect(formatTokensCompact(42)).toBe("42");
    expect(formatTokensCompact(999)).toBe("999");
    expect(formatTokensCompact(1000)).toBe("1k");
    expect(formatTokensCompact(1500)).toBe("1.5k");
    expect(formatTokensCompact(536_440.143)).toBe("536k");
    expect(formatTokensCompact(2_330_160)).toBe("2.3M");
    expect(formatTokensCompact(1_200_000_000)).toBe("1.2B");
    expect(formatTokensCompact(251_181.429)).toBe("251k");
    expect(formatTokensCompact(-2500)).toBe("-2.5k");
  });

  it("usage polish 4113376c: compact tokens carry a rounded-up unit and tidy fractions below a thousand", () => {
    expect(formatTokensCompact(999_600)).toBe("1M");
    expect(formatTokensCompact(999.6)).toBe("1k");
    expect(formatTokensCompact(2.345)).toBe("2.35");
    expect(formatTokensCompact(12.34)).toBe("12.3");
    expect(formatTokensCompact(0.00123)).toBe("0.0012");
    expect(formatTokensCompact(Number.NaN)).toBe("");
    for (const value of [536_440.143, 65_706.429, 50_800.143, 2_100.5, 12.3456]) {
      expect(formatTokensCompact(value)).not.toMatch(/\d\.\d{2,}[kMBT]?$|,/);
    }
  });

  it("usage polish 4113376c: chart dollars read US$1.17 and percent points keep one decimal", () => {
    expect(formatUsd(1.1666)).toBe("US$1.17");
    expect(formatUsd(0.5)).toBe("US$0.50");
    expect(formatPercentPoints(66)).toBe("66%");
    expect(formatPercentPoints(12.5)).toBe("12.5%");
    expect(formatPercentPoints(3.64)).toBe("3.6%");
    expect(formatPercentPoints(0)).toBe("0%");
    expect(formatPercentPoints(100)).toBe("100%");
  });
});
