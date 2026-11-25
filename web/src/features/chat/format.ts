import { BrainError } from "../../app/api";
import { formatUsd } from "../usage/format";
import type { ChatLine, ChatSummary } from "./types";

export const chatDays = ["Today", "Yesterday", "Earlier"] as const;

export type ChatDay = (typeof chatDays)[number];

const dayMs = 24 * 60 * 60 * 1000;

const clock = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false });
const date = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "short" });

function startOfDay(value: Date) {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate());
}

export function chatDay(iso: string, now: Date): ChatDay {
  const at = new Date(iso).getTime();
  const today = startOfDay(now).getTime();
  if (at >= today) return "Today";
  // A day is not always 24 hours when the clocks change, so step by calendar day.
  const yesterday = startOfDay(new Date(today - dayMs / 2)).getTime();
  if (at >= yesterday) return "Yesterday";
  return "Earlier";
}

/** The time of day for today and yesterday, the date for anything older. */
export function formatActivity(iso: string, now: Date): string {
  const at = new Date(iso);
  return chatDay(iso, now) === "Earlier" ? date.format(at) : clock.format(at);
}

export type ListedChat = ChatSummary & { time: string };

/**
 * Groups keep the order they are given, so a newest first list stays newest
 * first. Each chat carries the time or date to show beside it.
 */
export function groupByDay(chats: readonly ChatSummary[], now: Date) {
  return chatDays
    .map((day) => ({
      day,
      chats: chats
        .filter((chat) => chatDay(chat.last_activity, now) === day)
        .map((chat): ListedChat => ({ ...chat, time: formatActivity(chat.last_activity, now) })),
    }))
    .filter((group) => group.chats.length > 0);
}

// A message costs fractions of a cent, so the usual two decimals would turn a
// known cost into $0.00. Show up to four, and never fewer than two.
const cost = new Intl.NumberFormat("en-GB", {
  style: "currency",
  currency: "USD",
  minimumFractionDigits: 2,
  maximumFractionDigits: 4,
});

/**
 * Unknown stays Unknown, and a known cost is never rounded down to nothing.
 */
export function formatCost(value: number | null): string {
  if (value === null) return formatUsd(null);
  if (value > 0 && value < 0.0001) return `Under ${cost.format(0.0001)}`;
  return cost.format(value);
}

/**
 * The newest line is the user's and a reply comes on its own: the stub writes
 * an assistant line a moment later, and the brain marks a line it has no reply
 * for as pending, so a reload still shows the wait.
 */
export function awaitingReply(lines: readonly ChatLine[] | undefined, replyFollows: boolean): boolean {
  const last = lines?.at(-1);
  return last?.role === "user" && (replyFollows || last.pending === true);
}

/** A 403 means the chat can be read but not written in. A 409 means a reply is already running. Any other problem is shown as it comes. */
export function sendRefusal(error: unknown): string {
  if (error instanceof BrainError && error.status === 409) return "A reply is already under way in this chat.";
  if (error instanceof BrainError && error.status === 403) return "You can read this chat but not write in it.";
  return error instanceof Error ? error.message : String(error);
}
