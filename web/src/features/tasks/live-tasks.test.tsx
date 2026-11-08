import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createBrainApi } from "../../app/brain-api";
import { AppProviders } from "../../theme";

const mailId = "01990000-0000-7000-8000-000000000001";
const planId = "01990000-0000-7000-8000-000000000002";
const doneId = "01990000-0000-7000-8000-000000000003";
const unlistedId = "01990000-0000-7000-8000-000000000004";
const failedId = "01990000-0000-7000-8000-000000000005";

type Event = { id: string; time: string; phase: string; type: string; message: string };
type Stored = {
  id: string;
  title: string;
  status: string;
  phase: string;
  spent_cents: number;
  limit_cents: number;
  updated_at: string;
  result: string;
  detail: string;
  subtasks: { id: string; title: string; status: string }[];
  events: Event[];
};

function stored(id: string, title: string, status: string, phase: string, extra: Partial<Stored> = {}): Stored {
  return {
    id,
    title,
    status,
    phase,
    spent_cents: 0,
    limit_cents: 0,
    updated_at: "2026-11-08T09:00:00Z",
    result: "",
    detail: "",
    subtasks: [],
    events: [],
    ...extra,
  };
}

function event(id: string, phase: string, type: string, message: string): Event {
  return { id, time: "2026-11-08T09:00:00Z", phase, type, message };
}

type Call = { method: string; path: string; body: unknown };

/** A brain that keeps its tasks in memory and answers like the real routes. */
function brain(options: { list?: () => Response; refuseCreate?: boolean } = {}) {
  const tasks: Stored[] = [
    stored(mailId, "Sort the morning mail", "Working", "Working", {
      spent_cents: 45,
      limit_cents: 200,
      subtasks: [{ id: "child-1", title: "Flag the invoices", status: "Done" }],
      events: [
        event("ev-1", "Preparing", "open", "Task opened"),
        event("ev-2", "Working", "advance", "Moved on to Working"),
      ],
    }),
    stored(planId, "Plan the launch email", "Working", "Planning", {
      events: [event("ev-3", "Preparing", "open", "Task opened"), event("ev-4", "Planning", "plan", "Planning")],
    }),
    stored(doneId, "Reconcile the accounts", "Done", "Checking", {
      result: '{"detail":"All 31 entries matched.","summary":"The accounts reconcile."}',
    }),
  ];
  // Opened by its link, but not in the list: the page must read it by its own id.
  const unlisted = stored(unlistedId, "Only the detail knows me", "Working", "Working", {
    events: [event("ev-5", "Preparing", "open", "Task opened")],
  });
  const calls: Call[] = [];
  let serial = 100;

  const head = (task: Stored) => ({
    id: task.id,
    title: task.title,
    status: task.status,
    phase: task.phase,
    spent_cents: task.spent_cents,
    limit_cents: task.limit_cents,
    updated_at: task.updated_at,
  });
  const find = (id: string) => [...tasks, unlisted].find((task) => task.id === id);

  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    expect(init?.credentials).toBe("include");

    if (path === "/api/tasks" && method === "GET") return options.list?.() ?? Response.json(tasks.map(head));
    if (path === "/api/tasks" && method === "POST" && options.refuseCreate) {
      // Like the real brain with no sandbox: the task is made and marked Failed, then the answer is 503.
      const goal = (body as { goal: string }).goal;
      tasks.push(stored(failedId, goal, "Failed", "Preparing", { detail: "parent_cancelled" }));
      return Response.json({ error: "no sandbox is configured" }, { status: 503 });
    }
    const match = /^\/api\/tasks\/([^/]+)$/.exec(path);
    const task = match ? find(match[1] ?? "") : undefined;
    if (match && !task) return Response.json({ error: "no such task" }, { status: 404 });
    if (task && method === "GET") return Response.json({ ...task, ...head(task) });
    if (task && method === "POST") {
      const step = body as { kind: string; body?: string };
      if (step.kind === "chat") {
        serial += 1;
        const posted = event(`ev-${serial}`, task.phase, "message", step.body ?? "");
        task.events.push(posted);
        return Response.json(posted, { status: 201 });
      }
      if (step.kind === "advance" && task.phase === "Planning") {
        serial += 1;
        task.phase = "Working";
        task.events.push(event(`ev-${serial}`, "Working", "advance", "Moved on to Working"));
        return Response.json({ ...task, ...head(task) });
      }
      return Response.json({ error: "the task cannot take that step" }, { status: 409 });
    }
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
  return history;
}

