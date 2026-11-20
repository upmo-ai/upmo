import { afterEach, describe, expect, it, vi } from "vitest";

import { BrainError } from "./api";
import { createBrainApi, toUsage, toUsageLimits, toWorkspaceUsage } from "./brain-api";

/** A bucket as the gateway sends it, in the person's zone. */
const bucket = {
  at: "2026-03-29T03:00:00+02:00",
  requests: 3,
  errors: 1,
  input: 100,
  output: 40,
  cache_read: 300,
  cache_write: 7,
  spend_micro_usd: 1_250_000,
  estimated: true,
  list_price_micro_usd: null,
  by_provider: { p1: { spend_micro_usd: 1_250_000, requests: 3, errors: 1, tokens: 447 } },
  by_model: { m1: { spend_micro_usd: null, tokens: 447 } },
};

const report = {
  range: "today",
  bucket: "hour",
  time_zone: "Europe/Berlin",
  spend_micro_usd: 1_250_000,
  estimated: true,
  unpriced_calls: 2,
  list_price_micro_usd: 3_500_000,
  buckets: [bucket],
  providers: [
    {
      id: "p1",
      name: "Anthropic",
      scope: "global",
      plan_kind: "metered",
      spend_micro_usd: 0,
      requests: 3,
      errors: 1,
      latency_ms: { p50: 1, p95: 2, p99: 3 },
    },
  ],
  models: [{ id: "m1", name: "m1", provider_id: "p1", spend_micro_usd: null, tokens: 447 }],
  sources: [{ source: "chat", requests: 3, tokens: 447, spend_micro_usd: null }],
  tasks: [],
};

/** A copy of a record with one key left out, or set to another value. */
function changed<T extends object>(source: T, key: string, value?: unknown): T {
  const { [key]: _left, ...rest } = source as Record<string, unknown>;
  return (value === undefined ? rest : { ...rest, [key]: value }) as T;
}

/** An absent money field and one that is not whole micro-USD. Neither may read as unknown or as 0. */
const notMoney = [undefined, 1.5, "1"];

describe("toUsage", () => {
  it("UPM-359 keeps a real 0 as 0 and unknown as null, and reads micro-USD as whole numbers", () => {
    const usage = toUsage(report);
    expect(usage.spend_micro_usd).toBe(1_250_000);
    expect(usage.providers[0]?.spend_micro_usd).toBe(0);
    expect(usage.models[0]?.spend_micro_usd).toBeNull();
    expect(usage.sources[0]?.spend_micro_usd).toBeNull();
    expect(usage.buckets[0]?.by_model.m1?.spend_micro_usd).toBeNull();
    expect(usage.buckets[0]?.list_price_micro_usd).toBeNull();
    expect(usage.list_price_micro_usd).toBe(3_500_000);
    expect(usage.estimated).toBe(true);
    expect(usage.range).toBe("today");
  });

  it("UPM-359 a spend the gateway left out is refused, never read as unknown or as 0", () => {
    const { spend_micro_usd: _spend, ...without } = report;
    expect(() => toUsage(without)).toThrow("no spend_micro_usd");
    expect(() => toUsage({ ...report, spend_micro_usd: "1" })).toThrow("no spend_micro_usd");
    expect(() => toUsage({ ...report, spend_micro_usd: 1.5 })).toThrow("no spend_micro_usd");
  });

  it("UPM-359 an absent or non-integer money field is refused on the list price, a bucket, a provider, a model and a source", () => {
    const places: [place: string, make: (value: unknown) => unknown, key: string][] = [
      ["list price", (value) => changed(report, "list_price_micro_usd", value), "list_price_micro_usd"],
      ["bucket spend", (value) => ({ ...report, buckets: [changed(bucket, "spend_micro_usd", value)] }), "spend_micro_usd"],
      [
        "bucket list price",
        (value) => ({ ...report, buckets: [changed(bucket, "list_price_micro_usd", value)] }),
        "list_price_micro_usd",
      ],
      [
        "provider spend",
        (value) => ({ ...report, providers: [changed(report.providers[0]!, "spend_micro_usd", value)] }),
        "spend_micro_usd",
      ],
      [
        "model spend",
        (value) => ({ ...report, models: [changed(report.models[0]!, "spend_micro_usd", value)] }),
        "spend_micro_usd",
      ],
      [
        "source spend",
        (value) => ({ ...report, sources: [changed(report.sources[0]!, "spend_micro_usd", value)] }),
        "spend_micro_usd",
      ],
    ];
    for (const [place, make, key] of places) {
      for (const value of notMoney) {
        expect(() => toUsage(make(value)), `${place} ${String(value)}`).toThrow(`The brain sent no ${key}.`);
      }
    }
    // The same fields as the gateway sends them are read.
    expect(() => toUsage(report)).not.toThrow();
  });

  it("UPM-359 keeps the bucket label as the gateway cut it in the person's zone", () => {
    expect(toUsage(report).buckets[0]?.at).toBe("2026-03-29T03:00:00+02:00");
    expect(toUsage(report).time_zone).toBe("Europe/Berlin");
  });

  it("UPM-359 reads a day count range as a number", () => {
    expect(toUsage({ ...report, range: 30, bucket: "day" }).range).toBe(30);
    expect(() => toUsage({ ...report, range: 14 })).toThrow("unknown range");
  });
});

