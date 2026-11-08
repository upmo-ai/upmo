import { LineChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { UsagePanel } from "./panel";
import { formatTokensCompact } from "./format";
import { hasCalls, modelTokenSeries } from "./series";

export function ModelTokens({ usage }: { usage: Usage }) {
  const series = modelTokenSeries(usage);
  const empty = !hasCalls(usage) || series.length === 0;
  return (
    <UsagePanel title="Tokens per model" empty={empty ? "No tokens in this range." : undefined}>
      <LineChart title="Tokens per model" series={series} formatValue={formatTokensCompact} />
    </UsagePanel>
  );
}
