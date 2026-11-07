import type {
  Usage,
  UsageBucket,
  UsageModel,
  UsageModelShare,
  UsageProvider,
  UsageProviderShare,
  UsageRange,
  UsageTask,
  UsageTaskModel,
} from "../../app/api";

// Deterministic usage for the stub. Everything is built bottom-up from one cell
// per bucket and model, so provider, model, bucket and task figures always add up.
// Today is the 5th of November 2026 at 11:00 UTC; a longer range always contains
// the shorter ones with the same figures, and the hours of today add up to the
// last day.

const usageEnd = Date.UTC(2026, 10, 5);
const hourMs = 3_600_000;
const dayMs = 86_400_000;
const perMillion = 1_000_000;

/** Prices in USD per million tokens. */
interface Rate {
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
}

interface ModelSpec {
  id: string;
  name: string;
  provider_id: string;
  /** Share of a priced day's calls, in percent. */
  weight: number;
  /** Null when no price is configured for the model. */
  rate: Rate | null;
  /** Average tokens per call. */
  per_call: { input: number; output: number; cache_read: number; cache_write: number; tool_defs_est: number };
  /** Share of calls that fail, in percent. */
  error_pct: number;
}

const modelSpecs: ModelSpec[] = [
  {
    id: "claude-3-7-sonnet",
    name: "Claude 3.7 Sonnet",
    provider_id: "anthropic",
    weight: 30,
    rate: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 },
    per_call: { input: 2100, output: 520, cache_read: 5400, cache_write: 380, tool_defs_est: 700 },
    error_pct: 1.8,
  },
  {
    id: "claude-3-5-haiku",
    name: "Claude 3.5 Haiku",
    provider_id: "anthropic",
    weight: 24,
    rate: { input: 0.8, output: 4, cache_read: 0.08, cache_write: 1 },
    per_call: { input: 900, output: 240, cache_read: 2200, cache_write: 120, tool_defs_est: 450 },
    error_pct: 1.2,
  },
  {
    id: "gpt-4.1",
    name: "GPT-4.1",
    provider_id: "openai",
    weight: 20,
    rate: { input: 2, output: 8, cache_read: 0.5, cache_write: 0 },
    per_call: { input: 1800, output: 450, cache_read: 3000, cache_write: 0, tool_defs_est: 650 },
    error_pct: 2.4,
  },
  {
    id: "gpt-4.1-mini",
    name: "GPT-4.1 mini",
    provider_id: "openai",
    weight: 14,
    rate: { input: 0.4, output: 1.6, cache_read: 0.1, cache_write: 0 },
    per_call: { input: 700, output: 200, cache_read: 1200, cache_write: 0, tool_defs_est: 400 },
    error_pct: 0.9,
  },
  {
    id: "mistral-large",
    name: "Mistral Large",
    provider_id: "mistral",
    weight: 12,
    rate: { input: 2, output: 6, cache_read: 0, cache_write: 0 },
    per_call: { input: 1500, output: 380, cache_read: 0, cache_write: 0, tool_defs_est: 300 },
    error_pct: 0,
  },
  {
    id: "llama3.3:70b",
    name: "Llama 3.3 70B",
    provider_id: "ollama",
    weight: 0,
    rate: null,
    per_call: { input: 1200, output: 300, cache_read: 0, cache_write: 0, tool_defs_est: 0 },
    error_pct: 5,
  },
];

interface ProviderSpec {
  id: string;
  name: string;
  scope: "global" | "own";
  latency_ms: { p50: number; p95: number; p99: number };
}

const providerSpecs: ProviderSpec[] = [
  { id: "anthropic", name: "Anthropic", scope: "global", latency_ms: { p50: 1840, p95: 4200, p99: 7900 } },
  { id: "openai", name: "OpenAI", scope: "global", latency_ms: { p50: 1250, p95: 3100, p99: 6400 } },
  { id: "mistral", name: "Mistral", scope: "own", latency_ms: { p50: 980, p95: 2300, p99: 4100 } },
  { id: "ollama", name: "Ollama", scope: "own", latency_ms: { p50: 3400, p95: 8200, p99: 14100 } },
];

