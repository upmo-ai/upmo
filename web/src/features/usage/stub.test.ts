import { describe, expect, it } from "vitest";

import { createStubApi, type UsageRange } from "../../app/api";

const ranges: UsageRange[] = ["today", 7, 30, 90];

/** Money to the 4 decimals the stub keeps, so sums of floats compare equal. */
function cents(value: number | null): number | null {
  return value === null ? null : Math.round(value * 10_000) / 10_000;
}

function sum(values: (number | null)[]): number {
  return values.reduce<number>((total, value) => total + (value ?? 0), 0);
}

describe("usage analytics stub", () => {
  it("usage analytics: hourly buckets for today and daily buckets for the longer ranges", async () => {
    const api = createStubApi();
    const today = await api.usage("today");
    expect(today.range).toBe("today");
    expect(today.bucket).toBe("hour");
    expect(today.buckets.length).toBeGreaterThan(1);
    expect(today.buckets.every((bucket) => /^2026-11-05T\d\d:00:00Z$/.test(bucket.at))).toBe(true);
    for (const range of [7, 30, 90] as const) {
      const usage = await api.usage(range);
      expect(usage.range).toBe(range);
      expect(usage.bucket).toBe("day");
      expect(usage.buckets).toHaveLength(range);
      expect(new Set(usage.buckets.map((bucket) => bucket.at)).size).toBe(range);
    }
  });

  it("usage analytics: has four providers, two global and two own, and six models", async () => {
    const usage = await createStubApi().usage(30);
    expect(usage.providers).toHaveLength(4);
    expect(usage.providers.filter((provider) => provider.scope === "global")).toHaveLength(2);
    expect(usage.providers.filter((provider) => provider.scope === "own")).toHaveLength(2);
    expect(usage.models).toHaveLength(6);
    const providerIds = new Set(usage.providers.map((provider) => provider.id));
    expect(usage.models.every((model) => providerIds.has(model.provider_id))).toBe(true);
    for (const provider of usage.providers) {
      expect(provider.latency_ms.p50).toBeLessThan(provider.latency_ms.p95);
      expect(provider.latency_ms.p95).toBeLessThan(provider.latency_ms.p99);
    }
  });

  it("usage analytics: provider, model and bucket totals add up in every range", async () => {
    const api = createStubApi();
    // Spend is null exactly when no part of it is known, otherwise the sum of the known parts.
    const total = (values: (number | null)[]): number | null => {
      const known = values.filter((value) => value !== null);
      return known.length === 0 ? null : sum(known);
    };
    for (const range of ranges) {
      const usage = await api.usage(range);
      const providers = usage.providers.map((provider) => {
        const shares = usage.buckets.flatMap((bucket) => bucket.by_provider[provider.id] ?? []);
        return {
          requests: [provider.requests, sum(shares.map((share) => share.requests))],
          errors: [provider.errors, sum(shares.map((share) => share.errors))],
          spend: [provider.spend_usd, shares.length === 0 ? 0 : total(shares.map((share) => share.spend_usd))],
        };
      });
      for (const item of providers) {
        expect(item.requests[0]).toBe(item.requests[1]);
        expect(item.errors[0]).toBe(item.errors[1]);
        expect(cents(item.spend[0] ?? null)).toBe(cents(item.spend[1] ?? null));
      }
      const models = usage.models.map((model) => {
        const shares = usage.buckets.flatMap((bucket) => bucket.by_model[model.id] ?? []);
        return { tokens: [model.tokens, sum(shares.map((share) => share.tokens))] };
      });
      for (const item of models) {
        expect(item.tokens[0]).toBe(item.tokens[1]);
      }
      for (const bucket of usage.buckets) {
        const shares = Object.values(bucket.by_model);
        expect(bucket.requests).toBe(sum(Object.values(bucket.by_provider).map((share) => share.requests)));
        expect(bucket.errors).toBe(sum(Object.values(bucket.by_provider).map((share) => share.errors)));
        expect(bucket.input + bucket.output + bucket.cache_read + bucket.cache_write).toBe(
          sum(shares.map((share) => share.tokens)),
        );
        const expected = total(shares.map((share) => share.spend_usd));
        expect(cents(bucket.spend_usd)).toBe(cents(expected));
      }
    }
  });

  it("usage analytics: has a bucket with unknown cost, a provider with no errors and a few unpriced calls", async () => {
    const api = createStubApi();
    const exactUnknown: Record<string, number> = { today: 1, "7": 1 };
    for (const range of ranges) {
      const usage = await api.usage(range);
      const unknown = usage.buckets.filter((bucket) => bucket.spend_usd === null);
      expect(unknown.length).toBeGreaterThan(0);
      // Exactly one for today and the week, so a bug that nulls every bucket cannot pass.
      expect(unknown.length).toBe(exactUnknown[String(range)] ?? unknown.length);
      expect(usage.providers.some((provider) => provider.errors === 0 && provider.requests > 0)).toBe(true);
      expect(usage.unpriced_calls).toBeGreaterThan(1);
      expect(usage.unpriced_calls).toBeLessThan(50);
    }
  });

  it("usage analytics: a provider or model without a price never has a spend of zero", async () => {
    const api = createStubApi();
    for (const range of ranges) {
      const usage = await api.usage(range);
      const ollama = usage.providers.find((provider) => provider.id === "ollama")!;
      const llama = usage.models.find((model) => model.id === "llama3.3:70b")!;
      expect(ollama.spend_usd).toBeNull();
      expect(llama.spend_usd).toBeNull();
      expect(usage.providers.filter((provider) => provider.id !== "ollama").every((provider) => provider.spend_usd !== null)).toBe(true);
    }
  });

  it("usage analytics: every task links to a task the stub knows, with the same goal", async () => {
    const api = createStubApi();
    const known = await api.listTasks();
    const usage = await api.usage(90);
    expect(usage.tasks.length).toBeGreaterThan(0);
    for (const task of usage.tasks) {
      const match = known.find((item) => item.id === task.task_id);
      expect(match?.title).toBe(task.goal);
    }
  });

  it("usage analytics: task costs add up to at most the spend and their model splits add up to the task", async () => {
    const api = createStubApi();
    for (const range of ranges) {
      const usage = await api.usage(range);
      const spend = sum(usage.buckets.map((bucket) => bucket.spend_usd));
      expect(usage.tasks.length).toBeGreaterThan(0);
      expect(sum(usage.tasks.map((task) => task.cost_usd))).toBeLessThanOrEqual(spend);
      for (const task of usage.tasks) {
        expect(task.by_model.length).toBeGreaterThan(0);
        expect(sum(task.by_model.map((part) => part.input))).toBe(task.input);
        expect(sum(task.by_model.map((part) => part.output))).toBe(task.output);
        expect(sum(task.by_model.map((part) => part.cache_read))).toBe(task.cache_read);
        expect(sum(task.by_model.map((part) => part.cache_write))).toBe(task.cache_write);
        const parts = task.by_model.map((part) => part.cost_usd);
        const known = parts.filter((value) => value !== null);
        expect(task.cost_usd === null).toBe(known.length === 0);
        expect(task.cost_usd ?? 0).toBeCloseTo(sum(known), 6);
      }
      const tokens = sum(usage.buckets.map((bucket) => bucket.input + bucket.output + bucket.cache_read + bucket.cache_write));
      expect(sum(usage.tasks.map((task) => task.input + task.output + task.cache_read + task.cache_write))).toBeLessThan(tokens);
    }
  });

  it("usage analytics: has a task with unknown cost in the week and gives longer ranges more tasks", async () => {
    const api = createStubApi();
    const [today, week, month, quarter] = await Promise.all(ranges.map((range) => api.usage(range)));
    expect(week!.tasks.some((task) => task.cost_usd === null)).toBe(true);
    expect(today!.tasks.length).toBeLessThan(week!.tasks.length);
    expect(week!.tasks.length).toBeLessThan(month!.tasks.length);
    expect(month!.tasks.length).toBeLessThan(quarter!.tasks.length);
  });

  it("usage analytics: the hours of today add up to the last day and each range contains the shorter ones", async () => {
    const api = createStubApi();
    const today = await api.usage("today");
    const week = await api.usage(7);
    const quarter = await api.usage(90);
    const day = week.buckets[week.buckets.length - 1]!;
    expect(sum(today.buckets.map((bucket) => bucket.requests))).toBe(day.requests);
    expect(sum(today.buckets.map((bucket) => bucket.input))).toBe(day.input);
    expect(sum(today.buckets.map((bucket) => bucket.spend_usd))).toBeCloseTo(day.spend_usd ?? 0, 6);
    expect(quarter.buckets.slice(-7)).toEqual(week.buckets);
  });

  it("usage analytics: answers with nothing recorded when the stub starts empty", async () => {
    const api = createStubApi({ emptyUsage: true });
    for (const range of ranges) {
      const usage = await api.usage(range);
      expect(usage.range).toBe(range);
      expect(usage.buckets).toEqual([]);
      expect(usage.tasks).toEqual([]);
      expect(usage.providers).toEqual([]);
      expect(usage.models).toEqual([]);
      expect(usage.unpriced_calls).toBe(0);
    }
  });
});
