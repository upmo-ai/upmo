import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { chatQueryKey, chatsQueryKey, useApi } from "../../app/api";
import { toChatLine } from "../../app/brain-api";
import type { Chat, ChatAttempt, ChatLine, ChatNotice } from "./types";

/** A stream that says nothing for this long is treated as dead and polling takes over. */
export const STREAM_SILENCE_MS = 30_000;

/** Errors in a row with no event between them before polling takes over. */
export const STREAM_ERROR_LIMIT = 3;

/** How long the page polls after a fallback before it tries the stream again. */
export const STREAM_RETRY_MS = 60_000;

export type StreamState = "streaming" | "polling";

/** A piece of a reply that is still being written. */
export interface Delta {
  lineId: string;
  seq: number;
  body: string;
}

function parse(data: string): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(data);
    return typeof value === "object" && value !== null && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

/** The data of a `delta` event, or null when bad. */
export function parseDelta(data: string): Delta | null {
  const source = parse(data);
  if (source === null) return null;
  const { line_id: lineId, seq, body } = source;
  if (typeof lineId !== "string" || lineId === "" || typeof body !== "string") return null;
  if (typeof seq !== "number" || !Number.isInteger(seq) || seq < 1) return null;
  return { lineId, seq, body };
}

/** The data of a `status` event, or null when bad. */
export function parseNotice(data: string): ChatNotice | null {
  const source = parse(data);
  if (source === null) return null;
  const { line_id: lineId, failed_model: failed, reason, next_model: next } = source;
  if (typeof lineId !== "string" || lineId === "") return null;
  if (typeof failed !== "string" || typeof reason !== "string" || typeof next !== "string") return null;
  return { line_id: lineId, failed_model: failed, reason, next_model: next };
}

/** The data of a `line` event: the detail response's line JSON, or null when bad. */
export function parseLine(data: string): ChatLine | null {
  try {
    return toChatLine(JSON.parse(data));
  } catch {
    return null;
  }
}

/** The reply being written, as a line of the page's own: it is never stored. */
function pendingLine(id: string): ChatLine {
  return { id, role: "assistant", body: "", model: null, tokens: 0, cost_usd: null, at: "", status: "pending", seq: 0 };
}

/**
 * The chat with a delta added to its pending reply, which the first delta
 * starts. A delta already applied (its seq is not past the last one) and a delta
 * for a reply that is already stored change nothing.
 */
export function withDelta(chat: Chat | undefined, delta: Delta): Chat | undefined {
  if (!chat) return undefined;
  const at = chat.lines.findIndex((line) => line.id === delta.lineId);
  const current = at === -1 ? pendingLine(delta.lineId) : chat.lines[at];
  if (current.status !== "pending" || delta.seq <= (current.seq ?? 0)) return chat;
  const next: ChatLine = { ...current, body: current.body + delta.body, seq: delta.seq };
  if (at === -1) return { ...chat, lines: [...chat.lines, next] };
  return { ...chat, lines: chat.lines.map((line, index) => (index === at ? next : line)) };
}

/**
 * The chat with a stored line in it. It replaces the pending reply of the same
 * id and is otherwise added once. A reply to a user line takes the place of any
 * other reply to that line that failed, and the notices of its own id go.
 */
export function withLine(chat: Chat | undefined, line: ChatLine): Chat | undefined {
  if (!chat) return undefined;
  const same = chat.lines.find((seen) => seen.id === line.id);
  if (same && same.status !== "pending") return chat;
  const merged = same ? chat.lines.map((seen) => (seen.id === line.id ? line : seen)) : [...chat.lines, line];
  const lines = merged.filter(
    (seen) => seen.id === line.id || !(line.reply_to && seen.reply_to === line.reply_to && seen.status === "failed"),
  );
  const notices = chat.notices?.filter((notice) => notice.line_id !== line.id);
  return { ...chat, lines, ...(notices === undefined ? {} : { notices }) };
}

/** The chat with a switch notice added, or unchanged when the same notice is already there. */
export function withNotice(chat: Chat | undefined, notice: ChatNotice): Chat | undefined {
  if (!chat) return undefined;
  const seen = chat.notices?.some(
    (item) =>
      item.line_id === notice.line_id &&
      item.failed_model === notice.failed_model &&
      item.next_model === notice.next_model,
  );
  if (seen) return chat;
  return { ...chat, notices: [...(chat.notices ?? []), notice] };
}

/**
 * A thread just read, with the replies the page holds half of kept. The read
 * has no pending reply, so a poll would otherwise drop the text already shown
 * and a stream that resumes would carry on from the middle of it.
 */
export function keepPending(fresh: Chat, cached: Chat | undefined): Chat {
  const held = cached?.lines.filter(
    (line) => line.status === "pending" && !fresh.lines.some((seen) => seen.id === line.id),
  );
  return held?.length ? { ...fresh, lines: [...fresh.lines, ...held] } : fresh;
}

/** The newest event id the page has: the one just heard, else the one it had. */
export function newestEventId(seen: string | undefined, heard: string): string | undefined {
  return heard === "" ? seen : heard;
}

/**
 * Where a stream opened by the page resumes. A reply the page holds half of
 * resumes after the newest event heard, so its text carries on. With none
 * held, the stream starts cursorless and replays the reply under way from its
 * first piece.
 */
export function resumeAfter(chat: Chat | undefined, newest: string | undefined): string | undefined {
  return chat?.lines.some((line) => line.status === "pending") ? newest : undefined;
}

