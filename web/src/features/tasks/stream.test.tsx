import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createBrainApi } from "../../app/brain-api";
import { AppProviders } from "../../theme";
import {
  STREAM_ERROR_LIMIT,
  STREAM_SILENCE_MS,
  newestEventId,
  parseLine,
  parseStatus,
  streamUrl,
  withEvent,
  withStatus,
} from "./stream";
import type { Task, TaskEvent } from "./types";

const prepId = "01990000-0000-7000-8000-0000000000a1";
const workId = "01990000-0000-7000-8000-0000000000a2";

type Stored = {
  id: string;
  title: string;
  status: string;
  phase: string;
  spent_cents: number;
  limit_cents: number;
  events: { id: string; time: string; phase: string; type: string; message: string }[];
};

function stored(id: string, title: string, status: string, phase: string, events: Stored["events"]): Stored {
  return { id, title, status, phase, spent_cents: 0, limit_cents: 0, events };
}

const opened = { id: "ev-1", time: "2026-11-08T09:00:00Z", phase: "Preparing", type: "open", message: "Task opened" };
const advanced = {
  id: "ev-2",
  time: "2026-11-08T09:00:02Z",
  phase: "Working",
  type: "advance",
  message: "Moved on to Working",
};

type Call = { method: string; path: string };

/** A brain that keeps its tasks in memory and answers like the real routes. */
function brain(options: { refuseList?: boolean } = {}) {
  const tasks = [
    stored(prepId, "Prepare the launch site", "Working", "Preparing", [opened]),
    stored(workId, "Stream the October close", "Working", "Working", [opened, advanced]),
  ];
  const calls: Call[] = [];

  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, path });
    expect(init?.credentials).toBe("include");

    // The shell's bell and the task page read these too; the brain has none here.
    if (method === "GET" && (path === "/api/approvals" || path.startsWith("/api/notifications"))) {
      return Response.json([]);
    }
    if (path === "/api/tasks" && method === "GET") {
      // A missing brain: the probe settles on the stub for the whole session.
      if (options.refuseList) return new Response(null, { status: 404 });
      return Response.json(tasks.map(({ id, title, status, phase }) => ({ id, title, status, phase })));
    }
    const match = /^\/api\/tasks\/([^/]+)$/.exec(path);
    const task = match ? tasks.find((item) => item.id === match[1]) : undefined;
    if (match && task && method === "GET") return Response.json(task);
    return new Response(null, { status: 404 });
  });
  return { fetcher, calls, tasks };
}

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={createBrainApi()} />
    </AppProviders>,
  );
}

/**
 * The EventSource the stream client opens, with the events a test sends. A
 * line a test streams is also written to the brain's store, because the real
 * brain has a broadcast row committed before it sends the event.
 */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readonly url: string;
  withCredentials: boolean;
  readyState = FakeEventSource.CONNECTING;
  closed = 0;
  private listeners = new Map<string, Array<(event: MessageEvent) => void>>();

  constructor(url: string, init?: { withCredentials?: boolean }) {
    this.url = url;
    this.withCredentials = init?.withCredentials ?? false;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent) => void) {
    const current = this.listeners.get(type) ?? [];
    current.push(listener);
    this.listeners.set(type, current);
  }

  close() {
    this.closed += 1;
    this.readyState = FakeEventSource.CLOSED;
  }

  emit(type: string, data: string) {
    if (type === "open") this.readyState = FakeEventSource.OPEN;
    for (const listener of this.listeners.get(type) ?? []) {
      listener(new MessageEvent(type, { data }));
    }
  }

  /** A stream the brain refused: one error, already closed, no reconnect. */
  refuse() {
    this.readyState = FakeEventSource.CLOSED;
    this.emit("error", "");
  }
}

function lineJson(event: Stored["events"][number]) {
  return JSON.stringify(event);
}

async function openWorkTask() {
  renderAt(`/tasks/${workId}`);
  await screen.findByRole("heading", { level: 1, name: "Stream the October close" });
  // The heading and the stream open in the same settle; flush once so the
  // EventSource exists before it is read.
  await act(async () => {});
  const source = FakeEventSource.instances[0];
  expect(source).toBeTruthy();
  act(() => {
    source.emit("open", "");
  });
  return source;
}

function detailGets(calls: Call[]) {
  return calls.filter((call) => call.method === "GET" && call.path === `/api/tasks/${workId}`);
}

