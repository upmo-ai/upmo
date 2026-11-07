import { describe, expect, it } from "vitest";

import type { Usage, UsageBucket, UsageTask } from "../../app/api";
import {
  cacheHitSeries,
  modelSpendSeries,
  providerSpendSeries,
  spendGroups,
  taskCostGroups,
  tokenConsumptionSeries,
} from "./series";

function bucket(extra: Partial<UsageBucket>): UsageBucket {
  return {
    at: "2026-11-01",
    requests: 0,
    errors: 0,
    input: 0,
    output: 0,
    cache_read: 0,
    cache_write: 0,
    tool_defs_est: 0,
    spend_usd: 0,
    by_provider: {},
    by_model: {},
    ...extra,
  };
}

function usageOf(buckets: UsageBucket[]): Usage {
  return {
    range: 7,
    bucket: "day",
    unpriced_calls: 0,
    buckets,
    providers: [
      { id: "mistral", name: "Mistral", scope: "own", spend_usd: 1, requests: 2, errors: 0, latency_ms: { p50: 1, p95: 2, p99: 3 } },
    ],
    models: [{ id: "mistral-large", name: "Mistral Large", provider_id: "mistral", spend_usd: 1, tokens: 20 }],
    tasks: [],
  };
}

function values(series: { points: { value: number | null }[] }): (number | null)[] {
  return series.points.map((point) => point.value);
}

describe("usage series", () => {
  it("usage analytics: a token stack adds up to input plus output, also when tool definitions exceed input", () => {
    const usage = usageOf([
      bucket({ at: "2026-11-01", input: 100, output: 40, tool_defs_est: 30 }),
      bucket({ at: "2026-11-02", input: 100, output: 40, tool_defs_est: 100 }),
      bucket({ at: "2026-11-03", input: 100, output: 40, tool_defs_est: 150 }),
      bucket({ at: "2026-11-04", input: 0, output: 5, tool_defs_est: 20 }),
    ]);
    const [rest, output, tools] = tokenConsumptionSeries(usage).map(values);
    expect(rest).toEqual([70, 0, 0, 0]);
    expect(output).toEqual([40, 40, 40, 5]);
    expect(tools).toEqual([30, 100, 100, 0]);
    usage.buckets.forEach((item, index) => {
      expect(rest![index]! + output![index]! + tools![index]!).toBe(item.input + item.output);
    });
  });

  it("usage analytics: a row absent from a bucket spent nothing there even when the bucket's spend is unknown", () => {
    const usage = usageOf([
      bucket({ at: "2026-11-01", requests: 0, spend_usd: null }),
      bucket({
        at: "2026-11-02",
        requests: 2,
        spend_usd: 1,
        by_provider: { mistral: { spend_usd: null, requests: 2, errors: 0, tokens: 20 } },
        by_model: { "mistral-large": { spend_usd: null, tokens: 20 } },
      }),
      bucket({
        at: "2026-11-03",
        requests: 2,
        spend_usd: 1,
        by_provider: { mistral: { spend_usd: 1, requests: 2, errors: 0, tokens: 20 } },
        by_model: { "mistral-large": { spend_usd: 1, tokens: 20 } },
      }),
    ]);
    expect(values(providerSpendSeries(usage)[0]!)).toEqual([0, null, 1]);
    expect(values(modelSpendSeries(usage)[0]!)).toEqual([0, null, 1]);
  });

  it("usage polish 4113376c: unknown spend is a gap in the spend bars, known zero stays a bar", () => {
    const usage = usageOf([
      bucket({ at: "2026-11-01", spend_usd: 1.5 }),
      bucket({ at: "2026-11-02", spend_usd: null }),
      bucket({ at: "2026-11-03", spend_usd: 0 }),
    ]);
    const groups = spendGroups(usage);
    expect(groups.map((group) => group.label)).toEqual(["1 Nov", "2 Nov", "3 Nov"]);
    expect(groups.map((group) => group.bars)).toEqual([
      [{ name: "Spend", value: 1.5 }],
      [{ name: "Spend", value: null }],
      [{ name: "Spend", value: 0 }],
    ]);
  });

  it("usage polish 4113376c: the cache hit rate is one series in percent points, with a gap where nothing was read", () => {
    const usage = usageOf([
      bucket({ at: "2026-11-01", input: 100, cache_read: 300 }),
      bucket({ at: "2026-11-02", input: 0, cache_read: 0 }),
      bucket({ at: "2026-11-03", input: 50, cache_read: 0 }),
    ]);
    const series = cacheHitSeries(usage);
    expect(series).toHaveLength(1);
    expect(series[0]!.name).toBe("Cache hit rate");
    expect(values(series[0]!)).toEqual([75, null, 0]);
  });

  it("usage polish 4113376c: the cost chart keeps the costliest tasks and puts unknown cost last as a gap", () => {
    const task = (id: string, cost: number | null): UsageTask => ({
      task_id: id,
      goal: id,
      input: 1,
      output: 0,
      cache_read: 0,
      cache_write: 0,
      cost_usd: cost,
      by_model: [],
    });
    const usage = {
      ...usageOf([]),
      tasks: [task("n1", null), task("a", 1), task("b", 3), task("c", 2), task("n2", null)],
    };
    expect(taskCostGroups(usage, 4).map((group) => [group.label, group.bars[0]!.value])).toEqual([
      ["b", 3],
      ["c", 2],
      ["a", 1],
      ["n1", null],
    ]);
    expect(taskCostGroups(usage)).toHaveLength(5);
    expect(taskCostGroups(usage).every((group) => group.bars.length === 1 && group.bars[0]!.name === "Cost")).toBe(true);
  });
});
