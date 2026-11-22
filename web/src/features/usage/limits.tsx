import { Text, Title } from "@mantine/core";
import { UsageMeter, Panel } from "@upmo-ai/components";

import type { UsageLimits } from "../../app/api";
import { limitUse, limitWarnPercent } from "./figures";
import { formatDay, formatMicroUsd } from "./format";

/** One limit as a bar, with the words that carry the 80% and the over-the-limit states so color is not the only signal. */
function LimitBar({ label, limit, spent, held }: { label: string; limit: number; spent: number; held: number }) {
  const { percent, state } = limitUse(spent, limit);
  return (
    <div className="usage-limit">
      <UsageMeter
        label={label}
        percent={percent}
        used={formatMicroUsd(spent)}
        limit={formatMicroUsd(limit)}
        warnAt={limitWarnPercent}
      />
      {state === "over" ? (
        <Text size="sm" c="red" role="status">
          Limit reached
        </Text>
      ) : state === "near" ? (
        <Text size="sm" c="orange" role="status">
          {`${Math.floor((spent * 100) / limit)}% of the limit is used`}
        </Text>
      ) : null}
      {held > 0 ? (
        <Text size="sm" c="dimmed">
          {`${formatMicroUsd(held)} is held for calls in progress`}
        </Text>
      ) : null}
    </div>
  );
}

/**
 * Spend so far this month against the limits that apply to the person. A limit
 * that is not set has no bar. A limit that is set but unused shows a bar at
 * $0.00, which is a real zero.
 */
export function UsageLimitBars({ limits }: { limits: UsageLimits }) {
  const { own, period_start: start } = limits;
  const bars = [
    { label: "Your limit", limit: own.limit_micro_usd },
    { label: "Workspace limit", limit: own.workspace_limit_micro_usd },
  ].flatMap((bar) => (bar.limit === null ? [] : [{ label: bar.label, limit: bar.limit }]));

  return (
    <Panel p="md" aria-label="Monthly limits">
      <Title order={2} size="h4" mb="xs">
        Monthly limits
      </Title>
      <Text size="sm" c="dimmed" mb="sm">
        {`Month starting ${formatDay(start)}.`}
      </Text>
      {bars.length === 0 ? (
        <Text c="dimmed">No monthly spend limit is set.</Text>
      ) : (
        <div className="usage-limits">
          {bars.map((bar) => (
            <LimitBar key={bar.label} label={bar.label} limit={bar.limit} spent={own.spent_micro_usd} held={own.held_micro_usd} />
          ))}
        </div>
      )}
    </Panel>
  );
}
