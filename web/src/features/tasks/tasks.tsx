import { Group, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";
import {
  ArtifactViewer,
  BudgetCard,
  DeliveryCard,
  EventStream,
  IconButton,
  PageHeader,
  PhaseRail,
  SegmentedControl,
  StatusBadge,
  StatusDot,
  Tabs,
  TaskHeader,
  Textarea,
  ToolCallTrace,
  type EventItem,
} from "@upmo-ai/components";

import {
  TASKS_KEY,
  approvalsQueryKey,
  tasksQueryKey,
  useApi,
  useWrite,
  type Approval,
} from "../../app/api";
import { ShareDialog } from "../identity/share-dialog";
import { ShellIcon } from "../../ui/icons";
import { EmptyState } from "../../ui/section";
import { ApprovalAnnouncer, ApprovalCard } from "./approval-card";
import { taskPhase, taskStatus } from "./status";
import {
  isTaskFilter,
  pendingResult,
  type Subtask,
  type Task,
  type TaskArtifact,
  type TaskEvent,
  type TaskFilter,
} from "./types";

import "./tasks.css";

const filters: { label: string; value: TaskFilter }[] = [
  { label: "All", value: "All" },
  { label: "Underway", value: "Working" },
  { label: "Needs approval", value: "Needs approval" },
  { label: "Done", value: "Done" },
  { label: "Failed", value: "Failed" },
];

function taskCount(count: number) {
  return count === 1 ? "1 task" : `${count} tasks`;
}

function pendingFor(approvals: Approval[] | undefined, taskId?: string) {
  return (approvals ?? []).filter(
    (item) => item.state === "pending" && (taskId === undefined || item.taskId === taskId),
  );
}

/** The spent share of the limit. pausePercent is the threshold, never this. */
function budgetPercent(task: Task) {
  if (!task.limitCents || task.limitCents <= 0) return 0;
  return Math.min(100, Math.round(((task.spentCents ?? 0) / task.limitCents) * 100));
}

export function TasksPage() {
  const api = useApi();
  const [filter, setFilter] = useState<TaskFilter>("All");
  const tasks = useQuery({
    queryKey: tasksQueryKey(),
    queryFn: () => api.listTasks(),
  });
  const approvals = useQuery({
    queryKey: approvalsQueryKey(),
    queryFn: () => api.approvals.list(),
  });
  const all = tasks.data ?? [];
  const visible = filter === "All" ? all : all.filter((task) => task.status === filter);
  const pending = pendingFor(approvals.data);

  return (
    <Stack gap="md" className="task-screen">
      <PageHeader title="Tasks" />
      {tasks.isError ? (
        <Text role="alert" c="red">
          {tasks.error?.message ?? "Failed to load tasks."}
        </Text>
      ) : null}
      {approvals.isError ? (
        <Text role="alert" c="red">
          {approvals.error?.message ?? "Failed to load approvals."}
        </Text>
      ) : null}
      <ApprovalAnnouncer pending={approvals.data ? pending : undefined} />
      {pending.map((approval) => (
        <ApprovalCard key={approval.id} approval={approval} />
      ))}
      <Group justify="space-between" align="center" wrap="wrap">
        <SegmentedControl
          data-testid="task-filter"
          className="task-filter"
          value={filter}
          onChange={(value) => {
            if (isTaskFilter(value)) setFilter(value);
          }}
          data={filters}
        />
        {tasks.data ? <Text data-testid="task-count">{taskCount(visible.length)}</Text> : null}
      </Group>
      {tasks.data && all.length === 0 ? (
        <EmptyState
          title="Your first task"
          body="Give Upmo a goal. It plans the work, does it and checks the result while you do something else."
          action={{ label: "Create task", to: "/tasks" }}
          firstRun
        />
      ) : null}
      {tasks.data && all.length > 0 ? (
        <ul className="task-list" aria-label="Tasks">
          {visible.map((task) => (
            <li key={task.id}>
              <Link to="/tasks/$taskId" params={{ taskId: task.id }} className="task-tile">
                <div className="task-tile-head">
                  <span className="task-tile-title">{task.title}</span>
                  <StatusBadge
                    className="task-tile-badge"
                    status={taskStatus(task)}
                    data-testid="task-status"
                  />
                </div>
                <div className="task-tile-meta">
                  <span className="task-tile-cost">{task.cost ?? "€0.00"}</span>
                  <span>·</span>
                  <span>{task.tokens ? `${Math.round(task.tokens / 1000)}k tokens` : "0 tokens"}</span>
                  {task.model ? (
                    <>
                      <span>·</span>
                      <span>{task.model}</span>
                    </>
                  ) : null}
                </div>
              </Link>
            </li>
          ))}
        </ul>
      ) : null}
    </Stack>
  );
}

const defaultTaskEvents: TaskEvent[] = [
  { id: "de1", time: "08:00:00", phase: "Preparing", message: "Task initialized by user" },
  { id: "de2", time: "08:00:04", phase: "Preparing", message: "Sources verified and context loaded" },
  { id: "de3", time: "08:00:16", phase: "Planning", message: "Execution plan created" },
  { id: "de4", time: "08:01:02", phase: "Working", message: "Started executing step 1" },
];

function streamEvents(events: TaskEvent[], live: boolean): EventItem[] {
  return events.map((ev, index) => ({
    id: ev.id,
    time: ev.time,
    status: ev.phase,
    message: ev.message,
    detail:
      ev.detail || ev.tools
        ? [
            ...(ev.detail ?? []),
            ...(ev.tools ?? []).map((tool) => (
              <ToolCallTrace
                key={tool.id}
                name={tool.name}
                status={tool.status}
                args={tool.args}
                result={tool.result}
                error={tool.error}
                duration={tool.duration}
              />
            )),
          ]
        : undefined,
    live: live && index === events.length - 1,
  }));
}

export function TaskPage() {
  const params = useParams({ strict: false });
  const taskId = typeof params.taskId === "string" ? params.taskId : "";
  const api = useApi();
  const [shareOpen, setShareOpen] = useState(false);
  const [paused, setPaused] = useState(false);
  const [activityView, setActivityView] = useState<"summary" | "events">("summary");
  const [following, setFollowing] = useState(true);
  const [taskMessage, setTaskMessage] = useState("");
  const [selectedArtifactId, setSelectedArtifactId] = useState<string>("");

  const tasks = useQuery({
    queryKey: tasksQueryKey(),
    queryFn: () => api.listTasks(),
  });
  const approvals = useQuery({
    queryKey: approvalsQueryKey(),
    queryFn: () => api.approvals.list(),
  });
  const next = useWrite(() => api.nextPhase(taskId), [TASKS_KEY]);

  const task = tasks.data?.find((item) => item.id === taskId);
  const pending = pendingFor(approvals.data, taskId);

  if (!tasks.data || !approvals.data) return null;
  if (!task) return <Text>This task is not in the workspace.</Text>;

  const phase = taskPhase(task);
  const status = taskStatus(task, paused);
  const advance = task.status === "Working" && pending.length === 0;
  const live = task.status === "Working" && !paused;

  const artifacts: TaskArtifact[] = task.artifacts ?? [];
  const currentArtifactId =
    selectedArtifactId && artifacts.some((a) => a.id === selectedArtifactId)
      ? selectedArtifactId
      : artifacts[0]?.id ?? "";

  const budget = budgetPercent(task);

  return (
    <Stack gap="md" className="task-screen task-detail-page">
      <ApprovalAnnouncer pending={pending} />
      {pending.map((approval) => (
        <ApprovalCard key={approval.id} approval={approval} />
      ))}

      <TaskHeader
        className="task-header"
        title={task.title}
        status={status}
        meta={{
          runNumber: task.runNumber ?? 14,
          engine: task.engine ?? "Upmo",
          model: task.model ?? "gpt-5.2",
          startedAt: task.startedAt ?? "08:00",
          cost: task.cost ?? "€2.10",
        }}
        detail={task.detail}
        paused={paused}
        onTogglePause={task.status === "Working" ? () => setPaused(!paused) : undefined}
        canAdvance={advance && !next.isPending}
        onNextPhase={() => next.mutate()}
        onShare={() => setShareOpen(true)}
      />

      <ShareDialog opened={shareOpen} task={task} onClose={() => setShareOpen(false)} />

      <div className="u-task-layout">
        <div className="task-main-col">
          <Tabs defaultValue="activity">
            <Tabs.List>
              <Tabs.Tab value="activity">Activity</Tabs.Tab>
              <Tabs.Tab value="result">
                Result
                {artifacts.length > 0 ? (
                  <span aria-hidden="true" className="task-tab-badge">
                    {artifacts.length}
                  </span>
                ) : null}
              </Tabs.Tab>
            </Tabs.List>

            <Tabs.Panel value="activity" pt="md">
              <SegmentedControl
                size="xs"
                aria-label="Activity view"
                value={activityView}
                onChange={(val) => setActivityView(val as "summary" | "events")}
                data={[
                  { label: "Summary", value: "summary" },
                  { label: "All events", value: "events" },
                ]}
                className="task-activity-seg"
              />

              {activityView === "summary" ? (
                <div className="task-summary-view">
                  <PhaseRail
                    className="task-phases"
                    current={phase === "Done" ? null : task.phase}
                    done={phase === "Done"}
                    failed={phase === "Failed"}
                  />

                  {task.subtasks && task.subtasks.length > 0 ? (
                    <Stack gap="xs" mt="md">
                      <Title order={2}>Subtasks</Title>
                      <SubtaskList nodes={task.subtasks} label="Subtasks" />
                    </Stack>
                  ) : null}

                  <div className="task-composer">
                    <Textarea
                      minRows={2}
                      placeholder="Message this task…"
                      aria-label="Message this task"
                      value={taskMessage}
                      onChange={(e) => setTaskMessage(e.currentTarget.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" && !e.shiftKey) {
                          e.preventDefault();
                          if (taskMessage.trim()) setTaskMessage("");
                        }
                      }}
                    />
                    <div className="task-composer-foot">
                      <Text size="xs" c="dimmed">
                        Upmo reads it at the next step and replies here.
                      </Text>
                      <IconButton
                        size="sm"
                        variant="filled"
                        aria-label="Send message"
                        disabled={!taskMessage.trim()}
                        onClick={() => {
                          if (taskMessage.trim()) setTaskMessage("");
                        }}
                      >
                        <ShellIcon concept="action.send" size={16} />
                      </IconButton>
                    </div>
                  </div>
                </div>
              ) : (
                <EventStream
                  className="task-events"
                  events={streamEvents(task.events ?? defaultTaskEvents, live)}
                  following={following}
                  onFollowChange={setFollowing}
                />
              )}
            </Tabs.Panel>

            <Tabs.Panel value="result" pt="md">
              <Stack gap="lg">
                <section>
                  <Title order={2} className="task-section-heading">
                    Summary
                  </Title>
                  <div className="u-card task-result-card">
                    <Text data-testid="task-result">
                      {task.status === "Done" ? task.result : pendingResult}
                    </Text>
                  </div>
                </section>

                {artifacts.length > 0 ? (
                  <section>
                    <Title order={2} className="task-section-heading">
                      Artifacts
                    </Title>
                    <ArtifactViewer
                      artifacts={artifacts}
                      selectedId={currentArtifactId}
                      onSelect={setSelectedArtifactId}
                    />
                  </section>
                ) : null}
              </Stack>
            </Tabs.Panel>
          </Tabs>
        </div>

        <aside className="u-task-side">
          <BudgetCard
            percent={budget}
            cost={task.cost ?? "€2.10"}
            limit={task.spendLimit ?? "€5.00"}
            tokens={task.tokens ?? 38000}
            pauseAt={task.pausePercent ?? 90}
            // A full page load is expected until the design system offers a router-aware link.
            usageHref="/usage"
          />

          <DeliveryCard
            channel={task.deliveryChannel ?? "#finance on Slack"}
            when={task.deliveryWhen ?? "After the task is done"}
          />

          <div className="u-card task-side-card">
            <h4 className="task-side-card-title">Sources</h4>
            <div className="task-sources-chips">
              {(task.sources ?? ["Finance mailbox", "finance-policies", "vendor-contracts"]).map(
                (src) => (
                  <span key={src} className="u-chip task-source-chip">
                    {src}
                  </span>
                ),
              )}
            </div>
          </div>

          {task.runHistory && task.runHistory.length > 0 ? (
            <div className="u-card task-side-card">
              <h4 className="task-side-card-title">Run history</h4>
              <ul className="task-run-history-list" aria-label="Run history">
                {task.runHistory.map((item) => (
                  <li key={item.run} className="task-run-history-item">
                    <span>Run {item.run}</span>
                    <span className="task-run-history-status">
                      <StatusDot status={item.status} label={`Run ${item.run}: ${item.status}`} />
                      <span aria-hidden="true">{item.status}</span>
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </aside>
      </div>
    </Stack>
  );
}

function SubtaskList({ nodes, label }: { nodes: Subtask[]; label?: string }) {
  return (
    <ul className="subtask-tree" aria-label={label}>
      {nodes.map((node) => (
        <li key={node.id}>
          <span className="subtask-row">
            <span>{node.title}</span>
            <StatusBadge status={node.status} data-testid="subtask-status" />
          </span>
          {node.children && node.children.length > 0 ? <SubtaskList nodes={node.children} /> : null}
        </li>
      ))}
    </ul>
  );
}
