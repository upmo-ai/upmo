import { describe, expect, it } from "vitest";

import type { Usage, UsageBucket } from "../../app/api";
import { modelSpendSeries, providerSpendSeries, tokenConsumptionSeries } from "./series";

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
});
