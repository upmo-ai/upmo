import { Text } from "@mantine/core";
import { BarChart, LineChart, PageHeader, Panel, StatTile } from "@upmo-ai/components";

import { formatRate, formatTokens, formatUsd } from "./format";
import {
  cacheHitRate,
  knownSpend,
  periodCacheHit,
  sampleDays,
  sampleTasks,
  totalTokens,
} from "./sample";

import "./usage.css";

export function UsagePage() {
  const spend = knownSpend(sampleDays);
  const unknownDays = sampleDays.filter((day) => day.spendUsd === null).length;
  const tokens = totalTokens(sampleDays);
  const hit = periodCacheHit(sampleDays);

  return (
    <div className="usage-page">
      <PageHeader
        title="Usage"
        description="Last 7 days. Sample figures until the ledger exists."
      />
      <div className="usage-figures">
        <StatTile
          label="Spend"
          value={formatUsd(spend)}
          hint={
            unknownDays > 0
              ? `Excludes ${unknownDays} ${unknownDays === 1 ? "day" : "days"} with unknown cost`
              : undefined
          }
        />
        <StatTile label="Tokens" value={formatTokens(tokens)} />
        <StatTile label="Cache hit rate" value={formatRate(hit)} />
      </div>
      <div className="usage-charts">
        <Panel p="md">
          <Text fw={600} mb="sm" aria-hidden="true">
            Spend
          </Text>
          <BarChart
            title="Spend"
            data={sampleDays.map((day) => ({
              label: day.label,
              value: day.spendUsd ?? 0,
              display: formatUsd(day.spendUsd),
            }))}
          />
        </Panel>
        <Panel p="md">
          <Text fw={600} mb="sm" aria-hidden="true">
            Tokens per task
          </Text>
          <BarChart
            title="Tokens per task"
            colorIndex={2}
            data={sampleTasks.map((task) => ({
              label: task.title,
              value: task.tokens,
              display: formatTokens(task.tokens),
            }))}
          />
        </Panel>
        <Panel p="md">
          <Text fw={600} mb="sm" aria-hidden="true">
            Cache hit rate
          </Text>
          <LineChart
            title="Cache hit rate"
            colorIndex={3}
            data={sampleDays.map((day) => {
              const rate = cacheHitRate(day.input, day.cacheRead);
              return { label: day.label, value: rate * 100, display: formatRate(rate) };
            })}
          />
        </Panel>
      </div>
    </div>
  );
}
