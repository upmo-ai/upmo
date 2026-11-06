import { Group, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import {
  BarChart,
  LineChart,
  PageHeader,
  Panel,
  SegmentedControl,
  StatTile,
  Table,
} from "@upmo-ai/components";

import { useApi, usageQueryKey, type Usage, type UsagePeriod } from "../../app/api";
import { cacheHitRate, periodCacheHit, periodSpend, taskTokens, totalTokens } from "./figures";
import { formatDay, formatRate, formatTokens, formatUsd } from "./format";

import "./usage.css";

const periods = [
  { label: "7 days", value: "7" },
  { label: "30 days", value: "30" },
  { label: "90 days", value: "90" },
];

/** Past this many days the bars are too narrow for their own value labels. */
const denseFrom = 14;

function isPeriod(value: string): value is `${UsagePeriod}` {
  return value === "7" || value === "30" || value === "90";
}

export function UsagePage() {
  const api = useApi();
  const [period, setPeriod] = useState<UsagePeriod>(7);
  const usage = useQuery({
    queryKey: usageQueryKey(period),
    queryFn: () => api.usage(period),
  });

  return (
    <div className="usage-page">
      <PageHeader
        title="Usage"
        description={`Last ${period} days. Figures come from the ledger.`}
      />
      <Group justify="space-between" align="center" wrap="wrap">
        <SegmentedControl
          aria-label="Period"
          value={String(period)}
          onChange={(value) => {
            if (isPeriod(value)) setPeriod(Number(value) as UsagePeriod);
          }}
          data={periods}
        />
      </Group>
      {usage.isError ? (
        <Text role="alert" c="red">
          {usage.error?.message ?? "Failed to load usage."}
        </Text>
      ) : null}
      {usage.isPending ? (
        <Text role="status" c="dimmed">
          Loading usage
        </Text>
      ) : null}
      {usage.data ? <UsageFigures days={usage.data.days} tasks={usage.data.tasks} /> : null}
    </div>
  );
}

function UsageFigures({ days, tasks }: Usage) {
  const spend = periodSpend(days);
  const unknownDays = days.filter((day) => day.spend_usd === null).length;
  const tokens = totalTokens(days);
  const hit = periodCacheHit(days);
  const dense = days.length > denseFrom;
  const first = days[0];
  const last = days[days.length - 1];
  const byTokens = [...tasks].sort((a, b) => taskTokens(b) - taskTokens(a));

  return (
    <>
      <div className="usage-figures">
        <StatTile
          label="Spend"
          value={formatUsd(spend)}
          hint={
            spend === null
              ? "Cost is unknown for every day in this period"
              : unknownDays > 0
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
            className={dense ? "usage-dense" : undefined}
            data={days.map((day) => ({
              label: formatDay(day.date),
              value: day.spend_usd ?? 0,
              display: formatUsd(day.spend_usd),
            }))}
          />
          {first && last ? (
            <Text size="xs" c="dimmed" mt="xs" aria-hidden="true">
              {formatDay(first.date)} to {formatDay(last.date)}
            </Text>
          ) : null}
        </Panel>
        <Panel p="md">
          <Text fw={600} mb="sm" aria-hidden="true">
            Tokens per task
          </Text>
          <BarChart
            title="Tokens per task"
            colorIndex={2}
            data={byTokens.map((task) => ({
              label: task.goal,
              value: taskTokens(task),
              display: formatTokens(taskTokens(task)),
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
            data={days.map((day) => {
              const rate = cacheHitRate(day.input, day.cache_read);
              return { label: formatDay(day.date), value: rate * 100, display: formatRate(rate) };
            })}
          />
        </Panel>
      </div>
      <Panel p="md">
        <Title order={2} size="h4" mb="sm">
          Tasks
        </Title>
        {byTokens.length === 0 ? (
          <Text c="dimmed">No task usage in this period.</Text>
        ) : (
          <Table.ScrollContainer minWidth={640}>
            <Table aria-label="Task breakdown">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Task</Table.Th>
                  <Table.Th ta="right">Tokens in</Table.Th>
                  <Table.Th ta="right">Tokens out</Table.Th>
                  <Table.Th ta="right">Cache read</Table.Th>
                  <Table.Th ta="right">Cache write</Table.Th>
                  <Table.Th ta="right">Cost</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {byTokens.map((task) => (
                  <Table.Tr key={task.task_id}>
                    <Table.Td>{task.goal}</Table.Td>
                    <Table.Num>{formatTokens(task.input)}</Table.Num>
                    <Table.Num>{formatTokens(task.output)}</Table.Num>
                    <Table.Num>{formatTokens(task.cache_read)}</Table.Num>
                    <Table.Num>{formatTokens(task.cache_write)}</Table.Num>
                    <Table.Num>{formatUsd(task.cost_usd)}</Table.Num>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Panel>
    </>
  );
}
