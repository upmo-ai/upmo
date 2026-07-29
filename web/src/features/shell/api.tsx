import { createContext, useContext, useState, type ReactNode } from "react";

export interface Task {
  id: string;
  title: string;
}

export type NotificationKind = "approval" | "question" | "finished" | "failed";

export interface AppNotification {
  id: string;
  kind: NotificationKind;
  title: string;
}

export interface ApiClient {
  listTasks(): Promise<Task[]>;
  listNotifications(): Promise<AppNotification[]>;
  approveNotification(id: string): Promise<void>;
}

const tasks: Task[] = [
  { id: "aster-digest", title: "Daily finance digest" },
  { id: "aster-mail", title: "Mail triage" },
];

const notificationFixtures: AppNotification[] = [
  { id: "aster-failed", kind: "failed", title: "Weekly close failed" },
  { id: "aster-approval", kind: "approval", title: "Approve the digest" },
  { id: "aster-finished", kind: "finished", title: "Mail triage done" },
  { id: "aster-question", kind: "question", title: "Question about the budget" },
];

export function createStubApi(): ApiClient {
  let notifications = notificationFixtures.map((item) => ({ ...item }));

  return {
    async listTasks() {
      return tasks.map((task) => ({ ...task }));
    },
    async listNotifications() {
      return notifications.map((item) => ({ ...item }));
    },
    async approveNotification(id) {
      notifications = notifications.filter((item) => item.id !== id);
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
