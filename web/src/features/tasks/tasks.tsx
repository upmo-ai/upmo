import { Group, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import {
  ArtifactViewer,
  BudgetCard,
  Button,
  DeliveryCard,
  EmptyState,
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
  BrainError,
  TASKS_KEY,
  approvalsQueryKey,
  taskQueryKey,
  tasksQueryKey,
  useApi,
  useWrite,
  type Approval,
} from "../../app/api";
import { ArtifactsTab } from "../artifacts/artifacts";
import { ShareDialog } from "../identity/share-dialog";
import { CaretIcon, ShellIcon } from "../../ui/icons";
import { ApprovalAnnouncer, ApprovalCard } from "./approval-card";
import { NewTaskDialog } from "./new-task-dialog";
import { preparingPollMs } from "./preparing";
import { PreparingBlock } from "./preparing-block";
import { messageMax, messageProblem } from "./rules";
import { taskPhase, taskStatus } from "./status";
import {
  isTaskFilter,
  messageEventType,
  noResult,
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
  const [creating, setCreating] = useState(false);
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
      <PageHeader
        title="Tasks"
        actions={
          all.length > 0 ? (
            <Button type="button" onClick={() => setCreating(true)}>
              New task
            </Button>
          ) : undefined
        }
      />
      <NewTaskDialog opened={creating} onClose={() => setCreating(false)} />
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
          description="Give Upmo a goal. It plans the work, does it and checks the result while you do something else."
          icon={<CaretIcon size={24} />}
          action={
            <Button type="button" onClick={() => setCreating(true)}>
              New task
            </Button>
          }
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
                <TileMeta task={task} />
              </Link>
            </li>
          ))}
        </ul>
      ) : null}
    </Stack>
  );
}

/** The figures a task has reported. A figure it has not reported is left out. */
function TileMeta({ task }: { task: Task }) {
  const facts: ReactNode[] = [];
  if (task.cost) {
    facts.push(
      <span key="cost" className="task-tile-cost">
        {task.cost}
      </span>,
    );
  }
  if (task.tokens) facts.push(<span key="tokens">{Math.round(task.tokens / 1000)}k tokens</span>);
  if (task.model) facts.push(<span key="model">{task.model}</span>);
  if (facts.length === 0) return null;
  return (
    <div className="task-tile-meta">
      {facts.flatMap((fact, index) =>
        index === 0 ? [fact] : [<span key={`dot-${index}`}>·</span>, fact],
      )}
    </div>
  );
}

function isMessage(event: TaskEvent) {
  return event.type === messageEventType;
}