describe("stream rules", () => {
  const line = JSON.stringify({
    id: "ev-9",
    time: "2026-11-08T09:00:05Z",
    phase: "Working",
    type: "message",
    message: "Hello",
  });

  it("parses a line's data as the detail response's event JSON", () => {
    expect(parseLine(line)).toEqual({
      id: "ev-9",
      time: "09:00:05",
      phase: "Working",
      type: "message",
      message: "Hello",
    });
  });

  it("ignores a line whose data is not a task event", () => {
    expect(parseLine("not json")).toBeNull();
    expect(parseLine(JSON.stringify({ id: 7, time: "09:00:05" }))).toBeNull();
    expect(parseLine(JSON.stringify({ id: "ev-9", time: "09:00:05", phase: "Done", type: "x", message: "m" }))).toBeNull();
  });

  it("parses a status's data and ignores one with an unknown status or phase", () => {
    expect(parseStatus('{"status":"Needs approval","phase":"Planning"}')).toEqual({
      status: "Needs approval",
      phase: "Planning",
    });
    expect(parseStatus('{"status":"Paused","phase":"Planning"}')).toBeNull();
    expect(parseStatus('{"status":"Working","phase":"Done"}')).toBeNull();
    expect(parseStatus("[]")).toBeNull();
  });

  it("appends an event once: a duplicate id leaves the task as it is", () => {
    const event: TaskEvent = { id: "ev-9", time: "09:00:05", phase: "Working", message: "Hello" };
    const task: Task = { id: workId, title: "T", status: "Working", phase: "Working", result: "", detail: "" };
    const withOne = withEvent(task, event);
    expect(withOne?.events).toEqual([event]);
    expect(withEvent(withOne, event)).toBe(withOne);
  });

  it("patches the status and phase, and leaves a matching task as it is", () => {
    const task: Task = { id: workId, title: "T", status: "Working", phase: "Working", result: "", detail: "" };
    expect(withStatus(task, { status: "Done", phase: "Checking" })).toMatchObject({ status: "Done", phase: "Checking" });
    expect(withStatus(task, { status: "Working", phase: "Working" })).toBe(task);
    expect(withEvent(undefined, { id: "ev-9", time: "09:00:05", phase: "Working", message: "m" })).toBeUndefined();
    expect(withStatus(undefined, { status: "Done", phase: "Checking" })).toBeUndefined();
  });

  it("names the newest event and builds the stream URL after it", () => {
    const task: Task = { id: workId, title: "T", status: "Working", phase: "Working", result: "", detail: "" };
    expect(newestEventId(task)).toBeUndefined();
    expect(newestEventId({ ...task, events: [{ id: "ev-1", time: "09:00:00", phase: "Working", message: "a" }] })).toBe("ev-1");
    expect(streamUrl(workId, undefined)).toBe(`/api/tasks/${workId}/stream`);
    expect(streamUrl(workId, "ev-2")).toBe(`/api/tasks/${workId}/stream?after=ev-2`);
    expect(streamUrl("a b", "x/y")).toBe("/api/tasks/a%20b/stream?after=x%2Fy");
  });
});