/** Relative number of calls in each hour of today so far (00:00 to 11:00). */
const hourWeights = [3, 2, 2, 0, 2, 5, 12, 24, 38, 46, 52, 44];

/** The hour of today with only unpriced calls, so its cost is unknown. */
const unpricedHour = 3;

interface Cell {
  requests: number;
  errors: number;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  tool_defs_est: number;
  cost: number | null;
}

function round(value: number, places: number): number {
  const factor = 10 ** places;
  return Math.round(value * factor) / factor;
}

function costOf(rate: Rate | null, tokens: Pick<Cell, "input" | "output" | "cache_read" | "cache_write">): number | null {
  if (!rate) {
    return null;
  }
  return (
    (tokens.input * rate.input +
      tokens.output * rate.output +
      tokens.cache_read * rate.cache_read +
      tokens.cache_write * rate.cache_write) /
    perMillion
  );
}

function makeCell(spec: ModelSpec, index: number, requests: number, seed: number): Cell | null {
  if (requests <= 0) {
    return null;
  }
  const wobble = (k: number) => 0.9 + ((seed * (k + 3) + index) % 5) * 0.05;
  const input = Math.round(requests * spec.per_call.input * wobble(1));
  const output = Math.round(requests * spec.per_call.output * wobble(2));
  const cacheRead = Math.round(requests * spec.per_call.cache_read * wobble(3));
  const cacheWrite = Math.round(requests * spec.per_call.cache_write * wobble(4));
  const errors =
    spec.error_pct === 0
      ? 0
      : Math.min(requests, Math.round((requests * spec.error_pct) / 100 + ((seed + index) % 3) * 0.5));
  const cost = costOf(spec.rate, { input, output, cache_read: cacheRead, cache_write: cacheWrite });
  return {
    requests,
    errors,
    input,
    output,
    cache_read: cacheRead,
    cache_write: cacheWrite,
    tool_defs_est: Math.round(requests * spec.per_call.tool_defs_est * wobble(5)),
    cost: cost === null ? null : round(cost, 4),
  };
}

function addCells(a: Cell | null, b: Cell | null): Cell | null {
  if (!a || !b) {
    return a ?? b;
  }
  return {
    requests: a.requests + b.requests,
    errors: a.errors + b.errors,
    input: a.input + b.input,
    output: a.output + b.output,
    cache_read: a.cache_read + b.cache_read,
    cache_write: a.cache_write + b.cache_write,
    tool_defs_est: a.tool_defs_est + b.tool_defs_est,
    cost: a.cost === null && b.cost === null ? null : round((a.cost ?? 0) + (b.cost ?? 0), 4),
  };
}

function hourCells(hour: number): (Cell | null)[] {
  return modelSpecs.map((spec, index) => {
    if (hour === unpricedHour) {
      return spec.rate === null ? makeCell(spec, index, 4, hour) : null;
    }
    if (spec.rate === null) {
      return null;
    }
    const jitter = 0.85 + ((hour * 3 + index) % 4) * 0.1;
    return makeCell(spec, index, Math.round(((hourWeights[hour] ?? 0) * 1.15 * spec.weight * jitter) / 100), hour);
  });
}

function dayCells(offset: number): (Cell | null)[] {
  if (offset === 0) {
    // Today is the sum of its hours.
    return modelSpecs.map((_, index) =>
      hourWeights.reduce<Cell | null>((sum, _weight, hour) => addCells(sum, hourCells(hour)[index] ?? null), null),
    );
  }
  const unknown = offset % 9 === 3;
  const base = 190 + ((offset * 37) % 11) * 14;
  return modelSpecs.map((spec, index) => {
    if (spec.rate === null) {
      // Only an unpriced model answers on a day with unknown cost: a few calls.
      return unknown ? makeCell(spec, index, 3 + (offset % 4), offset) : null;
    }
    if (unknown) {
      return null;
    }
    const jitter = 0.85 + ((offset * (index + 2) + index) % 4) * 0.1;
    return makeCell(spec, index, Math.round((base * spec.weight * jitter) / 100), offset);
  });
}

