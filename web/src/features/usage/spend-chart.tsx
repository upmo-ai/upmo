import { Text } from "@mantine/core";
import { GroupedBarChart } from "@upmo-ai/components";

import type { Usage } from "../../app/api";
import { UsagePanel } from "./panel";
import { formatBucket, formatUsd } from "./format";
import { hasCalls, spendGroups } from "./series";

export function SpendChart({ usage }: { usage: Usage }) {
  const { buckets, bucket: unit } = usage;
  const first = buckets[0];
  const last = buckets[buckets.length - 1];
  const empty = !hasCalls(usage) || buckets.length === 0;
  return (
    <UsagePanel
      title="Spend"
      empty={empty ? "No spend to chart in this range." : undefined}
      note={
        first && last ? (
          <Text size="xs" c="dimmed" mt="xs" aria-hidden="true">
            {formatBucket(first.at, unit)} to {formatBucket(last.at, unit)}
          </Text>
        ) : null
      }
    >
      <GroupedBarChart title="Spend" groups={spendGroups(usage)} formatValue={formatUsd} />
    </UsagePanel>
  );
}
