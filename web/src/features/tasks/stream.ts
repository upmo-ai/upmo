import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { taskQueryKey, tasksQueryKey, useApi } from "../../app/api";
import { toEvent } from "../../app/brain-api";
import { phases, taskStatuses, type Phase, type Task, type TaskEvent, type TaskStatus } from "./types";

/** A stream that says nothing for this long is treated as dead and polling takes over. */
export const STREAM_SILENCE_MS = 30_000;

/** Errors in a row with no event between them before polling takes over. */
export const STREAM_ERROR_LIMIT = 3;

/** How long the page polls after a fallback before it tries the stream again. */
export const STREAM_RETRY_MS = 60_000;

export type StreamState = "streaming" | "polling";

/** The data of a `line` event: the detail response's event JSON, or null when bad. */
export function parseLine(data: string): TaskEvent | null {
  try {
    return toEvent(JSON.parse(data));
  } catch {
    return null;
  }
}

/** The data of a `status` event: the task's status and phase, or null when bad. */
export function parseStatus(data: string): { status: TaskStatus; phase: Phase } | null {
  let value: unknown;
  try {
    value = JSON.parse(data);
  } catch {
    return null;
  }
  if (typeof value !== "object" || value === null) return null;
  const source = value as Record<string, unknown>;
  if (!(taskStatuses as readonly string[]).includes(String(source.status))) return null;
  if (!(phases as readonly string[]).includes(String(source.phase))) return null;
  return { status: source.status as TaskStatus, phase: source.phase as Phase };
}

/** The task with the event appended, or unchanged when its id is already there. */
export function withEvent(task: Task | undefined, event: TaskEvent): Task | undefined {
  if (!task) return undefined;
  if (task.events?.some((seen) => seen.id === event.id)) return task;
  return { ...task, events: [...(task.events ?? []), event] };
}

/** The task with its status and phase patched, or unchanged when they already match. */
export function withStatus(
  task: Task | undefined,
  next: { status: TaskStatus; phase: Phase },
): Task | undefined {
  if (!task) return undefined;
  if (task.status === next.status && task.phase === next.phase) return task;
  return { ...task, status: next.status, phase: next.phase };
}

/** The newest event id the page already has, so the stream resumes after it. */
export function newestEventId(task: Task | undefined): string | undefined {
  return task?.events?.at(-1)?.id;
}

/** The stream URL for a task: /api/tasks/{id}/stream, replaying after `after`. */
export function streamUrl(taskId: string, after: string | undefined): string {
  const path = `/api/tasks/${encodeURIComponent(taskId)}/stream`;
  return after === undefined ? path : `${path}?after=${encodeURIComponent(after)}`;
}

/**
 * The open task's event stream, GET /api/tasks/{id}/stream. "streaming" means
 * the page can stop polling the task; "polling" means it polls as before,
 * which is also the answer before the stream has opened. After a fallback the
 * stream is tried again once a minute of polling has passed.
 *
 * The hook is called before the page's detail query and reads the cache itself:
 * the stream opens only once the task has loaded (it resumes after the newest
 * event the page has), and the query's refetchInterval callback already runs
 * during the first render, before anything declared after the query would exist.
 */
export function useTaskStream(taskId: string): StreamState {
  const api = useApi();
  const queryClient = useQueryClient();
  const [state, setState] = useState<StreamState>("polling");
  // Bumped when a fallback's retry falls due, so the effect opens a new stream.
  const [attempt, setAttempt] = useState(0);
  // The task page owns the detail query; this is only whether it has data.
  const loaded = useSyncExternalStore(
    useCallback(
      (notify: () => void) => queryClient.getQueryCache().subscribe(() => notify()),
      [queryClient],
    ),
    () => queryClient.getQueryData(taskQueryKey(taskId)) !== undefined,
  );

  useEffect(() => {
    // The stub brain has no stream and jsdom has no EventSource: the page polls.
    if (typeof EventSource === "undefined" || !api.taskEventsStream || !loaded) return;
    const after = newestEventId(queryClient.getQueryData<Task>(taskQueryKey(taskId)));
    const source = new EventSource(streamUrl(taskId, after), { withCredentials: true });
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
      // The retry bumps the attempt this run opened, so the effect reopens.
      retry = setTimeout(() => setAttempt(attempt + 1), STREAM_RETRY_MS);
    };
    // Any event proves the stream is alive and clears the error count. EventSource
    // shows no comment heartbeats, so a quiet stream falls back after 30 s.
    const heard = () => {
      errors = 0;
      if (silence !== undefined) clearTimeout(silence);
      silence = setTimeout(fallback, STREAM_SILENCE_MS);
    };

    source.addEventListener("open", () => {
      setState("streaming");
      heard();
    });
    source.addEventListener("message", heard);
    source.addEventListener("line", async (event) => {
      heard();
      const line = parseLine(event.data);
      if (line === null) return;
      // A task read still in flight would land later and drop the line.
      await queryClient.cancelQueries({ queryKey: taskQueryKey(taskId) });
      queryClient.setQueryData<Task>(taskQueryKey(taskId), (cached) => withEvent(cached, line));
      void queryClient.invalidateQueries({ queryKey: tasksQueryKey(), exact: true });
    });
    source.addEventListener("status", async (event) => {
      heard();
      const next = parseStatus(event.data);
      if (next === null) return;
      // A task read still in flight would land later and drop the status.
      await queryClient.cancelQueries({ queryKey: taskQueryKey(taskId) });
      queryClient.setQueryData<Task>(taskQueryKey(taskId), (cached) => withStatus(cached, next));
      void queryClient.invalidateQueries({ queryKey: tasksQueryKey(), exact: true });
    });
    source.addEventListener("end", () => {
      // Nothing follows: close before the browser reconnects, and read the task
      // once so its final state loads.
      stop();
      setState("polling");
      void queryClient.invalidateQueries({ queryKey: taskQueryKey(taskId) });
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
    };
  }, [api, loaded, queryClient, taskId, attempt]);

  return state;
}
