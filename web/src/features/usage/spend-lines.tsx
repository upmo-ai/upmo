import { useState } from "react";
import { Text } from "@mantine/core";
import { LineChart, type LineSeries } from "@upmo-ai/components";

import { UsagePanel, StackToggle } from "./panel";
import { formatUsd } from "./format";

/** Spend over time with one line per row, switchable between lines and stacked areas. */
export function SpendLines({
  title,
  series,
  unpriced,
  empty,
}: {
  title: string;
  series: LineSeries[];
  /** Rows that took calls but have no price in the range, so they have no line. */
  unpriced: string[];
  /** Text for the empty state, or undefined when there is data to draw. */
  empty?: string;
}) {
  const [stacked, setStacked] = useState(false);
  return (
    <UsagePanel
      title={title}
      empty={empty}
      action={<StackToggle label={`${title} view`} stacked={stacked} onChange={setStacked} />}
      note={
        unpriced.length > 0 ? (
          <Text size="sm" c="dimmed" mt="sm">
            No price for: {unpriced.join(", ")}
          </Text>
        ) : null
      }
    >
      <LineChart title={title} series={series} stacked={stacked} formatValue={formatUsd} />
    </UsagePanel>
  );
}
