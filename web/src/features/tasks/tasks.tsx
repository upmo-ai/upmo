import { Badge, Button, Group, SegmentedControl, Stack, Tabs, Text, Title } from "@mantine/core";
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
import { EmptyState } from "../../ui/section";
import { ApprovalCard } from "./approval-card";
import {
  isTaskFilter,
  pendingResult,
  phases,
  type Phase,
  type Subtask,
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
                <span className="task-tile-title">{task.title}</span>
                <TaskStatusBadge status={task.status} />
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

export function TaskPage() {
  const params = useParams({ strict: false });
  const taskId = typeof params.taskId === "string" ? params.taskId : "";
  const api = useApi();
  const [shareOpen, setShareOpen] = useState(false);
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

  return (
    <Stack gap="md" className="task-screen">
      {pending.map((approval) => (
        <ApprovalCard key={approval.id} approval={approval} />
      ))}
      <Group justify="space-between" align="center" wrap="wrap">
        <Title order={1}>{task.title}</Title>
        <Button type="button" variant="default" onClick={() => setShareOpen(true)}>
          Share
        </Button>
      </Group>
      <ShareDialog opened={shareOpen} task={task} onClose={() => setShareOpen(false)} />
      <TaskStatusBadge status={task.status} />
      <Text className="task-meta">{task.detail}</Text>
      <div>
        <Button disabled={!advance || next.isPending} onClick={() => next.mutate()}>
          Next phase
        </Button>
      </div>
      <Tabs defaultValue="activity">
        <Tabs.List>
          <Tabs.Tab value="activity">Activity</Tabs.Tab>
          <Tabs.Tab value="result">Result</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="activity">
          <ol className="task-phases" aria-label="Phases">
            {phases.map((phase) => (
              <li
                key={phase}
                className={phase === current ? "task-phase task-phase-current" : "task-phase"}
                aria-current={phase === current ? "step" : undefined}
              >
                {phase}
              </li>
            ))}
          </ol>
          {task.subtasks && task.subtasks.length > 0 ? (
            <Stack gap="xs" mt="md">
              <Title order={2}>Subtasks</Title>
              <SubtaskList nodes={task.subtasks} label="Subtasks" />
            </Stack>
          ) : null}
        </Tabs.Panel>
        <Tabs.Panel value="result">
          <Text data-testid="task-result">
            {task.status === "Done" ? task.result : pendingResult}
          </Text>
        </Tabs.Panel>
      </Tabs>
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