function badges() {
  return within(screen.getByRole("list", { name: "Tasks" }))
    .getAllByTestId("task-status")
    .map((badge) => badge.textContent);
}

describe("tasks from the brain", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists the brain's tasks with their status and phase, not the sample ones", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderAt("/tasks");

    expect(await screen.findByText("Sort the morning mail")).toBeTruthy();
    expect(screen.getByText("Plan the launch email")).toBeTruthy();
    expect(screen.getByText("Reconcile the accounts")).toBeTruthy();
    expect(screen.queryByText("Daily finance digest")).toBeNull();
    expect(screen.getByTestId("task-count").textContent).toBe("3 tasks");
    expect(badges()).toEqual(["Working", "Planning", "Done"]);

    // Only a task with a budget shows money, and none shows what the brain did not send.
    const tile = screen.getByText("Sort the morning mail").closest("a") as HTMLElement;
    expect(tile.textContent).toContain("€0.45");
    const bare = screen.getByText("Plan the launch email").closest("a") as HTMLElement;
    expect(bare.querySelector(".task-tile-meta")).toBeNull();
    expect(document.body.textContent).not.toContain("undefined");
  });

  it("opens a task by its own id and shows its events, subtasks and result", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt(`/tasks/${unlistedId}`);

    expect(await screen.findByRole("heading", { level: 1, name: "Only the detail knows me" })).toBeTruthy();
    expect(calls.some((call) => call.method === "GET" && call.path === `/api/tasks/${unlistedId}`)).toBe(true);
    cleanup();

    renderAt(`/tasks/${mailId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Sort the morning mail" })).toBeTruthy();
    const subtasks = await screen.findByRole("list", { name: "Subtasks" });
    expect(within(subtasks).getByText("Flag the invoices")).toBeTruthy();
    expect(within(subtasks).getByTestId("subtask-status").textContent).toBe("Done");
    expect(screen.getByText("Spent: €0.45")).toBeTruthy();
    expect(screen.getByText("Limit: €2.00")).toBeTruthy();

    act(() => {
      screen.getByRole("radio", { name: "All events" }).click();
    });
    const activity = screen.getByRole("region", { name: "Activity" });
    expect(within(activity).getByText("2 events")).toBeTruthy();
    expect(within(activity).getByText("Task opened")).toBeTruthy();
    expect(within(activity).getByText("Moved on to Working")).toBeTruthy();

    cleanup();
    renderAt(`/tasks/${doneId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Reconcile the accounts" })).toBeTruthy();
    act(() => {
      screen.getByRole("tab", { name: "Result" }).click();
    });
    expect(screen.getByTestId("task-result").textContent).toContain("The accounts reconcile.");
    expect(document.body.textContent).not.toContain("undefined");
  });

  it("says so when the brain has no such task", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderAt("/tasks/01990000-0000-7000-8000-0000000000ff");
    expect(await screen.findByText("This task is not in the workspace.")).toBeTruthy();
  });

  it("posts a message as kind chat and shows the new event", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt(`/tasks/${mailId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Sort the morning mail" })).toBeTruthy();

    const box = screen.getByLabelText("Message this task") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "Skip the drafts folder" } });
    act(() => {
      screen.getByRole("button", { name: "Send message" }).click();
    });

    const messages = await screen.findByRole("list", { name: "Messages" });
    expect(within(messages).getByText("Skip the drafts folder")).toBeTruthy();
    expect(box.value).toBe("");
    expect(calls).toContainEqual({
      method: "POST",
      path: `/api/tasks/${mailId}`,
      body: { kind: "chat", body: "Skip the drafts folder" },
    });
  });

  it("posts kind advance for Next phase and shows the task in its new phase", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt(`/tasks/${planId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Plan the launch email" })).toBeTruthy();
    expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Planning");

    act(() => {
      screen.getByRole("button", { name: "Next phase" }).click();
    });

    await waitFor(() => {
      expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Working");
    });
    expect(calls).toContainEqual({ method: "POST", path: `/api/tasks/${planId}`, body: { kind: "advance" } });
  });

  it("does not hide a 401 behind the sample tasks", async () => {
    vi.stubGlobal(
      "fetch",
      brain({ list: () => Response.json({ error: "unauthorised" }, { status: 401 }) }).fetcher,
    );
    const history = renderAt("/tasks");

    await waitFor(() => {
      expect(history.location.pathname).toBe("/login");
    });
    expect(screen.queryByText("Daily finance digest")).toBeNull();
  });

  it("does not hide a server error behind the sample tasks", async () => {
    vi.stubGlobal("fetch", brain({ list: () => Response.json({ error: "task" }, { status: 500 }) }).fetcher);
    renderAt("/tasks");

    expect((await screen.findByRole("alert")).textContent).toBe("task");
    expect(screen.queryByText("Daily finance digest")).toBeNull();
  });

  it("uses the sample tasks for the whole session when the brain answers 404", async () => {
    const { fetcher, calls } = brain({ list: () => new Response(null, { status: 404 }) });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/tasks/aster-mail");

    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Next phase" }).click();
    });
    await waitFor(() => {
      expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Planning");
    });
    expect(calls.filter((call) => call.path.startsWith("/api/tasks/"))).toEqual([]);
    expect(calls.filter((call) => call.method === "POST")).toEqual([]);
  });

  it("uses the sample tasks when there is no network", async () => {
    const fetcher = vi.fn<typeof fetch>(async (input) => {
      if (String(input).startsWith("/api/tasks")) throw new TypeError("Failed to fetch");
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/tasks");

    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    expect(screen.getByTestId("task-count").textContent).toBe("5 tasks");
  });

  it("shows the task a refused create left Failed, without a reload", async () => {
    const { fetcher, calls } = brain({ refuseCreate: true });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/tasks");
    expect(await screen.findByText("Sort the morning mail")).toBeTruthy();
    expect(screen.queryByText("Tidy the shared drive")).toBeNull();

    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    fireEvent.change(within(dialog).getByLabelText(/Goal/), { target: { value: "Tidy the shared drive" } });
    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });

    expect((await within(dialog).findByRole("alert")).textContent).toContain("needs a sandbox");
    const created = calls.findIndex((call) => call.method === "POST" && call.path === "/api/tasks");
    expect(created).toBeGreaterThan(-1);
    // The list is read again after the refusal, and it now holds the Failed task.
    await waitFor(() => {
      expect(calls.slice(created + 1).some((call) => call.method === "GET" && call.path === "/api/tasks")).toBe(true);
    });
    // The goal is also in the dialog's field, so look in the list only.
    const list = screen.getByRole("list", { name: "Tasks" });
    expect(await within(list).findByText("Tidy the shared drive")).toBeTruthy();
    expect(badges()).toEqual(["Working", "Planning", "Done", "Failed"]);
  });

  it.each([
    ["a server error", 500],
    ["a 401", 401],
  ])("asks the brain again after %s and does not stay on the sample tasks", async (_name, status) => {
    let asked = 0;
    const { fetcher, calls } = brain({
      list: () => {
        asked += 1;
        return asked === 1 ? Response.json({ error: "task" }, { status }) : Response.json([]);
      },
    });
    vi.stubGlobal("fetch", fetcher);
    const client = createBrainApi();

    await expect(client.listTasks()).rejects.toMatchObject({ status });
    expect(await client.listTasks()).toEqual([]);
    // The second call probed again (GET /api/tasks) and then read the list: three reads in all.
    expect(calls.filter((call) => call.method === "GET" && call.path === "/api/tasks")).toHaveLength(3);
  });
});
