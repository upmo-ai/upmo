import { Text } from "@mantine/core";
import { DonutChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { UsagePanel } from "./panel";
import { formatUsd } from "./format";
import { hasCalls, spendShare } from "./series";

export function ProviderShare({ usage }: { usage: Usage }) {
  const { slices, unpriced } = spendShare(usage);
  const total = slices.reduce((sum, slice) => sum + slice.value, 0);
  const empty = !hasCalls(usage);
  return (
    <UsagePanel title="Spend share by provider" empty={empty ? "No spend in this range." : undefined}>
      {slices.length > 0 ? (
        <DonutChart
          title="Spend share by provider"
          centerLabel="Spend"
          centerValue={formatUsd(total)}
          data={slices}
          formatValue={formatUsd}
        />
      ) : (
        <Text c="dimmed">No priced spend in this range.</Text>
      )}
      {unpriced.length > 0 ? (
        <Text size="sm" c="dimmed" mt="sm">
          No price for: {unpriced.join(", ")}
        </Text>
      ) : null}
    </UsagePanel>
  );
}
