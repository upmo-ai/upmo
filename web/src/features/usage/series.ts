import type { BarGroup, LineSeries } from "@upmo-ai/components";

import type { Usage, UsageBucket, UsageProvider } from "../../app/api";
import { cacheHitRate, costliestTasks, totalRequests } from "./figures";
import { formatBucket } from "./format";

/** How many tasks the cost chart shows. */
export const chartTasks = 8;

type SeriesColor = LineSeries["colorIndex"];

/** The chart palette has six colours. The same row keeps its colour in every chart; later rows fall to the chart's own order. */
function colorAt(index: number): SeriesColor {
  return index < 6 ? ((index + 1) as SeriesColor) : undefined;
}

/** A provider's name in a chart, with the marker for providers shared by everyone. */
export function providerLabel(provider: Pick<UsageProvider, "name" | "scope">): string {
  return provider.scope === "global" ? `${provider.name} (global)` : provider.name;
}

/** Whether any call was recorded in the range. Without one, every usage panel shows its empty state. */
export function hasCalls(usage: Usage): boolean {
  return totalRequests(usage.buckets) > 0;
}

/**
 * Spend of one provider or model in a bucket. A row present with unknown spend
 * is a gap. A row absent from the bucket made no calls there, so it spent
 * nothing, whatever the bucket's own spend is.
 */
function spendPoint(share: { spend_micro_usd: number | null } | undefined): number | null {
  return share ? share.spend_micro_usd : 0;
}

interface Row {
  id: string;
  name: string;
  spend_micro_usd: number | null;
}

function spendSeries(
  usage: Usage,
  rows: Row[],
  share: (bucket: UsageBucket, id: string) => { spend_micro_usd: number | null } | undefined,
): LineSeries[] {
  return rows.flatMap((row, index) =>
    // A row whose spend is unknown for the whole range has nothing to draw: it is listed as unpriced instead.
    row.spend_micro_usd === null
      ? []
      : [
          {
            name: row.name,
            colorIndex: colorAt(index),
            points: usage.buckets.map((bucket) => ({
              label: formatBucket(bucket.at, usage.bucket),
              value: spendPoint(share(bucket, row.id)),
            })),
          },
        ],
  );
}

export function providerSpendSeries(usage: Usage): LineSeries[] {
  const rows = usage.providers.map((provider) => ({
    id: provider.id,
    name: providerLabel(provider),
    spend_micro_usd: provider.spend_micro_usd,
  }));
  return spendSeries(usage, rows, (bucket, id) => bucket.by_provider[id]);
}

export function modelSpendSeries(usage: Usage): LineSeries[] {
  return spendSeries(usage, usage.models, (bucket, id) => bucket.by_model[id]);
}

/** Tokens are always known, so a model with no calls in a bucket has 0 tokens there. */
export function modelTokenSeries(usage: Usage): LineSeries[] {
  return usage.models.flatMap((model, index) =>
    model.tokens === 0
      ? []
      : [
          {
            name: model.name,
            colorIndex: colorAt(index),
            points: usage.buckets.map((bucket) => ({
              label: formatBucket(bucket.at, usage.bucket),
              value: bucket.by_model[model.id]?.tokens ?? 0,
            })),
          },
        ],
  );
}

/** Four layers that add up to every token of the bucket: input, output, cache read and cache write. */
export function tokenConsumptionSeries(usage: Usage): LineSeries[] {
  const series = (name: string, pick: (bucket: UsageBucket) => number): LineSeries => ({
    name,
    points: usage.buckets.map((bucket) => ({
      label: formatBucket(bucket.at, usage.bucket),
      value: pick(bucket),
    })),
  });
  return [
    series("Input", (bucket) => bucket.input),
    series("Output", (bucket) => bucket.output),
    series("Cache read", (bucket) => bucket.cache_read),
    series("Cache write", (bucket) => bucket.cache_write),
  ];
}

/** Providers that took calls, with their latency. A provider without calls has no latency to show. */
export function latencyProviders(usage: Usage): UsageProvider[] {
  return usage.providers.filter((provider) => provider.requests > 0);
}

/** Providers that took calls but have no price anywhere in the range, so the spend charts leave them out. */
export function unpricedProviders(usage: Usage): string[] {
  return usage.providers.filter((provider) => provider.spend_micro_usd === null && provider.requests > 0).map(providerLabel);
}

/** Models that took tokens but have no price anywhere in the range, so the spend charts leave them out. */
export function unpricedModels(usage: Usage): string[] {
  return usage.models.filter((model) => model.spend_micro_usd === null && model.tokens > 0).map((model) => model.name);
}

export interface SpendShare {
  /** Providers with a known spend, biggest first. */
  slices: { label: string; value: number }[];
  /** Providers that took calls but have no price, so their spend is unknown. */
  unpriced: string[];
}

export function spendShare(usage: Usage): SpendShare {
  const slices = usage.providers
    .flatMap((provider) =>
      provider.spend_micro_usd === null ? [] : [{ label: providerLabel(provider), value: provider.spend_micro_usd }],
    )
    .sort((a, b) => b.value - a.value);
  return { slices, unpriced: unpricedProviders(usage) };
}

/** Spend per bucket as one bar each. A bucket with unknown spend is a gap, never a bar of zero. */
export function spendGroups(usage: Usage): BarGroup[] {
  return usage.buckets.map((bucket) => ({
    label: formatBucket(bucket.at, usage.bucket),
    bars: [{ name: "Spend", value: bucket.spend_micro_usd }],
  }));
}

/**
 * The share of input served from cache in each bucket, in percent points. A
 * bucket that read no input at all has no rate, so it is a gap rather than 0%.
 */
export function cacheHitSeries(usage: Usage): LineSeries[] {
  return [
    {
      name: "Cache hit rate",
      points: usage.buckets.map((bucket) => ({
        label: formatBucket(bucket.at, usage.bucket),
        value: bucket.input + bucket.cache_read === 0 ? null : cacheHitRate(bucket.input, bucket.cache_read) * 100,
      })),
    },
  ];
}

/** The costliest tasks, one bar each, biggest first. A task with unknown cost goes last as a gap, never a bar of zero. */
export function taskCostGroups(usage: Usage, limit = chartTasks): BarGroup[] {
  return costliestTasks(usage.tasks, limit).map((task) => ({
    label: task.goal,
    bars: [{ name: "Cost", value: task.cost_micro_usd }],
  }));
}
