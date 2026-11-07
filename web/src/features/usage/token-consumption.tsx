import { Text } from "@mantine/core";
import { LineChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { AnalyticsPanel } from "./analytics-panel";
import { formatTokensCompact } from "./format";
import { hasCalls, tokenConsumptionSeries } from "./series";

export function TokenConsumption({ usage }: { usage: Usage }) {
  const empty = !hasCalls(usage);
  return (
    <AnalyticsPanel title="Token consumption" empty={empty ? "No tokens in this range." : undefined}>
      <LineChart title="Token consumption" series={tokenConsumptionSeries(usage)} stacked formatValue={formatTokensCompact} />
      <Text size="xs" c="dimmed" mt="xs">
        Tool definitions are an estimate and are counted out of input.
      </Text>
    </AnalyticsPanel>
  );
}
