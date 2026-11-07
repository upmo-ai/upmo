import type { LineSeries } from "@upmo-ai/components";

import type { Usage, UsageBucket, UsageProvider } from "../../app/api";
import { totalRequests } from "./figures";
import { formatBucket } from "./format";

type SeriesColor = LineSeries["colorIndex"];

/** The chart palette has six colours. The same row keeps its colour in every chart; later rows fall to the chart's own order. */
function colorAt(index: number): SeriesColor {
  return index < 6 ? ((index + 1) as SeriesColor) : undefined;
}

/** A provider's name in a chart, with the marker for providers shared by everyone. */
export function providerLabel(provider: Pick<UsageProvider, "name" | "scope">): string {
  return provider.scope === "global" ? `${provider.name} (global)` : provider.name;
}

/** Whether any call was recorded in the range. Without one, every analytics panel shows its empty state. */
export function hasCalls(usage: Usage): boolean {
  return totalRequests(usage.buckets) > 0;
}

/**
 * Spend of one provider or model in a bucket. A row present with unknown spend
 * is a gap. A row absent from the bucket made no calls there, so it spent
 * nothing, whatever the bucket's own spend is.
 */
function spendPoint(share: { spend_usd: number | null } | undefined): number | null {
  return share ? share.spend_usd : 0;
}

interface Row {
  id: string;
  name: string;
  spend_usd: number | null;
}

function spendSeries(
  usage: Usage,
  rows: Row[],
  share: (bucket: UsageBucket, id: string) => { spend_usd: number | null } | undefined,
): LineSeries[] {
  return rows.flatMap((row, index) =>
    // A row whose spend is unknown for the whole range has nothing to draw: it is listed as unpriced instead.
    row.spend_usd === null
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
    spend_usd: provider.spend_usd,
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

/** The estimated tool definitions are part of input, so the slice never exceeds it. */
function toolDefinitions(bucket: UsageBucket): number {
  return Math.min(bucket.tool_defs_est, bucket.input);
}

/** Three layers that add up to input + output in every bucket. */
export function tokenConsumptionSeries(usage: Usage): LineSeries[] {
  const series = (name: string, pick: (bucket: UsageBucket) => number): LineSeries => ({
    name,
    points: usage.buckets.map((bucket) => ({
      label: formatBucket(bucket.at, usage.bucket),
      value: pick(bucket),
    })),
  });
  return [
    series("Input excluding tool definitions", (bucket) => bucket.input - toolDefinitions(bucket)),
    series("Output", (bucket) => bucket.output),
    series("Tool definitions (est.)", toolDefinitions),
  ];
}

/** Providers that took calls, with their latency. A provider without calls has no latency to show. */
export function latencyProviders(usage: Usage): UsageProvider[] {
  return usage.providers.filter((provider) => provider.requests > 0);
}

/** Providers that took calls but have no price anywhere in the range, so the spend charts leave them out. */
export function unpricedProviders(usage: Usage): string[] {
  return usage.providers.filter((provider) => provider.spend_usd === null && provider.requests > 0).map(providerLabel);
}

/** Models that took tokens but have no price anywhere in the range, so the spend charts leave them out. */
export function unpricedModels(usage: Usage): string[] {
  return usage.models.filter((model) => model.spend_usd === null && model.tokens > 0).map((model) => model.name);
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
      provider.spend_usd === null ? [] : [{ label: providerLabel(provider), value: provider.spend_usd }],
    )
    .sort((a, b) => b.value - a.value);
  return { slices, unpriced: unpricedProviders(usage) };
}
