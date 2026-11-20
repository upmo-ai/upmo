import { LineChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { UsagePanel } from "./panel";
import { formatTokensCompact } from "./format";
import { hasCalls, tokenConsumptionSeries } from "./series";

export function TokenConsumption({ usage }: { usage: Usage }) {
  const empty = !hasCalls(usage);
  return (
    <UsagePanel title="Token consumption" empty={empty ? "No tokens in this range." : undefined}>
      <LineChart title="Token consumption" series={tokenConsumptionSeries(usage)} stacked formatValue={formatTokensCompact} />
    </UsagePanel>
  );
}
