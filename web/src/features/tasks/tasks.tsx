import {
  ActionIcon,
  Badge,
  Button,
  Group,
  Progress,
  SegmentedControl,
  Stack,
  Table,
  Tabs,
  Text,
  Textarea,
  Title,
  Tooltip,
} from "@mantine/core";
import { IconDownload, IconPencil } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { useState } from "react";

import {
  TASKS_KEY,
  approvalsQueryKey,
  tasksQueryKey,
  useApi,
  useWrite,
  type Approval,
  type Task,
  type TaskStatus,
} from "../../app/api";
import { ShareDialog } from "../identity/share-dialog";
import { CaretIcon, ShellIcon } from "../../ui/icons";
import { EmptyState } from "../../ui/section";
import { ApprovalCard } from "./approval-card";
import {
  isTaskFilter,
  pendingResult,
  phases,
  type Phase,
  type Subtask,
  type TaskArtifact,
  type TaskEvent,
  type TaskFilter,
} from "./types";

import "./tasks.css";

const filters: { label: string; value: TaskFilter }[] = [
  { label: "All", value: "All" },
  { label: "Working", value: "Working" },
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

export function TaskStatusBadge({
  status,
  marker = "task-status",
}: {
  status: TaskStatus;
  marker?: string;
}) {
  return (
    <Badge
      className="task-badge"
      classNames={{ label: "task-badge-label" }}
      data-testid={marker}
      variant="light"
      styles={{
        root: {
          height: "auto",
          overflow: "visible",
          maxWidth: "100%",
          textTransform: "none",
        },
        label: {
          height: "auto",
          overflow: "visible",
          whiteSpace: "normal",
          textOverflow: "clip",
          textTransform: "none",
        },
      }}
    >
      {status}
    </Badge>
  );
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
      <Title order={1}>Tasks</Title>
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
                  <TaskStatusBadge status={task.status} />
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

function markedPhase(task: Task): Phase | null {
  if (task.status === "Done") return null;
  return task.phase;
}

const defaultTaskEvents: TaskEvent[] = [
  { id: "de1", time: "08:00:00", phase: "Preparing", message: "Task initialized by user" },
  { id: "de2", time: "08:00:04", phase: "Preparing", message: "Sources verified and context loaded" },
  { id: "de3", time: "08:00:16", phase: "Planning", message: "Execution plan created" },
  { id: "de4", time: "08:01:02", phase: "Working", message: "Started executing step 1" },
];

export function TaskPage() {
  const params = useParams({ strict: false });
  const taskId = typeof params.taskId === "string" ? params.taskId : "";
  const api = useApi();
  const [shareOpen, setShareOpen] = useState(false);
  const [paused, setPaused] = useState(false);
  const [activityView, setActivityView] = useState<"summary" | "events">("summary");
  const [taskMessage, setTaskMessage] = useState("");
  const [selectedArtifactId, setSelectedArtifactId] = useState<string>("");
  const [markdownView, setMarkdownView] = useState<"rendered" | "source">("rendered");

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

  const current = markedPhase(task);
  const advance = task.status === "Working" && pending.length === 0;

  const artifacts: TaskArtifact[] = task.artifacts ?? [];
  const currentArtifactId =
    selectedArtifactId && artifacts.some((a) => a.id === selectedArtifactId)
      ? selectedArtifactId
      : artifacts[0]?.id ?? "";
  const activeArtifact = artifacts.find((a) => a.id === currentArtifactId);

  const budgetPercent = task.pausePercent ?? 42;

  return (
    <Stack gap="md" className="task-screen task-detail-page">
      {pending.map((approval) => (
        <ApprovalCard key={approval.id} approval={approval} />
      ))}

      <header className="task-page-head">
        <div className="task-head-main">
          <div className="task-head-title-row">
            <Title order={1}>{task.title}</Title>
            <TaskStatusBadge status={task.status} />
          </div>
          <div className="task-meta-desc">
            <span>Run {task.runNumber ?? 14}</span>
            <span>·</span>
            <span>{task.engine ?? "Upmo"}</span>
            <span>·</span>
            <span>{task.model ?? "gpt-5.2"}</span>
            <span>·</span>
            <span>started {task.startedAt ?? "08:00"}</span>
            <span>·</span>
            <span className="task-cost-highlight">{task.cost ?? "€2.10"}</span>
            <span>·</span>
            <span className="task-status-indicator">
              <CaretIcon size={12} strokeWidth={12} className="task-status-caret" />
              {task.status}
            </span>
          </div>
          <Text className="task-meta">{task.detail}</Text>
        </div>

        <Group gap="xs" className="task-head-actions">
          <Button
            size="sm"
            variant="default"
            onClick={() => setPaused(!paused)}
          >
            {paused ? "Resume" : "Pause"}
          </Button>
          <Button
            size="sm"
            disabled={!advance || next.isPending}
            onClick={() => next.mutate()}
          >
            Next phase
          </Button>
          <Button
            size="sm"
            variant="default"
            onClick={() => setShareOpen(true)}
          >
            Share
          </Button>
        </Group>
      </header>

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
                  <ol className="task-phases u-tl" aria-label="Phases">
                    {phases.map((phase) => {
                      const isDone =
                        task.status === "Done" ||
                        (task.status !== "Failed" &&
                          phases.indexOf(phase) < phases.indexOf(task.phase));
                      const isCurrent = phase === current;
                      return (
                        <li
                          key={phase}
                          className={`task-phase ${isCurrent ? "task-phase-current" : ""}`}
                          data-done={isDone ? "true" : undefined}
                          data-current={isCurrent ? "true" : undefined}
                          aria-current={isCurrent ? "step" : undefined}
                        >
                          {phase}
                        </li>
                      );
                    })}
                  </ol>

                  {task.subtasks && task.subtasks.length > 0 ? (
                    <Stack gap="xs" mt="md">
                      <Title order={2}>Subtasks</Title>
                      <SubtaskList nodes={task.subtasks} label="Subtasks" />
                    </Stack>
                  ) : null}

                  <div className="task-composer">
                    <Textarea
                      rows={2}
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
                      <Tooltip label="Send message" position="bottom" openDelay={300}>
                        <ActionIcon
                          size="sm"
                          variant="filled"
                          aria-label="Send message"
                          disabled={!taskMessage.trim()}
                          onClick={() => {
                            if (taskMessage.trim()) setTaskMessage("");
                          }}
                        >
                          <ShellIcon concept="action.send" size={16} />
                        </ActionIcon>
                      </Tooltip>
                    </div>
                  </div>
                </div>
              ) : (
                <div className="task-events-view">
                  <div className="task-evlog-head">
                    <Text size="xs" fw={500}>
                      {task.events?.length ?? defaultTaskEvents.length} events · Following live
                    </Text>
                  </div>
                  <ol className="task-evlog-list">
                    {(task.events ?? defaultTaskEvents).map((ev) => (
                      <li key={ev.id} className="task-evlog-item">
                        <div className="task-evlog-row">
                          <time className="task-evlog-time">{ev.time}</time>
                          <Badge size="xs" variant="outline" className="task-evlog-phase">
                            {ev.phase}
                          </Badge>
                          <span className="task-evlog-msg">{ev.message}</span>
                        </div>
                        {ev.detail ? (
                          <div className="task-evlog-detail">
                            {ev.detail.map((d, i) => (
                              <div key={i}>{d}</div>
                            ))}
                          </div>
                        ) : null}
                      </li>
                    ))}
                  </ol>
                </div>
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
                    <div className="u-artifacts task-artifacts-layout">
                      <div className="task-artifact-list">
                        {artifacts.map((art) => (
                          <button
                            key={art.id}
                            type="button"
                            className={`task-artifact-tab ${currentArtifactId === art.id ? "active" : ""}`}
                            onClick={() => setSelectedArtifactId(art.id)}
                          >
                            <span className="task-artifact-tab-name">{art.name}</span>
                            <span className="task-artifact-tab-meta">
                              {art.kind === "markdown" ? "Markdown" : "CSV"} · {art.size} · {art.time}
                            </span>
                          </button>
                        ))}
                      </div>

                      <div className="task-artifact-view">
                        {activeArtifact ? (
                          <>
                            <div className="task-artifact-head">
                              <div>
                                <Text fw={600} size="sm">
                                  {activeArtifact.name}
                                </Text>
                                <Text size="xs" c="dimmed">
                                  {activeArtifact.kind === "markdown" ? "Markdown" : "CSV"} · {activeArtifact.size} · {activeArtifact.time}
                                </Text>
                              </div>
                              <Group gap="xs">
                                {activeArtifact.kind === "markdown" ? (
                                  <SegmentedControl
                                    size="xs"
                                    value={markdownView}
                                    onChange={(val) => setMarkdownView(val as "rendered" | "source")}
                                    data={[
                                      { label: "Rendered", value: "rendered" },
                                      { label: "Source", value: "source" },
                                    ]}
                                  />
                                ) : null}
                                <Tooltip label="Download artifact" position="bottom" openDelay={300}>
                                  <ActionIcon
                                    size="sm"
                                    variant="outline"
                                    aria-label="Download artifact"
                                    onClick={() => {
                                      const blob = new Blob([activeArtifact.content], { type: "text/plain" });
                                      const url = URL.createObjectURL(blob);
                                      const a = document.createElement("a");
                                      a.href = url;
                                      a.download = activeArtifact.name;
                                      a.click();
                                      URL.revokeObjectURL(url);
                                    }}
                                  >
                                    <IconDownload size={14} />
                                  </ActionIcon>
                                </Tooltip>
                              </Group>
                            </div>
                            <div className="task-artifact-body">
                              {activeArtifact.kind === "csv" ? (
                                <Table striped highlightOnHover withTableBorder withColumnBorders>
                                  <Table.Thead>
                                    <Table.Tr>
                                      {activeArtifact.content
                                        .split("\n")[0]
                                        .split(",")
                                        .map((col, idx) => (
                                          <Table.Th key={idx}>{col}</Table.Th>
                                        ))}
                                    </Table.Tr>
                                  </Table.Thead>
                                  <Table.Tbody>
                                    {activeArtifact.content
                                      .split("\n")
                                      .slice(1)
                                      .filter((row) => row.trim().length > 0)
                                      .map((row, rIdx) => (
                                        <Table.Tr key={rIdx}>
                                          {row.split(",").map((cell, cIdx) => (
                                            <Table.Td key={cIdx}>{cell}</Table.Td>
                                          ))}
                                        </Table.Tr>
                                      ))}
                                  </Table.Tbody>
                                </Table>
                              ) : markdownView === "source" ? (
                                <pre className="task-artifact-pre">
                                  <code>{activeArtifact.content}</code>
                                </pre>
                              ) : (
                                <div className="task-artifact-prose">
                                  {activeArtifact.content.split("\n\n").map((para, pIdx) => {
                                    if (para.startsWith("## ")) {
                                      return (
                                        <Title order={3} key={pIdx} style={{ fontSize: "16px", marginTop: "8px" }}>
                                          {para.replace("## ", "")}
                                        </Title>
                                      );
                                    }
                                    if (para.startsWith("- ")) {
                                      const items = para.split("\n").filter((l) => l.startsWith("- "));
                                      return (
                                        <ul key={pIdx} style={{ paddingLeft: "20px", margin: "6px 0" }}>
                                          {items.map((item, iIdx) => (
                                            <li key={iIdx}>{item.replace("- ", "")}</li>
                                          ))}
                                        </ul>
                                      );
                                    }
                                    return <p key={pIdx}>{para}</p>;
                                  })}
                                </div>
                              )}
                            </div>
                          </>
                        ) : null}
                      </div>
                    </div>
                  </section>
                ) : null}
              </Stack>
            </Tabs.Panel>
          </Tabs>
        </div>

        <aside className="u-task-side">
          {/* Budget Card */}
          <div className="u-card task-side-card">
            <div className="task-side-card-head">
              <h4 className="task-side-card-title">Budget</h4>
              <Tooltip label="Edit budget" position="bottom" openDelay={300}>
                <ActionIcon size="sm" variant="subtle" aria-label="Edit budget">
                  <IconPencil size={14} />
                </ActionIcon>
              </Tooltip>
            </div>
            <div className="task-budget-progress-row">
              <Progress
                value={budgetPercent}
                size="sm"
                aria-label="Budget used"
                className="task-budget-bar"
              />
              <span className="task-budget-pct">{budgetPercent}%</span>
            </div>
            <div className="task-side-card-meta">
              {task.cost ?? "€2.10"} of {task.spendLimit ?? "€5.00"} ·{" "}
              {task.tokens ? `${Math.round(task.tokens / 1000)}k` : "38k"} tokens · pauses at{" "}
              {task.pausePercent ?? 90}%
            </div>
            <Link to="/usage" className="task-usage-link">
              See usage for this task
            </Link>
          </div>

          {/* Delivery Card */}
          <div className="u-card task-side-card">
            <div className="task-side-card-head">
              <h4 className="task-side-card-title">Delivery</h4>
              <Tooltip label="Change delivery channel" position="bottom" openDelay={300}>
                <ActionIcon size="sm" variant="subtle" aria-label="Change delivery channel">
                  <IconPencil size={14} />
                </ActionIcon>
              </Tooltip>
            </div>
            <div className="task-delivery-body">
              <span className="task-delivery-icon">
                <ShellIcon concept="action.send" size={16} />
              </span>
              <div className="task-delivery-text">
                <span className="task-delivery-name">
                  {task.deliveryChannel ?? "#finance on Slack"}
                </span>
                <span className="task-delivery-when">
                  {task.deliveryWhen ?? "After the task is done"}
                </span>
              </div>
            </div>
          </div>

          {/* Sources Card */}
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

          {/* Run History Card */}
          {task.runHistory && task.runHistory.length > 0 ? (
            <div className="u-card task-side-card">
              <h4 className="task-side-card-title">Run history</h4>
              <div className="task-run-history-list">
                {task.runHistory.map((item) => (
                  <div key={item.run} className="task-run-history-item">
                    <span>Run {item.run}</span>
                    <TaskStatusBadge status={item.status} marker={`run-${item.run}-status`} />
                  </div>
                ))}
              </div>
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
            <TaskStatusBadge status={node.status} marker="subtask-status" />
          </span>
          {node.children && node.children.length > 0 ? <SubtaskList nodes={node.children} /> : null}
        </li>
      ))}
    </ul>
  );
}
