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
  Tabs,
} from "@upmo-ai/components";

import {
  useApi,
  usageLimitsQueryKey,
  usageQueryKey,
  type Usage,
  type UsageRange,
  type UsageTask,
} from "../../app/api";
import { UsageLimitBars } from "./limits";
import { UsagePanels } from "./panels";
import { WorkspaceUsageTab } from "./workspace";
import {
  errorRate,
  isEmptyUsage,
  periodCacheHit,
  sortTasks,
  sourceLabel,
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
import { formatMicroUsd, formatRate, formatRatePrecise, formatSpend, formatTokens } from "./format";
import { defaultUsageRange, parseUsageRange, rangeDescription, usageRanges } from "./range";

import "./usage.css";

const rangeOptions = usageRanges.map((option) => ({ label: option.label, value: String(option.value) }));

/** Shown where a figure does not exist, such as the share of a task whose cost is unknown. */
const noValue = "\u2014";

type UsageView = "mine" | "workspace";

export function UsagePage() {
  const api = useApi();
  const navigate = useNavigate();
  const search = useSearch({ strict: false });
  const range: UsageRange = parseUsageRange((search as { range?: unknown }).range) ?? defaultUsageRange;
  const [view, setView] = useState<UsageView>("mine");
  const limits = useQuery({
    queryKey: usageLimitsQueryKey(),
    queryFn: () => api.usageLimits(),
  });
  // The gateway sends the limit rows only to a person who holds usage:view, so
  // their presence is what offers the Workspace tab. The route refuses anyone
  // else on its own, whatever the page shows.
  const canViewWorkspace = limits.data?.rows !== undefined;

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
      {limits.isError ? (
        <Text role="alert" c="red">
          {limits.error?.message ?? "Failed to load usage limits."}
        </Text>
      ) : null}
      {limits.data ? <UsageLimitBars limits={limits.data} /> : null}
      {canViewWorkspace ? (
        <Tabs
          value={view}
          onChange={(value) => setView(value === "workspace" ? "workspace" : "mine")}
          keepMounted={false}
        >
          <Tabs.List aria-label="Whose usage">
            <Tabs.Tab value="mine">Your usage</Tabs.Tab>
            <Tabs.Tab value="workspace">Workspace</Tabs.Tab>
          </Tabs.List>
          <Tabs.Panel value="mine" pt="md">
            <OwnUsage range={range} />
          </Tabs.Panel>
          <Tabs.Panel value="workspace">
            <WorkspaceUsageTab range={range} />
          </Tabs.Panel>
        </Tabs>
      ) : (
        <OwnUsage range={range} />
      )}
    </div>
  );
}

/** The signed-in person's own usage for a range. */
function OwnUsage({ range }: { range: UsageRange }) {
  const api = useApi();
  const usage = useQuery({
    queryKey: usageQueryKey(range),
    queryFn: () => api.usage(range),
  });
  return (
    <div className="usage-page-body">
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
          <KindOfWork usage={usage} />
        </>
      )}
      <TaskCost usage={usage} />
    </>
  );
}

function UsageOverview({ usage }: { usage: Usage }) {
  const { buckets, bucket: unit, unpriced_calls: unpriced, spend_micro_usd: spend, list_price_micro_usd: listPrice } = usage;
  const unknown = buckets.filter((bucket) => bucket.spend_micro_usd === null).length;
  const requests = totalRequests(buckets);
  const spendNote = [spendHint(spend, unknown, unit), usage.estimated ? "Includes estimated costs" : undefined]
    .filter((note) => note !== undefined)
    .join(". ");

  return (
    <>
      <div className="usage-figures">
        <StatTile
          label="Spend"
          value={formatMicroUsd(spend)}
          hint={spendNote === "" ? undefined : spendNote}
        />
        {listPrice !== null ? (
          <StatTile
            label="Not billed"
            value={formatMicroUsd(listPrice)}
            hint="Subscription and local calls at list price"
          />
        ) : null}
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

/** Where the calls came from: chat, tasks, automations and the rest. */
function KindOfWork({ usage }: { usage: Usage }) {
  const { sources } = usage;
  return (
    <Panel p="md">
      <Title order={2} size="h4" mb="sm">
        Kind of work
      </Title>
      {sources.length === 0 ? (
        <Text c="dimmed">No usage by kind of work in this range.</Text>
      ) : (
        <Table.ScrollContainer minWidth={480} type="native" className="usage-scroll" tabIndex={0} role="region">
          <Table aria-label="Kind of work">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Kind</Table.Th>
                <Table.Th ta="right">Requests</Table.Th>
                <Table.Th ta="right">Tokens</Table.Th>
                <Table.Th ta="right">Spend</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {sources.map((source) => (
                <Table.Tr key={source.source}>
                  <Table.Td>{sourceLabel(source.source)}</Table.Td>
                  <Table.Num>{formatTokens(source.requests)}</Table.Num>
                  <Table.Num>{formatTokens(source.tokens)}</Table.Num>
                  <Table.Num>{formatSpend(source.spend_micro_usd, usage.estimated)}</Table.Num>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </Panel>
  );
}

interface TaskSort {
  key: TaskSortKey;
  direction: SortDirection;
}

function TaskCost({ usage }: { usage: Usage }) {
  const { tasks, models } = usage;
  const [sort, setSort] = useState<TaskSort>({ key: "cost", direction: "desc" });
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const rows = sortTasks(tasks, sort.key, sort.direction, usage.spend_micro_usd);
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
                  share={taskShare(task, usage.spend_micro_usd)}
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
        <Table.Num>{formatMicroUsd(task.cost_micro_usd)}</Table.Num>
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
                    <Table.Num>{formatMicroUsd(part.cost_micro_usd)}</Table.Num>
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
