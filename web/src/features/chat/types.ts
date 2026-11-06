export type LineRole = "user" | "assistant";

/** One row of GET /api/chats. */
export interface ChatSummary {
  id: string;
  title: string;
  preview: string;
  /** ISO 8601 instant of the newest line, or of creation for an empty chat. */
  last_activity: string;
}

/** One message of GET /api/chats/{id}. */
export interface ChatLine {
  id: string;
  role: LineRole;
  body: string;
  /** The model that wrote an assistant line. Null for a user line. */
  model: string | null;
  tokens: number;
  /** Null means the cost is unknown. It is never stored as 0. */
  cost_usd: number | null;
  /** ISO 8601 instant. */
  at: string;
}

export interface Chat {
  id: string;
  lines: ChatLine[];
}