function streamEvents(events: TaskEvent[], live: boolean): EventItem[] {
  // Messages from people are not progress, so the live marker skips them.
  let liveIndex = -1;
  events.forEach((ev, index) => {
    if (!isMessage(ev)) liveIndex = index;
  });
  return events.map((ev, index) => ({
    id: ev.id,
    time: ev.time,
    status: ev.phase,
    message: isMessage(ev) ? `You: ${ev.message}` : ev.message,
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
    live: live && index === liveIndex,
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
  const [tab, setTab] = useState<string | null>("activity");

  const detail = useQuery({
    queryKey: taskQueryKey(taskId),
    queryFn: () => api.getTask(taskId),
    refetchInterval: (query) => preparingPollMs(query.state.data ? [query.state.data] : undefined, taskId),
  });
  const approvals = useQuery({
    queryKey: approvalsQueryKey(),
    queryFn: () => api.approvals.list(),
  });
  // TASKS_KEY is the front of the task's own key, so a write refreshes the list and this task.
  const next = useWrite(() => api.nextPhase(taskId), [TASKS_KEY]);
  // The draft is cleared when the message is sent, so anything typed while the post is in
  // flight is a new draft. A failed post puts the unsent text back in front of it.
  const post = useWrite((body: string) => api.postTaskMessage(taskId, body), [TASKS_KEY], {
    shown: true,
    onError: (_error, body) => setTaskMessage((draft) => (draft === "" ? body : `${body}\n${draft}`)),
  });

  const task = detail.data;
  const pending = pendingFor(approvals.data, taskId);

  if (!task && detail.isError) {
    return detail.error instanceof BrainError && detail.error.status === 404 ? (
      <Text>This task is not in the workspace.</Text>
    ) : (
      <Text role="alert" c="red">
        {detail.error.message || "Failed to load the task."}
      </Text>
    );
  }
  if (!task || !approvals.data) return null;

  const phase = taskPhase(task);
  const status = taskStatus(task, paused);
  const working = task.status === "Working" && pending.length === 0;
  // Checking is the last step. Upmo finishes the task when its checks pass.
  const checking = working && task.phase === "Checking";
  const advance = working && !checking;
  const live = task.status === "Working" && !paused;
  const events = task.events ?? [];
  const messages = events.filter(isMessage);
  const messageTooLong = taskMessage.length > messageMax;
  const send = () => {
    if (post.isPending || messageProblem(taskMessage) !== null) return;
    post.mutate(taskMessage.trim());
    setTaskMessage("");
  };

  const artifacts: TaskArtifact[] = task.artifacts ?? [];
  const currentArtifactId =
    selectedArtifactId && artifacts.some((a) => a.id === selectedArtifactId)
      ? selectedArtifactId
      : artifacts[0]?.id ?? "";

  const budget =
    task.cost !== undefined &&
    task.spendLimit !== undefined &&
    task.limitCents !== undefined &&
    task.pausePercent !== undefined
      ? {
          percent: budgetPercent(task),
          cost: task.cost,
          limit: task.spendLimit,
          pauseAt: task.pausePercent,
        }
      : null;

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
          runNumber: task.runNumber,
          engine: task.engine,
          model: task.model,
          startedAt: task.startedAt,
          cost: task.cost,
        }}
        detail={task.detail}
        paused={paused}
        onTogglePause={task.status === "Working" ? () => setPaused(!paused) : undefined}
        canAdvance={advance && !next.isPending}
        onNextPhase={() => next.mutate()}
        onShare={() => setShareOpen(true)}
      />

      {checking ? (
        <Text size="sm" c="dimmed" data-testid="next-phase-note">
          Next phase is off because Checking is the last step. Upmo marks the task Done when its checks
          pass, so there is nothing for you to advance.
        </Text>
      ) : null}

      <ShareDialog opened={shareOpen} task={task} onClose={() => setShareOpen(false)} />

      <div className="u-task-layout">
        <div className="task-main-col">
          <PreparingBlock task={task} />
          <Tabs defaultValue="activity" onChange={setTab}>
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
              <Tabs.Tab value="artifacts">Artifacts</Tabs.Tab>
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

                  {messages.length > 0 ? (
                    <Stack gap="xs" mt="md">
                      <Title order={2}>Messages</Title>
                      <ul className="task-messages" aria-label="Messages">
                        {messages.map((message) => (
                          <li key={message.id} className="task-message">
                            <time className="task-message-time">{message.time}</time>
                            <span className="task-message-body">{message.message}</span>
                          </li>
                        ))}
                      </ul>
                    </Stack>
                  ) : null}

                  <div className="task-composer">
                    <Textarea
                      minRows={2}
                      placeholder="Message this task…"
                      aria-label="Message this task"
                      aria-invalid={messageTooLong}
                      value={taskMessage}
                      onChange={(e) => {
                        setTaskMessage(e.currentTarget.value);
                        if (post.isError) post.reset();
                      }}
                      onKeyDown={(e) => {
                        // Enter confirms an IME candidate; it must not send the message.
                        // Enter confirms an IME candidate; it must not send the message.
                        if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                          e.preventDefault();
                          send();
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
                        disabled={messageProblem(taskMessage) !== null || post.isPending}
                        onClick={send}
                      >
                        <ShellIcon concept="action.send" size={16} />
                      </IconButton>
                    </div>
                    {messageTooLong ? (
                      <Text size="xs" c="red" role="alert">
                        {messageProblem(taskMessage)}
                      </Text>
                    ) : null}
                    {post.isError ? (
                      <Text size="xs" c="red" role="alert">
                        {post.error.message}
                      </Text>
                    ) : null}
                  </div>
                </div>
              ) : (
                events.length > 0 ? (
                  <EventStream
                    className="task-events"
                    events={streamEvents(events, live)}
                    following={following}
                    onFollowChange={setFollowing}
                  />
                ) : (
                  <Text className="task-events" c="dimmed">
                    No events yet.
                  </Text>
                )
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
                      {task.status !== "Done" ? pendingResult : task.result || noResult}
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

            <Tabs.Panel value="artifacts" pt="md">
              <ArtifactsTab key={taskId} taskId={taskId} active={tab === "artifacts"} />
            </Tabs.Panel>
          </Tabs>
        </div>

        <aside className="u-task-side">
          {budget ? (
            <BudgetCard
              percent={budget.percent}
              cost={budget.cost}
              limit={budget.limit}
              tokens={task.tokens}
              pauseAt={budget.pauseAt}
              // A full page load is expected until the design system offers a router-aware link.
              usageHref="/usage"
            />
          ) : (
            <div className="u-card task-side-card">
              <h4 className="task-side-card-title">Budget</h4>
              <Text size="sm">Spent: {task.cost ?? "Unknown"}</Text>
              <Text size="sm">Limit: {task.spendLimit ?? "Unknown"}</Text>
            </div>
          )}

          {task.deliveryChannel ? (
            <DeliveryCard channel={task.deliveryChannel} when={task.deliveryWhen ?? "Unknown"} />
          ) : null}

          {task.sources && task.sources.length > 0 ? (
            <div className="u-card task-side-card">
              <h4 className="task-side-card-title">Sources</h4>
              <div className="task-sources-chips">
                {task.sources.map((src) => (
                  <span key={src} className="u-chip task-source-chip">
                    {src}
                  </span>
                ))}
              </div>
            </div>
          ) : null}

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
