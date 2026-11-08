import { LineChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { UsagePanel } from "./panel";
import { formatPercentPoints } from "./format";
import { cacheHitSeries, hasCalls } from "./series";

export function CacheHitChart({ usage }: { usage: Usage }) {
  const empty = !hasCalls(usage);
  return (
    <UsagePanel title="Cache hit rate" empty={empty ? "No cache reads in this range." : undefined}>
      <LineChart title="Cache hit rate" series={cacheHitSeries(usage)} area formatValue={formatPercentPoints} />
    </UsagePanel>
  );
}
