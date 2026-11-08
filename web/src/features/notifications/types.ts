export type NotificationKind = "approval" | "proposal" | "question" | "finished" | "failed";

/** The kinds the brain sends as notifications. An approval is never one: it waits on its own list. */
export const noticeKinds = ["proposal", "question", "finished", "failed"] as const;

export interface AppNotification {
  id: string;
  kind: NotificationKind;
  title: string;
  /** The task the notification is about, when there is one. */
  taskId?: string;
}

/** How many notifications the bell lists. Approvals are listed on top of these. */
export const NOTIFICATION_LIMIT = 50;

export type ApprovalState = "pending" | "approved" | "denied";

export interface Approval {
  id: string;
  taskId: string;
  title: string;
  action: string;
  state: ApprovalState;
}
