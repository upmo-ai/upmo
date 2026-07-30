export type NotificationKind = "approval" | "question" | "finished" | "failed";

export interface AppNotification {
  id: string;
  kind: NotificationKind;
  title: string;
}

export type ApprovalState = "pending" | "approved" | "denied";

export interface Approval {
  id: string;
  taskId: string;
  title: string;
  action: string;
  state: ApprovalState;
}