describe("toUsageLimits", () => {
  const own = { limit_micro_usd: 50_000_000, workspace_limit_micro_usd: null, spent_micro_usd: 0, held_micro_usd: 250_000 };

  it("UPM-359 keeps no limit as null and nothing spent as a real 0", () => {
    const limits = toUsageLimits({ period_start: "2026-11-01", own });
    expect(limits.own).toEqual(own);
    expect(limits.own.workspace_limit_micro_usd).toBeNull();
    expect(limits.own.spent_micro_usd).toBe(0);
  });

  it("UPM-359 spend that is not a number is refused", () => {
    expect(() => toUsageLimits({ period_start: "2026-11-01", own: { ...own, spent_micro_usd: null } })).toThrow(
      "no spent_micro_usd",
    );
  });

  it("UPM-359 sends rows on only when the gateway sent them, even empty", () => {
    expect("rows" in toUsageLimits({ period_start: "2026-11-01", own })).toBe(false);
    expect(toUsageLimits({ period_start: "2026-11-01", own, rows: [] }).rows).toEqual([]);
    const row = { kind: "workspace", amount_micro_usd: null, spent_micro_usd: 5, held_micro_usd: 0 };
    expect(toUsageLimits({ period_start: "2026-11-01", own, rows: [row] }).rows).toEqual([row]);
  });

  it("UPM-359 rows the gateway sent as null do not count as sent, so they never offer the Workspace tab", () => {
    expect("rows" in toUsageLimits({ period_start: "2026-11-01", own, rows: null })).toBe(false);
    expect("rows" in toUsageLimits({ period_start: "2026-11-01", own, rows: "none" })).toBe(false);
  });

  it("UPM-359 an absent or non-integer money field is refused on a limit row and on the own limits", () => {
    const row = { kind: "workspace", amount_micro_usd: null, spent_micro_usd: 5, held_micro_usd: 0 };
    for (const key of ["amount_micro_usd", "spent_micro_usd", "held_micro_usd"]) {
      for (const value of notMoney) {
        expect(() => toUsageLimits({ period_start: "2026-11-01", own, rows: [changed(row, key, value)] }), `${key} ${String(value)}`).toThrow(
          `The brain sent no ${key}.`,
        );
      }
    }
    for (const key of ["limit_micro_usd", "workspace_limit_micro_usd", "held_micro_usd"]) {
      for (const value of notMoney) {
        expect(() => toUsageLimits({ period_start: "2026-11-01", own: changed(own, key, value) }), `${key} ${String(value)}`).toThrow(
          `The brain sent no ${key}.`,
        );
      }
    }
  });
});