function sumKnown(values: (number | null)[]): number | null {
  const known = values.filter((value): value is number => value !== null);
  return known.length === 0 ? null : round(known.reduce((sum, value) => sum + value, 0), 4);
}

function bucketFrom(at: string, cells: (Cell | null)[]): UsageBucket {
  const byModel: Record<string, UsageModelShare> = {};
  const costs: Record<string, (number | null)[]> = {};
  const byProvider: Record<string, UsageProviderShare> = {};
  const total = { requests: 0, errors: 0, input: 0, output: 0, cache_read: 0, cache_write: 0, tool_defs_est: 0 };

  cells.forEach((cell, index) => {
    const spec = modelSpecs[index];
    if (!cell || !spec) {
      return;
    }
    const tokens = cell.input + cell.output + cell.cache_read + cell.cache_write;
    byModel[spec.id] = { spend_usd: cell.cost, tokens };
    (costs[spec.provider_id] ??= []).push(cell.cost);
    const share = (byProvider[spec.provider_id] ??= { spend_usd: null, requests: 0, errors: 0, tokens: 0 });
    share.requests += cell.requests;
    share.errors += cell.errors;
    share.tokens += tokens;
    total.requests += cell.requests;
    total.errors += cell.errors;
    total.input += cell.input;
    total.output += cell.output;
    total.cache_read += cell.cache_read;
    total.cache_write += cell.cache_write;
    total.tool_defs_est += cell.tool_defs_est;
  });
  for (const [id, share] of Object.entries(byProvider)) {
    share.spend_usd = sumKnown(costs[id] ?? []);
  }

  return {
    at,
    ...total,
    spend_usd: sumKnown(cells.map((cell) => cell?.cost ?? null)),
    by_provider: byProvider,
    by_model: byModel,
  };
}

function cellsFor(range: UsageRange): { unit: "hour" | "day"; slots: { at: string; cells: (Cell | null)[] }[] } {
  if (range === "today") {
    return {
      unit: "hour",
      slots: hourWeights.map((_weight, hour) => ({
        at: `${new Date(usageEnd + hour * hourMs).toISOString().slice(0, 13)}:00:00Z`,
        cells: hourCells(hour),
      })),
    };
  }
  const slots: { at: string; cells: (Cell | null)[] }[] = [];
  for (let offset = range - 1; offset >= 0; offset -= 1) {
    slots.push({ at: new Date(usageEnd - offset * dayMs).toISOString().slice(0, 10), cells: dayCells(offset) });
  }
  return { unit: "day", slots };
}

/** What a task spent on one model, in tokens. */
type TaskPart = [modelId: string, input: number, output: number, cacheRead: number, cacheWrite: number];

/**
 * `age` is how many days ago the task last ran. The ids and goals are those of
 * the stub tasks, so every row links to a task page that exists.
 */
const taskFixtures: { age: number; task_id: string; goal: string; parts: TaskPart[] }[] = [
  {
    age: 0,
    task_id: "aster-digest",
    goal: "Daily finance digest",
    parts: [
      ["claude-3-7-sonnet", 150_000, 42_000, 60_000, 16_000],
      ["claude-3-5-haiku", 60_000, 16_000, 20_000, 6_000],
    ],
  },
  { age: 0, task_id: "aster-mail", goal: "Mail triage", parts: [["claude-3-5-haiku", 41_000, 9_000, 30_000, 4_000]] },
  {
    age: 2,
    task_id: "aster-probe",
    goal: "Fix the flaky login test",
    parts: [["llama3.3:70b", 190_000, 12_000, 0, 0]],
  },
  {
    age: 12,
    task_id: "aster-done",
    goal: "File the receipts",
    parts: [
      ["claude-3-5-haiku", 60_000, 13_000, 70_000, 6_000],
      ["mistral-large", 38_000, 8_000, 0, 0],
    ],
  },
  {
    age: 45,
    task_id: "aster-failed",
    goal: "Weekly close",
    parts: [["claude-3-7-sonnet", 240_000, 70_000, 20_000, 32_000]],
  },
];