/** The stream URL for a chat: /api/chats/{id}/stream, replaying after `after`. */
export function streamUrl(chatId: string, after: string | undefined): string {
  const path = `/api/chats/${encodeURIComponent(chatId)}/stream`;
  return after === undefined ? path : `${path}?after=${encodeURIComponent(after)}`;
}

/** What the page says when a model did not answer and the next is tried. */
export function noticeText(notice: ChatNotice): string {
  const failed = `${notice.failed_model} didn't answer (${notice.reason})`;
  return notice.next_model === "" ? failed : `${failed}, trying ${notice.next_model}`;
}

/** The models a failed reply tried, each with why it failed. */
export function attemptsText(attempts: readonly ChatAttempt[] | undefined): string {
  if (!attempts || attempts.length === 0) return "";
  return `Tried ${attempts.map((attempt) => `${attempt.model} (${attempt.reason})`).join(", then ")}.`;
}

/**
 * The open chat's event stream, GET /api/chats/{id}/stream. "streaming" means
 * the page can stop polling the chat; "polling" means it polls while a reply
 * is awaited, which is also the answer before the stream has opened. After a
 * fallback the stream is tried again once a minute of polling has passed.
 *
 * The hook is called before the page's thread query and reads the cache itself:
 * the stream opens only once the chat has loaded.
 */
export function useChatStream(chatId: string | undefined): StreamState {
  const api = useApi();
  const queryClient = useQueryClient();
  const [state, setState] = useState<StreamState>("polling");
  // Bumped when a fallback's retry falls due, so the effect opens a new stream.
  const [attempt, setAttempt] = useState(0);
  // The newest event heard, for a stream that resumes after a fallback.
  const newest = useRef<{ chat: string | undefined; id: string | undefined }>({ chat: undefined, id: undefined });
  // The page owns the thread query; this is only whether it has data.
  const loaded = useSyncExternalStore(
    useCallback(
      (notify: () => void) => queryClient.getQueryCache().subscribe(() => notify()),
      [queryClient],
    ),
    () => chatId !== undefined && queryClient.getQueryData(chatQueryKey(chatId)) !== undefined,
  );

  useEffect(() => {
    // The stub brain has no stream and jsdom has no EventSource: the page polls.
    if (chatId === undefined || typeof EventSource === "undefined" || !api.chatStream || !loaded) return;
    const key = chatQueryKey(chatId);
    if (newest.current.chat !== chatId) newest.current = { chat: chatId, id: undefined };
    const after = resumeAfter(queryClient.getQueryData<Chat>(key), newest.current.id);
    const source = new EventSource(streamUrl(chatId, after), { withCredentials: true });
    let errors = 0;
    let silence: ReturnType<typeof setTimeout> | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;

    const stop = () => {
      stopped = true;
      if (silence !== undefined) clearTimeout(silence);
      source.close();
    };
    const fallback = () => {
      if (stopped) return;
      stop();
      setState("polling");
      // Polling keeps the page live while the stream waits for another try.
      retry = setTimeout(() => setAttempt(attempt + 1), STREAM_RETRY_MS);
    };
    // Any event proves the stream is alive and clears the error count. EventSource
    // shows no comment heartbeats, so a quiet stream falls back after 30 s.
    const heard = (event?: Event) => {
      errors = 0;
      if (silence !== undefined) clearTimeout(silence);
      silence = setTimeout(fallback, STREAM_SILENCE_MS);
      const id = (event as MessageEvent | undefined)?.lastEventId;
      if (id !== undefined) newest.current.id = newestEventId(newest.current.id, id);
    };
    // A read still in flight would land later and drop what the event added.
    const apply = async (update: (cached: Chat | undefined) => Chat | undefined) => {
      await queryClient.cancelQueries({ queryKey: key });
      queryClient.setQueryData<Chat>(key, update);
    };

    source.addEventListener("open", () => {
      setState("streaming");
      heard();
      // A stream that replays from the start sends the reply under way whole,
      // and the read closes any gap between what the page has and what is stored.
      if (after === undefined) void queryClient.invalidateQueries({ queryKey: key });
    });
    source.addEventListener("message", heard);
    source.addEventListener("delta", (event) => {
      heard(event);
      const delta = parseDelta(event.data);
      if (delta !== null) void apply((cached) => withDelta(cached, delta));
    });
    source.addEventListener("status", (event) => {
      heard(event);
      const notice = parseNotice(event.data);
      if (notice !== null) void apply((cached) => withNotice(cached, notice));
    });
    source.addEventListener("line", async (event) => {
      heard(event);
      const line = parseLine(event.data);
      if (line === null) return;
      await apply((cached) => withLine(cached, line));
      if (line.role === "assistant") void queryClient.invalidateQueries({ queryKey: chatsQueryKey() });
    });
    source.addEventListener("end", () => {
      // Nothing follows: close before the browser reconnects, and read the chat
      // once so its final state loads.
      stop();
      setState("polling");
      void queryClient.invalidateQueries({ queryKey: key });
    });
    source.addEventListener("error", () => {
      errors += 1;
      // A closed source was refused and the browser is not reconnecting; any
      // other error reconnects by itself, carrying Last-Event-ID.
      if (source.readyState === EventSource.CLOSED || errors >= STREAM_ERROR_LIMIT) fallback();
    });

    return () => {
      stop();
      if (retry !== undefined) clearTimeout(retry);
      // Another chat's stream has not opened yet: until it does, it is polled.
      setState("polling");
    };
  }, [api, loaded, queryClient, chatId, attempt]);

  return state;
}