describe("toWorkspaceUsage", () => {
  const row = {
    id: "u1",
    name: "Ada",
    requests: 2,
    errors: 0,
    input: 10,
    output: 5,
    cache_read: 0,
    cache_write: 0,
    reasoning: 1,
    tokens: 15,
    spend_micro_usd: null,
    estimated: false,
    unpriced_calls: 2,
    list_price_micro_usd: 2_000_000,
  };

  it("UPM-359 reads the group and the rows, with unknown spend as null", () => {
    const { sources: _sources, providers: _providers, models: _models, tasks: _tasks, ...base } = report;
    const usage = toWorkspaceUsage({ ...base, group: "team", rows: [row] });
    expect(usage.group).toBe("team");
    expect(usage.rows[0]?.spend_micro_usd).toBeNull();
    expect(usage.rows[0]?.list_price_micro_usd).toBe(2_000_000);
    expect(() => toWorkspaceUsage({ ...base, group: "galaxy", rows: [] })).toThrow("unknown group");
  });

  it("UPM-359 an absent or non-integer money field is refused on a workspace row and on the workspace's list price", () => {
    const { sources: _sources, providers: _providers, models: _models, tasks: _tasks, ...base } = report;
    for (const key of ["spend_micro_usd", "list_price_micro_usd"]) {
      for (const value of notMoney) {
        expect(() => toWorkspaceUsage({ ...base, group: "team", rows: [changed(row, key, value)] }), `row ${key} ${String(value)}`).toThrow(
          `The brain sent no ${key}.`,
        );
      }
    }
    for (const value of notMoney) {
      expect(() => toWorkspaceUsage(changed({ ...base, group: "team", rows: [row] }, "list_price_micro_usd", value))).toThrow(
        "The brain sent no list_price_micro_usd.",
      );
    }
  });
});

describe("createBrainApi usage", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("UPM-359 reads the real routes with the range and group as the gateway spells them", async () => {
    const calls: string[] = [];
    const { sources: _s, providers: _p, models: _m, tasks: _t, ...base } = report;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input);
        calls.push(path);
        expect(init?.credentials).toBe("include");
        if (path === "/api/tasks") return Response.json([]);
        if (path.startsWith("/api/usage/workspace")) return Response.json({ ...base, group: "agent", rows: [] });
        if (path === "/api/usage/limits") {
          return Response.json({
            period_start: "2026-11-01",
            own: { limit_micro_usd: null, workspace_limit_micro_usd: null, spent_micro_usd: 0, held_micro_usd: 0 },
          });
        }
        return Response.json(report);
      }),
    );
    const api = createBrainApi();
    expect((await api.usage("today")).spend_micro_usd).toBe(1_250_000);
    await api.usage(30);
    expect((await api.usageLimits()).own.spent_micro_usd).toBe(0);
    expect((await api.workspaceUsage(90, "agent")).group).toBe("agent");
    expect(calls).toEqual([
      "/api/tasks",
      "/api/usage?range=today",
      "/api/usage?range=30",
      "/api/usage/limits",
      "/api/usage/workspace?range=90&group=agent",
    ]);
  });

  it("UPM-359 a refused workspace read is a BrainError 403 and not a sign-out", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input: RequestInfo | URL) => {
        if (String(input) === "/api/tasks") return Response.json([]);
        return Response.json({ error: "Only people who may view usage can see the workspace's usage." }, { status: 403 });
      }),
    );
    const api = createBrainApi();
    const refused = api.workspaceUsage(7, "person");
    await expect(refused).rejects.toBeInstanceOf(BrainError);
    await expect(refused).rejects.toMatchObject({ status: 403 });
  });
});
