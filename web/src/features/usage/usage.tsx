import { Group, Text, Title, VisuallyHidden } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { IconArrowDown, IconArrowUp, IconChevronDown, IconChevronRight, IconSelector } from "@tabler/icons-react";
import { useId, useState } from "react";
import {
  EmptyState,
  IconButton,
  PageHeader,
  Panel,
  SegmentedControl,
  StatTile,
  Table,
} from "@upmo-ai/components";

import { useApi, usageQueryKey, type Usage, type UsageRange, type UsageTask } from "../../app/api";
import { UsagePanels } from "./panels";
import {
  errorRate,
  isEmptyUsage,
  periodCacheHit,
  periodSpend,
  sortTasks,
  taskShare,
  taskTokens,
  totalCacheRead,
  totalErrors,
  totalInput,
  totalOutput,
  totalRequests,
  type SortDirection,
  type TaskSortKey,
} from "./figures";
import { formatRate, formatRatePrecise, formatTokens, formatUsd } from "./format";
import { defaultUsageRange, parseUsageRange, rangeDescription, usageRanges } from "./range";

import "./usage.css";

const rangeOptions = usageRanges.map((option) => ({ label: option.label, value: String(option.value) }));

/** Shown where a figure does not exist, such as the share of a task whose cost is unknown. */
const noValue = "\u2014";

export function UsagePage() {
  const api = useApi();
  const navigate = useNavigate();
  const search = useSearch({ strict: false });
  const range: UsageRange = parseUsageRange((search as { range?: unknown }).range) ?? defaultUsageRange;
  const usage = useQuery({
    queryKey: usageQueryKey(range),
    queryFn: () => api.usage(range),
  });

  return (
    <div className="usage-page">
      <PageHeader title="Usage" description={`${rangeDescription(range)} Figures come from the ledger.`} />
      <Group justify="space-between" align="center" wrap="wrap">
        <SegmentedControl
          aria-label="Range"
          value={String(range)}
          onChange={(value) => {
            const next = parseUsageRange(value);
            if (next !== undefined) {
              void navigate({ to: "/usage", search: { range: next }, replace: true });
            }
          }}
          data={rangeOptions}
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
      {usage.data ? <UsageFigures usage={usage.data} /> : null}
    </div>
  );
}

function spendHint(spend: number | null, unknown: number, unit: "hour" | "day"): string | undefined {
  const name: string = unit;
  if (spend === null) {
    return `Cost is unknown for every ${name} in this period`;
  }
  if (unknown > 0) {
    return `Excludes ${unknown} ${unknown === 1 ? name : `${name}s`} with unknown cost`;
  }
  return undefined;
}

function UsageFigures({ usage }: { usage: Usage }) {
  const { buckets, tasks } = usage;
  const empty = isEmptyUsage(buckets, tasks);

  return (
    <>
      {empty ? (
        <Panel p="md">
          <EmptyState
            headingLevel={2}
            title="No usage in this range"
            description="Calls to models are counted here as they happen. Try a longer range."
          />
        </Panel>
      ) : (
        <>
          <UsageOverview usage={usage} />
          <UsagePanels usage={usage} />
        </>
      )}
      <TaskCost usage={usage} />
    </>
  );
}

function UsageOverview({ usage }: { usage: Usage }) {
  const { buckets, bucket: unit, unpriced_calls: unpriced } = usage;
  const spend = periodSpend(buckets);
  const unknown = buckets.filter((bucket) => bucket.spend_usd === null).length;
  const requests = totalRequests(buckets);

  return (
    <>
      <div className="usage-figures">
        <StatTile label="Spend" value={formatUsd(spend)} hint={spendHint(spend, unknown, unit)} />
        <StatTile label="Requests" value={formatTokens(requests)} />
        <StatTile label="Error rate" value={formatRatePrecise(errorRate(requests, totalErrors(buckets)))} />
        <StatTile
          label="Input tokens"
          value={formatTokens(totalInput(buckets))}
          hint={`${formatTokens(totalCacheRead(buckets))} cached reads`}
        />
        <StatTile label="Output tokens" value={formatTokens(totalOutput(buckets))} />
        <StatTile label="Cache hit rate" value={formatRate(periodCacheHit(buckets))} />
      </div>
      {unpriced > 0 ? (
        <Text size="sm" c="dimmed">
          {unpriced === 1
            ? "1 call in range had no configured price and is excluded from spend."
            : `${formatTokens(unpriced)} calls in range had no configured price and are excluded from spend.`}
        </Text>
      ) : null}
    </>
  );
}

interface TaskSort {
  key: TaskSortKey;
  direction: SortDirection;
}

