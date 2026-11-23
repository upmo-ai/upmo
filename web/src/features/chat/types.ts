export type LineRole = "user" | "assistant";

/** One row of GET /api/chats. */
export interface ChatSummary {
  id: string;
  title: string;
  preview: string;
  /** ISO 8601 instant of the newest line, or of creation for an empty chat. */
  last_activity: string;
}

export interface ChatAttachment {
  name: string;
  mime: string;
  size_bytes: number;
  sha256?: string;
}

export interface ChatAttachmentUpload {
  file: File;
}

/** One message of GET /api/chats/{id}. */
export interface ChatLine {
  id: string;
  role: LineRole;
  body: string;
  /** The person who sent a user line. Null for an assistant line. */
  author_id?: string | null;
  author_name?: string | null;
  author_avatar?: string | null;
  /** The model that wrote an assistant line. Null for a user line. */
  model: string | null;
  tokens: number;
  /** Null means the cost is unknown. It is never stored as 0. */
  cost_usd: number | null;
  /** ISO 8601 instant. */
  at: string;
  /** Absent means done. "pending" is a reply still being written; it lives only in the page. */
  status?: LineStatus;
  /** Set on a user line the brain has stored no reply for yet, so a reload still shows the wait. */
  pending?: boolean;
  /** The user line a reply answers. */
  reply_to?: string | null;
  /** The models a failed reply tried, and the plain reason each failed. */
  attempts?: ChatAttempt[];
  /** Uploaded files attached to a user line. */
  attachments?: ChatAttachment[];
  /** The last delta applied to a pending reply, so a replayed one is not added twice. */
  seq?: number;
}

export type LineStatus = "done" | "failed" | "pending";

export interface ChatAttempt {
  model: string;
  reason: string;
}

/** A model that failed before the reply began, and the one tried next. */
export interface ChatNotice {
  line_id: string;
  failed_model: string;
  reason: string;
  next_model: string;
}

export interface Chat {
  id: string;
  lines: ChatLine[];
  tainted?: boolean;
  private_read?: boolean;
  /** Switch notices for replies still pending. Never read from the server. */
  notices?: ChatNotice[];
}
