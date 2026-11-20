import { Group, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button, EmptyState, Panel, Select, StatTile, Table } from "@upmo-ai/components";

import {
  useApi,
  workspaceUsageQueryKey,
  type UsageGroup,
  type UsageRange,
  type WorkspaceUsage,
} from "../../app/api";
import { formatMicroUsd, formatSpend, formatTokens } from "./format";

const groups: { value: UsageGroup; label: string }[] = [
  { value: "person", label: "Person" },
  { value: "team", label: "Team" },
  { value: "agent", label: "Agent" },
  { value: "model", label: "Model" },
  { value: "provider", label: "Provider" },
  { value: "source", label: "Kind of work" },
];

/** Shown where a figure does not exist, such as the list price of a row with no subscription or local calls. */
const noValue = "\u2014";

function isGroup(value: string | null): value is UsageGroup {
  return groups.some((group) => group.value === value);
}

/**
 * Where the server's CSV of the same read is. The browser downloads it as it is:
 * the page does not build a CSV of its own.
 */
export function workspaceCsvPath(range: UsageRange, group: UsageGroup): string {
  return `/api/usage/workspace?range=${encodeURIComponent(String(range))}&group=${encodeURIComponent(group)}&format=csv`;
}

/** The workspace's usage, grouped by person, team, agent, model, provider or kind of work. */
export function WorkspaceUsageTab({ range }: { range: UsageRange }) {
  const api = useApi();
  const [group, setGroup] = useState<UsageGroup>("person");
  const usage = useQuery({
    queryKey: workspaceUsageQueryKey(range, group),
    queryFn: () => api.workspaceUsage(range, group),
  });

  return (
    <div className="usage-workspace">
      <Group justify="space-between" align="flex-end" wrap="wrap">
        <Select
          label="Group by"
          data={groups}
          value={group}
          allowDeselect={false}
          onChange={(value) => {
            if (isGroup(value)) {
              setGroup(value);
            }
          }}
        />
        <Button component="a" variant="default" href={workspaceCsvPath(range, group)} download>
          Export CSV
        </Button>
      </Group>
      {usage.isError ? (
        <Text role="alert" c="red">
          {usage.error?.message ?? "Failed to load workspace usage."}
        </Text>
      ) : null}
      {usage.isPending ? (
        <Text role="status" c="dimmed">
          Loading workspace usage
        </Text>
      ) : null}
      {usage.data ? <WorkspaceFigures usage={usage.data} /> : null}
    </div>
  );
}

function WorkspaceFigures({ usage }: { usage: WorkspaceUsage }) {
  const label = groups.find((group) => group.value === usage.group)?.label ?? usage.group;
  if (usage.rows.length === 0) {
    return (
      <Panel p="md">
        <EmptyState
          headingLevel={2}
          title="No workspace usage in this range"
          description="Calls to models are counted here as they happen. Try a longer range."
        />
      </Panel>
    );
  }
  return (
    <>
      <div className="usage-figures">
        <StatTile label="Workspace spend" value={formatSpend(usage.spend_micro_usd, usage.estimated)} />
        {usage.list_price_micro_usd !== null ? (
          <StatTile
            label="Not billed"
            value={formatMicroUsd(usage.list_price_micro_usd)}
            hint="Subscription and local calls at list price"
          />
        ) : null}
      </div>
      <Panel p="md">
        <Title order={2} size="h4" mb="sm">
          {`Usage by ${label.toLowerCase()}`}
        </Title>
        <Table.ScrollContainer minWidth={640} type="native" className="usage-scroll" tabIndex={0} role="region">
          <Table aria-label="Workspace usage">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{label}</Table.Th>
                <Table.Th ta="right">Requests</Table.Th>
                <Table.Th ta="right">Tokens</Table.Th>
                <Table.Th ta="right">Spend</Table.Th>
                <Table.Th ta="right">Not billed (list price)</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {usage.rows.map((row) => (
                <Table.Tr key={row.id}>
                  <Table.Td>{row.name}</Table.Td>
                  <Table.Num>{formatTokens(row.requests)}</Table.Num>
                  <Table.Num>{formatTokens(row.tokens)}</Table.Num>
                  <Table.Num>{formatSpend(row.spend_micro_usd, row.estimated)}</Table.Num>
                  <Table.Num>{row.list_price_micro_usd === null ? noValue : formatMicroUsd(row.list_price_micro_usd)}</Table.Num>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Panel>
    </>
  );
}