describe("task stream", () => {
  beforeEach(() => {
    localStorage.clear();
    FakeEventSource.instances.length = 0;
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("opens with the newest event's id, adds a line once and ignores its duplicate", async () => {
    const { fetcher, tasks } = brain();
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeEventSource);
    const source = await openWorkTask();

    expect(source.url).toBe(`/api/tasks/${workId}/stream?after=ev-2`);
    expect(source.withCredentials).toBe(true);

    act(() => {
      screen.getByRole("radio", { name: "All events" }).click();
    });
    expect(within(screen.getByRole("region", { name: "Activity" })).getByText("2 events")).toBeTruthy();

    const streamLine = {
      id: "ev-101",
      time: "2026-11-08T09:00:07Z",
      phase: "Working",
      type: "message",
      message: "Stream line one",
    };
    tasks[1].events.push(streamLine);
    act(() => {
      source.emit("line", lineJson(streamLine));
    });
    const activity = screen.getByRole("region", { name: "Activity" });
    expect(await within(activity).findByText("3 events")).toBeTruthy();
    expect(within(activity).getAllByText(/Stream line one/)).toHaveLength(1);

    act(() => {
      source.emit("line", lineJson(streamLine));
    });
    expect(within(screen.getByRole("region", { name: "Activity" })).getByText("3 events")).toBeTruthy();
  });

  it("patches the header when a status event arrives", async () => {
    const { fetcher, tasks } = brain();
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeEventSource);
    const source = await openWorkTask();

    expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Working");
    // The brain has the change committed before it broadcasts the status.
    tasks[1].phase = "Planning";
    act(() => {
      source.emit("status", '{"status":"Working","phase":"Planning"}');
    });
    await waitFor(() => {
      expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Planning");
    });
  });

  it("closes and stops on end, and reads the task once more", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeEventSource);
    const source = await openWorkTask();

    act(() => {
      source.emit("end", '{"reason":"finished"}');
    });
    expect(source.closed).toBe(1);
    expect(source.readyState).toBe(FakeEventSource.CLOSED);
    await waitFor(() => {
      expect(detailGets(calls).length).toBeGreaterThan(1);
    });
  });

  it("resumes after the newest event when the page opens the stream again", async () => {
    const { fetcher, tasks } = brain();
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeEventSource);
    const source = await openWorkTask();

    const streamLine = {
      id: "ev-101",
      time: "2026-11-08T09:00:07Z",
      phase: "Working",
      type: "message",
      message: "Stream line one",
    };
    tasks[1].events.push(streamLine);
    act(() => {
      source.emit("line", lineJson(streamLine));
    });

    cleanup();
    renderAt(`/tasks/${workId}`);
    await screen.findByRole("heading", { level: 1, name: "Stream the October close" });
    await act(async () => {});
    expect(FakeEventSource.instances[1]?.url).toBe(`/api/tasks/${workId}/stream?after=ev-101`);
  });

  it("stops at once on a refused stream and after repeated errors", async () => {
    const { fetcher } = brain();
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeEventSource);
    const source = await openWorkTask();

    act(() => {
      source.refuse();
    });
    expect(source.closed).toBe(1);

    cleanup();
    FakeEventSource.instances.length = 0;
    renderAt(`/tasks/${workId}`);
    await screen.findByRole("heading", { level: 1, name: "Stream the October close" });
    await act(async () => {});
    const retrying = FakeEventSource.instances[0];
    expect(retrying).toBeTruthy();
    act(() => {
      retrying.emit("open", "");
      for (let seen = 0; seen < STREAM_ERROR_LIMIT; seen += 1) retrying.emit("error", "");
    });
    expect(retrying.closed).toBe(1);
  });

  it("turns the preparing poll off while streaming and back on after silence", async () => {
    vi.useFakeTimers();
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeEventSource);
    renderAt(`/tasks/${prepId}`);
    // Fake timers also freeze the test's own polling, so the queries settle
    // here before anything is asserted.
    for (let round = 0; round < 5; round += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
    }
    expect(screen.getByRole("heading", { level: 1, name: "Prepare the launch site" })).toBeTruthy();

    const reads = () => calls.filter((call) => call.method === "GET" && call.path === `/api/tasks/${prepId}`);
    const source = FakeEventSource.instances[0];
    expect(source).toBeTruthy();

    // Before the stream opens the task is still polled every second.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(reads().length).toBeGreaterThan(1);

    act(() => {
      source.emit("open", "");
    });
    const streaming = reads().length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_000);
    });
    expect(reads().length).toBe(streaming);

    // Nothing arrives for the whole silence window: polling takes over again.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STREAM_SILENCE_MS + 1_000);
    });
    expect(source.closed).toBe(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(reads().length).toBeGreaterThan(streaming);
  });

  it("keeps polling when there is no EventSource", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt(`/tasks/${prepId}`);

    await screen.findByRole("heading", { level: 1, name: "Prepare the launch site" });
    await waitFor(
      () => {
        expect(
          calls.filter((call) => call.method === "GET" && call.path === `/api/tasks/${prepId}`).length,
        ).toBeGreaterThan(1);
      },
      { timeout: 4_000, interval: 100 },
    );
  });

  it("never streams while the stub brain is used", async () => {
    const { fetcher } = brain({ refuseList: true });
    vi.stubGlobal("fetch", fetcher);
    vi.stubGlobal("EventSource", FakeEventSource);
    renderAt("/tasks/aster-mail");

    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    expect(FakeEventSource.instances.length).toBe(0);
  });
});
