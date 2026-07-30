import { createContext, useContext, useState, type ReactNode } from "react";

import type { Approval, AppNotification } from "../features/notifications/types";
import { phases, type Phase, type Task } from "../features/tasks/types";

export type { Approval, AppNotification, NotificationKind } from "../features/notifications/types";
export type { Phase, Task, TaskStatus } from "../features/tasks/types";

export interface ApiClient {
  listTasks(): Promise<Task[]>;
  listNotifications(): Promise<AppNotification[]>;
  approveNotification(id: string): Promise<void>;
  nextPhase(taskId: string): Promise<Task>;
  approvals: {
    list(): Promise<Approval[]>;
    approve(id: string): Promise<Approval>;
    deny(id: string): Promise<Approval>;
  };
}

export function tasksQueryKey() {
  return ["tasks"] as const;
}

export function approvalsQueryKey() {
  return ["approvals"] as const;
}

export function notificationsQueryKey() {
  return ["notifications"] as const;
}

/** JSON round-trip so a caller cannot change the stored record. */
function copy<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

const taskFixtures: Task[] = [
    {
      id: "aster-digest",
      title: "Daily finance digest",
      status: "Needs approval",
      phase: "Planning",
      result: "The daily finance digest is ready.",
      detail: "Send the daily finance digest.",
    },
    {
      id: "aster-mail",
      title: "Mail triage",
      status: "Working",
      phase: "Preparing",
      result: "Mail triage is finished.",
      detail: "Sort the morning mail.",
    },
    {
      id: "aster-done",
      title: "File the receipts",
      status: "Done",
      phase: "Checking",
      result: "The receipts are filed.",
      detail: "File the receipts for Aster.",
    },
    {
      id: "aster-failed",
      title: "Weekly close",
      status: "Failed",
      phase: "Checking",
      result: "",
      detail: "Close the week for Aster.",
    },
  ];

const notificationFixtures: AppNotification[] = [
  { id: "aster-failed", kind: "failed", title: "Weekly close failed" },
  { id: "aster-finished", kind: "finished", title: "Mail triage done" },
  { id: "aster-question", kind: "question", title: "Question about the budget" },
];

const approvalFixtures: Approval[] = [
  {
    id: "aster-approval",
    taskId: "aster-digest",
    title: "Approve the digest",
    action: "Send the daily finance digest",
    state: "pending",
  },
];

function blocked(task: Task, approvals: Approval[]) {
  if (task.status === "Needs approval" || task.status === "Done" || task.status === "Failed") {
    return true;
  }
  return approvals.some((item) => item.taskId === task.id && item.state === "pending");
}

function advance(task: Task) {
  const index = phases.indexOf(task.phase);
  if (index >= 0 && index < phases.length - 1) {
    task.phase = phases[index + 1] as Phase;
    task.status = "Working";
    return;
  }
  task.status = "Done";
}

export function createStubApi(): ApiClient {
  const tasks = copy(taskFixtures);
  const notifications = copy(notificationFixtures);
  const approvals = copy(approvalFixtures);

  function settle(id: string, state: "approved" | "denied"): Approval {
    const approval = approvals.find((item) => item.id === id);
    if (!approval) throw new Error("approval missing");
    if (approval.state === "pending") {
      approval.state = state;
      const task = tasks.find((item) => item.id === approval.taskId);
      if (task && task.status === "Needs approval") task.status = "Working";
    }
    return copy(approval);
  }

  return {
    async listTasks() {
      return tasks.map((task) => copy(task));
    },
    async listNotifications() {
      return notifications.map((item) => copy(item));
    },
    async approveNotification(id) {
      const index = notifications.findIndex((item) => item.id === id);
      if (index >= 0) notifications.splice(index, 1);
    },
    async nextPhase(taskId) {
      const task = tasks.find((item) => item.id === taskId);
      if (!task) throw new Error("task missing");
      if (!blocked(task, approvals)) advance(task);
      return copy(task);
    },
    approvals: {
      async list() {
        return approvals.map((item) => copy(item));
      },
      async approve(id) {
        return settle(id, "approved");
      },
      async deny(id) {
        return settle(id, "denied");
      },
    },
  };
}

const ApiContext = createContext<ApiClient | null>(null);

export function ApiProvider({ client, children }: { client?: ApiClient; children: ReactNode }) {
  const [value] = useState(() => client ?? createStubApi());
  return <ApiContext.Provider value={value}>{children}</ApiContext.Provider>;
}

export function useApi(): ApiClient {
  const api = useContext(ApiContext);
  if (!api) throw new Error("API client missing");
  return api;
}