function TaskCost({ usage }: { usage: Usage }) {
  const { buckets, tasks, models } = usage;
  const [sort, setSort] = useState<TaskSort>({ key: "cost", direction: "desc" });
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const spend = periodSpend(buckets);
  const rows = sortTasks(tasks, sort.key, sort.direction, spend);
  const modelNames = new Map(models.map((model) => [model.id, model.name]));

  const sortBy = (key: TaskSortKey) =>
    setSort((current) => ({
      key,
      direction: current.key === key && current.direction === "desc" ? "asc" : "desc",
    }));
  const toggle = (taskId: string) =>
    setOpen((current) => {
      const next = new Set(current);
      if (!next.delete(taskId)) {
        next.add(taskId);
      }
      return next;
    });

  return (
    <Panel p="md">
      <Title order={2} size="h4" mb="sm">
        Tasks
      </Title>
      {rows.length === 0 ? (
        <Text c="dimmed">No task usage in this range.</Text>
      ) : (
        <Table.ScrollContainer
          minWidth={900}
          type="native"
          className="usage-scroll"
          tabIndex={0}
          role="region"
        >
          <Table aria-label="Task breakdown">
            <Table.Thead>
              <Table.Tr>
                <Table.Th w={48}>
                  <VisuallyHidden>Model split</VisuallyHidden>
                </Table.Th>
                <Table.Th>Task</Table.Th>
                <Table.Th ta="right">Tokens in</Table.Th>
                <Table.Th ta="right">Tokens out</Table.Th>
                <Table.Th ta="right">Cache read</Table.Th>
                <Table.Th ta="right">Cache write</Table.Th>
                <SortHeader label="Tokens" sortKey="tokens" sort={sort} onSort={sortBy} />
                <SortHeader label="Cost" sortKey="cost" sort={sort} onSort={sortBy} />
                <SortHeader label="Share" sortKey="share" sort={sort} onSort={sortBy} />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {rows.map((task) => (
                <TaskRows
                  key={task.task_id}
                  task={task}
                  share={taskShare(task, spend)}
                  expanded={open.has(task.task_id)}
                  onToggle={() => toggle(task.task_id)}
                  modelNames={modelNames}
                />
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </Panel>
  );
}

function SortHeader({
  label,
  sortKey,
  sort,
  onSort,
}: {
  label: string;
  sortKey: TaskSortKey;
  sort: TaskSort;
  onSort: (key: TaskSortKey) => void;
}) {
  const active = sort.key === sortKey;
  const Icon = !active ? IconSelector : sort.direction === "asc" ? IconArrowUp : IconArrowDown;
  return (
    <Table.Th
      ta="right"
      aria-sort={!active ? "none" : sort.direction === "asc" ? "ascending" : "descending"}
    >
      <button type="button" className="usage-sort" onClick={() => onSort(sortKey)}>
        {label}
        <Icon size={14} aria-hidden="true" />
      </button>
    </Table.Th>
  );
}

function TaskRows({
  task,
  share,
  expanded,
  onToggle,
  modelNames,
}: {
  task: UsageTask;
  share: number | null;
  expanded: boolean;
  onToggle: () => void;
  modelNames: ReadonlyMap<string, string>;
}) {
  const detailId = useId();
  const Chevron = expanded ? IconChevronDown : IconChevronRight;

  return (
    <>
      <Table.Tr>
        <Table.Td>
          <IconButton
            aria-label={`${expanded ? "Hide" : "Show"} the model split for ${task.goal}`}
            aria-expanded={expanded}
            aria-controls={expanded ? detailId : undefined}
            variant="subtle"
            size={32}
            onClick={onToggle}
          >
            <Chevron size={18} aria-hidden="true" />
          </IconButton>
        </Table.Td>
        <Table.Td>
          <Link to="/tasks/$taskId" params={{ taskId: task.task_id }} className="usage-task-link">
            {task.goal}
          </Link>
        </Table.Td>
        <Table.Num>{formatTokens(task.input)}</Table.Num>
        <Table.Num>{formatTokens(task.output)}</Table.Num>
        <Table.Num>{formatTokens(task.cache_read)}</Table.Num>
        <Table.Num>{formatTokens(task.cache_write)}</Table.Num>
        <Table.Num>{formatTokens(taskTokens(task))}</Table.Num>
        <Table.Num>{formatUsd(task.cost_usd)}</Table.Num>
        <Table.Num>{share === null ? noValue : formatRatePrecise(share)}</Table.Num>
      </Table.Tr>
      {expanded ? (
        <Table.Tr id={detailId}>
          <Table.Td colSpan={9} className="usage-split">
            <Table aria-label={`Cost split for ${task.goal}`} highlightOnHover={false} stickyHeader={false}>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>Model</Table.Th>
                  <Table.Th ta="right">Tokens in</Table.Th>
                  <Table.Th ta="right">Tokens out</Table.Th>
                  <Table.Th ta="right">Cache read</Table.Th>
                  <Table.Th ta="right">Cache write</Table.Th>
                  <Table.Th ta="right">Cost</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {task.by_model.map((part) => (
                  <Table.Tr key={part.model_id}>
                    <Table.Td>{modelNames.get(part.model_id) ?? part.model_id}</Table.Td>
                    <Table.Num>{formatTokens(part.input)}</Table.Num>
                    <Table.Num>{formatTokens(part.output)}</Table.Num>
                    <Table.Num>{formatTokens(part.cache_read)}</Table.Num>
                    <Table.Num>{formatTokens(part.cache_write)}</Table.Num>
                    <Table.Num>{formatUsd(part.cost_usd)}</Table.Num>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.Td>
        </Table.Tr>
      ) : null}
    </>
  );
}