function taskFrom(fixture: (typeof taskFixtures)[number]): UsageTask {
  const byModel: UsageTaskModel[] = fixture.parts.map(([modelId, input, output, cacheRead, cacheWrite]) => {
    const spec = modelSpecs.find((item) => item.id === modelId);
    const cost = costOf(spec?.rate ?? null, { input, output, cache_read: cacheRead, cache_write: cacheWrite });
    return {
      model_id: modelId,
      cost_usd: cost === null ? null : round(cost, 2),
      input,
      output,
      cache_read: cacheRead,
      cache_write: cacheWrite,
    };
  });
  const sum = (pick: (part: UsageTaskModel) => number) => byModel.reduce((total, part) => total + pick(part), 0);
  return {
    task_id: fixture.task_id,
    goal: fixture.goal,
    input: sum((part) => part.input),
    output: sum((part) => part.output),
    cache_read: sum((part) => part.cache_read),
    cache_write: sum((part) => part.cache_write),
    cost_usd: sumKnown(byModel.map((part) => part.cost_usd)),
    by_model: byModel,
  };
}

/** A provider is priced when at least one of its models has a price; with none, its spend is never known. */
function providerPriced(providerId: string): boolean {
  return modelSpecs.some((spec) => spec.provider_id === providerId && spec.rate !== null);
}

const rangeShift: Record<string, number> = { today: 0, "7": 25, "30": -15, "90": 40 };

export function usageFor(range: UsageRange, options: { empty?: boolean } = {}): Usage {
  const { unit, slots } = cellsFor(range);
  const buckets = slots.map((slot) => bucketFrom(slot.at, slot.cells));
  if (options.empty) {
    return { range, bucket: unit, unpriced_calls: 0, buckets: [], providers: [], models: [], tasks: [] };
  }

  const unpricedCalls = slots.reduce(
    (sum, slot) =>
      sum +
      slot.cells.reduce((inner, cell, index) => (modelSpecs[index]?.rate === null ? inner + (cell?.requests ?? 0) : inner), 0),
    0,
  );

  const shift = rangeShift[String(range)] ?? 0;
  const providers: UsageProvider[] = providerSpecs.map((spec) => {
    const shares = buckets.flatMap((bucket) => bucket.by_provider[spec.id] ?? []);
    return {
      id: spec.id,
      name: spec.name,
      scope: spec.scope,
      spend_usd: shares.length === 0 ? (providerPriced(spec.id) ? 0 : null) : sumKnown(shares.map((share) => share.spend_usd)),
      requests: shares.reduce((sum, share) => sum + share.requests, 0),
      errors: shares.reduce((sum, share) => sum + share.errors, 0),
      latency_ms: {
        p50: spec.latency_ms.p50 + shift,
        p95: spec.latency_ms.p95 + shift * 2,
        p99: spec.latency_ms.p99 + shift * 3,
      },
    };
  });
  const models: UsageModel[] = modelSpecs.map((spec) => {
    const shares = buckets.flatMap((bucket) => bucket.by_model[spec.id] ?? []);
    return {
      id: spec.id,
      name: spec.name,
      provider_id: spec.provider_id,
      spend_usd: shares.length === 0 ? (spec.rate === null ? null : 0) : sumKnown(shares.map((share) => share.spend_usd)),
      tokens: shares.reduce((sum, share) => sum + share.tokens, 0),
    };
  });
  const days = range === "today" ? 1 : range;

  return {
    range,
    bucket: unit,
    unpriced_calls: unpricedCalls,
    buckets,
    providers,
    models,
    tasks: taskFixtures.filter((fixture) => fixture.age < days).map(taskFrom),
  };
}
