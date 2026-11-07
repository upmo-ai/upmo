import { LineChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { AnalyticsPanel } from "./analytics-panel";
import { formatPercentPoints } from "./format";
import { cacheHitSeries, hasCalls } from "./series";

export function CacheHitChart({ usage }: { usage: Usage }) {
  const empty = !hasCalls(usage);
  return (
    <AnalyticsPanel title="Cache hit rate" empty={empty ? "No cache reads in this range." : undefined}>
      <LineChart title="Cache hit rate" series={cacheHitSeries(usage)} area formatValue={formatPercentPoints} />
    </AnalyticsPanel>
  );
}
