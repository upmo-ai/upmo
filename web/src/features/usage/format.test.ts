import { describe, expect, it } from "vitest";

import { createStubApi, type UsageBucket, type UsageTask } from "../../app/api";
import {
  cacheHitRate,
  costliestTasks,
  errorRate,
  isEmptyUsage,
  limitUse,
  periodCacheHit,
  sortTasks,
  sourceLabel,
  taskShare,
  taskTokens,
} from "./figures";
import {
  formatBucket,
  formatDay,
  formatHour,
  formatLatency,
  formatMicroUsd,
  formatPercentPoints,
  formatRate,
  formatRatePrecise,
  formatSpend,
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
    spend_micro_usd: spend,
    estimated: false,
    list_price_micro_usd: null,
    by_provider: {},
    by_model: {},
    ...extra,
  };
}

function task(id: string, cost: number | null, tokens = 1): UsageTask {
  return { task_id: id, goal: id, input: tokens, output: 0, cache_read: 0, cache_write: 0, cost_micro_usd: cost, by_model: [] };
}

describe("usage figures", () => {
  it("never prints unknown cost as zero", () => {
    expect(formatUsd(null)).toBe("Unknown");
    expect(formatUsd(undefined)).toBe("Unknown");
    expect(formatUsd(0)).not.toBe("Unknown");
    expect(formatUsd(0)).toMatch(/0/);
    expect(formatUsd(12.47)).toMatch(/12\.47/);
  });

  it("UPM-359 unknown micro-USD is Unknown and a real zero is US$0.00, never the same text", () => {
    expect(formatMicroUsd(null)).toBe("Unknown");
    expect(formatMicroUsd(undefined)).toBe("Unknown");
    expect(formatMicroUsd(0)).toBe("US$0.00");
    expect(formatMicroUsd(0)).not.toBe(formatMicroUsd(null));
  });

  it("UPM-359 divides micro-USD only at display and keeps a tiny real cost visible", () => {
    expect(formatMicroUsd(12_470_000)).toBe("US$12.47");
    expect(formatMicroUsd(41_250_000)).toBe("US$41.25");
    expect(formatMicroUsd(10_000)).toBe("US$0.01");
    expect(formatMicroUsd(1_200)).toBe("US$0.0012");
    expect(formatMicroUsd(100)).toBe("US$0.0001");
    expect(formatMicroUsd(99)).toBe("Under US$0.0001");
  });

  it("UPM-359 an estimated spend says so, and an unknown one does not claim to be an estimate", () => {
    expect(formatSpend(2_500_000, true)).toBe("US$2.50 (estimated)");
    expect(formatSpend(2_500_000, false)).toBe("US$2.50");
    expect(formatSpend(0, true)).toBe("US$0.00 (estimated)");
    expect(formatSpend(null, true)).toBe("Unknown");
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

  it("UPM-359 an hour keeps the clock reading the server sent, in the person's zone", () => {
    expect(formatHour("2026-11-05T09:00:00+02:00")).toBe("09:00");
    expect(formatHour("2026-11-05T23:00:00-05:00")).toBe("23:00");
    // The spring-forward day has no 02:00 and the label does not invent one.
    expect(formatHour("2026-03-29T01:00:00+01:00")).toBe("01:00");
    expect(formatHour("2026-03-29T03:00:00+02:00")).toBe("03:00");
    expect(formatBucket("2026-03-29T03:00:00+02:00", "hour")).toBe("03:00");
  });

  it("usage panels: formats an hour as a 24 hour clock in UTC and labels buckets by their unit", () => {
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

  it("the period cache hit rate is between none and all of the input", async () => {
    const { buckets } = await createStubApi().usage(7);
    expect(periodCacheHit(buckets)).toBeGreaterThan(0);
    expect(periodCacheHit(buckets)).toBeLessThan(1);
  });

  it("UPM-204 adds all four token fields of a task and nothing else", () => {
    const base = { task_id: "t", goal: "t", cost_micro_usd: null, by_model: [] };
    expect(taskTokens({ ...base, input: 1, output: 20, cache_read: 300, cache_write: 4000 })).toBe(4321);
    expect(taskTokens({ ...base, input: 7, output: 0, cache_read: 0, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 7, cache_read: 0, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 7, cache_write: 0 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 0, cache_write: 7 })).toBe(7);
    expect(taskTokens({ ...base, input: 0, output: 0, cache_read: 0, cache_write: 0 })).toBe(0);
  });

  it("UPM-359 a limit turns near at 80% of it exactly and over at the limit", () => {
    expect(limitUse(0, 50_000_000)).toEqual({ percent: 0, state: "ok" });
    expect(limitUse(39_999_999, 50_000_000).state).toBe("ok");
    expect(limitUse(40_000_000, 50_000_000)).toEqual({ percent: 80, state: "near" });
    expect(limitUse(49_999_999, 50_000_000).state).toBe("near");
    expect(limitUse(50_000_000, 50_000_000)).toEqual({ percent: 100, state: "over" });
    expect(limitUse(75_000_000, 50_000_000)).toEqual({ percent: 100, state: "over" });
  });

  it("UPM-359 a limit of zero allows nothing, so spending is over it and not spending is fine", () => {
    expect(limitUse(0, 0)).toEqual({ percent: 0, state: "ok" });
    expect(limitUse(1, 0)).toEqual({ percent: 100, state: "over" });
  });

  it("UPM-359 names the kinds of work and shows one it does not know as sent", () => {
    expect(sourceLabel("chat")).toBe("Chat");
    expect(sourceLabel("task")).toBe("Tasks");
    expect(sourceLabel("automation")).toBe("Automations");
    expect(sourceLabel("something-new")).toBe("something-new");
  });

  it("usage panels: formats a latency as milliseconds, seconds or minutes", () => {
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

  it("usage panels: a latency that is not known reads No data, never 0 ms", () => {
    expect(formatLatency(null)).toBe("No data");
    expect(formatLatency(undefined)).toBe("No data");
    expect(formatLatency(Number.NaN)).toBe("No data");
    expect(formatLatency(Number.POSITIVE_INFINITY)).toBe("No data");
    expect(formatLatency(Number.NEGATIVE_INFINITY)).toBe("No data");
    expect(formatLatency(0)).toBe("0 ms");
  });

  it("usage panels: error rate is errors over requests and zero without requests", () => {
    expect(errorRate(200, 5)).toBe(0.025);
    expect(errorRate(0, 0)).toBe(0);
  });

  it("usage panels: a range is empty only without calls and without tasks", () => {
    expect(isEmptyUsage([], [])).toBe(true);
    expect(isEmptyUsage([bucket(null)], [])).toBe(true);
    expect(isEmptyUsage([bucket(1, { requests: 1 })], [])).toBe(false);
    expect(isEmptyUsage([], [task("a", 1)])).toBe(false);
  });

  it("usage panels: a task share is its cost over the spend, and none when either is unknown", () => {
    expect(taskShare(task("a", 2_500_000), 10_000_000)).toBe(0.25);
    expect(taskShare(task("a", null), 10_000_000)).toBeNull();
    expect(taskShare(task("a", 2_500_000), null)).toBeNull();
    expect(taskShare(task("a", 0), 10_000_000)).toBe(0);
    expect(taskShare(task("a", 1), 0)).toBeNull();
  });

  it("usage panels: sorts tasks by cost, tokens or share, unknown last, ties in the order given", () => {
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

  it("usage panels: tasks with equal tokens keep the order given, ascending and descending", () => {
    const tasks = [task("a", 1, 100), task("b", 1, 400), task("c", 1, 100), task("d", 1, 400)];
    const ids = (direction: "asc" | "desc") => sortTasks(tasks, "tokens", direction, 4).map((item) => item.task_id);
    expect(ids("desc")).toEqual(["b", "d", "a", "c"]);
    expect(ids("asc")).toEqual(["a", "c", "b", "d"]);
  });

  it("usage panels: the costliest tasks come first, then the ones with unknown cost, up to the limit", () => {
    const tasks = [task("a", null), task("b", 1), task("c", 3), task("d", null), task("e", 2)];
    expect(costliestTasks(tasks, 8).map((item) => item.task_id)).toEqual(["c", "e", "b", "a", "d"]);
    expect(costliestTasks(tasks, 3).map((item) => item.task_id)).toEqual(["c", "e", "b"]);
  });

  it("usage panels: reads the range from the URL and drops anything else", () => {
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
