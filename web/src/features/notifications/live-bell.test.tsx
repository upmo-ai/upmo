import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createBrainApi } from "../../app/brain-api";
import { AppProviders } from "../../theme";

const waitingId = "01990000-0000-7000-8000-000000000001";
const doneId = "01990000-0000-7000-8000-000000000002";
const failedId = "01990000-0000-7000-8000-000000000003";
const approvalId = "01990000-0000-7000-8000-0000000000a1";
const questionId = "01990000-0000-7000-8000-0000000000c4";

// The approval the card names by its action; the bell names it by its task's goal.
const approvalAction = "Send the weekly summary to the finance list";
const approvalTitle = "Email the weekly summary";

type StoredTask = { id: string; title: string; status: string; phase: string; spent_cents: number; limit_cents: number };
type StoredApproval = { id: string; task_id: string; title: string; action: string; state: string };
type StoredNotice = { id: string; kind: string; title: string; task_id?: string };

type Call = { method: string; path: string; body: unknown };

/**
 * A brain that keeps its tasks, approvals and notifications in memory and
 * answers like the real routes. The notifications are stored newest first, in a
 * different order than the bell sorts them, so the sort itself is exercised.
 */
function brain(options: { refuseDecide?: boolean; refuseNotices?: () => Response } = {}) {
  const tasks: StoredTask[] = [
    { id: waitingId, title: approvalTitle, status: "Needs approval", phase: "Working", spent_cents: 0, limit_cents: 0 },
    { id: doneId, title: "Reconcile the accounts", status: "Done", phase: "Checking", spent_cents: 0, limit_cents: 0 },
  ];
  const approvals: StoredApproval[] = [
    { id: approvalId, task_id: waitingId, title: approvalTitle, action: approvalAction, state: "pending" },
  ];
  const notices: StoredNotice[] = [
    { id: "01990000-0000-7000-8000-0000000000c1", kind: "failed", title: "The weekly close failed", task_id: failedId },
    { id: "01990000-0000-7000-8000-0000000000c2", kind: "finished", title: "The accounts are reconciled", task_id: doneId },
    { id: "01990000-0000-7000-8000-0000000000c3", kind: "proposal", title: "Archive the old ledgers" },
    { id: questionId, kind: "question", title: "Where should the invoices go?" },
  ];
  const calls: Call[] = [];

  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    expect(init?.credentials).toBe("include");

    if (path === "/api/tasks" && method === "GET") {
      return Response.json(tasks);
    }
    const taskMatch = /^\/api\/tasks\/([^/]+)$/.exec(path);
    if (taskMatch) {
      const task = tasks.find((item) => item.id === taskMatch[1]);
      if (!task) return Response.json({ error: "no such task" }, { status: 404 });
      return Response.json(task);
    }
    if (path === "/api/approvals" && method === "GET") return Response.json(approvals);
    const decision = /^\/api\/approvals\/([^/]+)\/(approve|deny)$/.exec(path);
    if (decision && method === "POST") {
      if (options.refuseDecide) {
        return Response.json({ error: "the task is no longer waiting" }, { status: 409 });
      }
      const approval = approvals.find((item) => item.id === decision[1]);
      const task = approval && tasks.find((item) => item.id === approval.task_id);
      if (!approval || !task) return Response.json({ error: "no such approval" }, { status: 404 });
      if (approval.state !== "pending" || task.status !== "Needs approval") {
        return Response.json({ error: "the task is no longer waiting" }, { status: 409 });
      }
      approval.state = decision[2] === "approve" ? "approved" : "denied";
      task.status = "Working";
      return Response.json(approval);
    }
    if (path.startsWith("/api/notifications") && method === "GET") {
      return options.refuseNotices?.() ?? Response.json(notices);
    }
    const dismissed = /^\/api\/notifications\/([^/]+)$/.exec(path);
    if (dismissed && method === "DELETE") {
      const index = notices.findIndex((item) => item.id === dismissed[1]);
      if (index < 0) return Response.json({ error: "no such notification" }, { status: 404 });
      notices.splice(index, 1);
      return new Response(null, { status: 204 });
    }
    return new Response(null, { status: 404 });
  });
  return { fetcher, calls };
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

function headerStatus() {
  return document.querySelector(".task-header .upmo-status-badge")?.textContent;
}

async function openBell() {
  const bell = await screen.findByRole("button", { name: /^Notifications, \d+ unread$/ });
  act(() => {
    bell.click();
  });
  return screen.findByRole("dialog", { name: /^Notifications/ });
}

describe("approvals and notifications from the brain", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists the brain's notifications with their task links and the approval on top", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderAt("/tasks");

    const dialog = await openBell();
    const rows = within(dialog).getAllByRole("listitem", { hidden: true });
    expect(rows.map((row) => row.textContent)).toEqual([
      `Unread: ${approvalTitle}Needs your approvalApprove`,
      "Unread: Where should the invoices go?QuestionDismiss",
      "Unread: Archive the old ledgersProposalDismiss",
      "Unread: The accounts are reconciledFinishedDismiss",
      "Unread: The weekly close failedFailedDismiss",
    ]);
    // A notification about a task links to it; one about nothing stays plain text.
    const finished = within(rows[3]!).getByRole("link", { hidden: true });
    expect(finished.getAttribute("href")).toBe(`/tasks/${doneId}`);
    expect(within(rows[1]!).queryByRole("link", { hidden: true })).toBeNull();
  });

  it("approves from the task page, and the task works again after the refetch", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt(`/tasks/${waitingId}`);

    const card = await screen.findByRole("group", { name: approvalAction });
    expect(headerStatus()).toBe("Needs approval");

    act(() => {
      within(card).getByRole("button", { name: "Approve" }).click();
    });

    await waitFor(() => {
      expect(screen.queryByRole("group", { name: approvalAction })).toBeNull();
      expect(headerStatus()).toBe("Working");
    });
    // The bell drops the approval with it.
    expect(screen.getByRole("button", { name: "Notifications, 4 unread" })).toBeTruthy();
    expect(calls).toContainEqual({ method: "POST", path: `/api/approvals/${approvalId}/approve`, body: undefined });
  });

  it("approves from the bell, and the approval leaves the list with the task working again", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt(`/tasks/${waitingId}`);
    await screen.findByRole("group", { name: approvalAction });

    const dialog = await openBell();
    act(() => {
      within(dialog).getByRole("button", { name: "Approve", hidden: true }).click();
    });

    await waitFor(() => {
      expect(screen.queryByRole("group", { name: approvalAction })).toBeNull();
      expect(headerStatus()).toBe("Working");
    });
    expect(calls).toContainEqual({ method: "POST", path: `/api/approvals/${approvalId}/approve`, body: undefined });
    expect(screen.getByRole("button", { name: "Notifications, 4 unread" })).toBeTruthy();
  });

  it("denies from the task page and the task works again all the same", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt(`/tasks/${waitingId}`);

    const card = await screen.findByRole("group", { name: approvalAction });
    act(() => {
      within(card).getByRole("button", { name: "Deny" }).click();
    });

    await waitFor(() => {
      expect(screen.queryByRole("group", { name: approvalAction })).toBeNull();
      expect(headerStatus()).toBe("Working");
    });
    expect(calls).toContainEqual({ method: "POST", path: `/api/approvals/${approvalId}/deny`, body: undefined });
  });

  it("shows the brain's refusal when the task no longer waits, and keeps the card", async () => {
    vi.stubGlobal("fetch", brain({ refuseDecide: true }).fetcher);
    renderAt(`/tasks/${waitingId}`);

    const card = await screen.findByRole("group", { name: approvalAction });
    act(() => {
      within(card).getByRole("button", { name: "Approve" }).click();
    });

    // The shell banner carries the brain's own words, and the card stays.
    expect(await screen.findByText("the task is no longer waiting")).toBeTruthy();
    expect(screen.getByRole("group", { name: approvalAction })).toBeTruthy();
    expect(headerStatus()).toBe("Needs approval");
  });

  it("dismisses a notification with DELETE and the row leaves the list", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt("/tasks");

    const dialog = await openBell();
    const row = within(dialog)
      .getAllByRole("listitem", { hidden: true })
      .find((item) => item.textContent?.includes("Where should the invoices go?"));
    if (!row) throw new Error("no row for the question");
    act(() => {
      within(row).getByRole("button", { name: "Dismiss", hidden: true }).click();
    });

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Notifications, 4 unread" })).toBeTruthy();
    });
    expect(screen.queryByText("Where should the invoices go?")).toBeNull();
    expect(screen.getByText("Archive the old ledgers")).toBeTruthy();
    expect(calls).toContainEqual({ method: "DELETE", path: `/api/notifications/${questionId}`, body: undefined });
  });

  it("does not hide a 401 behind the sample notifications", async () => {
    vi.stubGlobal(
      "fetch",
      brain({ refuseNotices: () => Response.json({ error: "unauthorized" }, { status: 401 }) }).fetcher,
    );
    const history = renderAt("/tasks");

    await waitFor(() => {
      expect(history.location.pathname).toBe("/login");
    });
    expect(screen.queryByText("Question about the budget")).toBeNull();
  });

  it("keeps the sample notifications when the brain is absent", async () => {
    const calls: string[] = [];
    const fetcher = vi.fn<typeof fetch>(async (input) => {
      calls.push(String(input));
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/tasks");

    expect(await screen.findByRole("button", { name: "Notifications, 5 unread" })).toBeTruthy();
    expect(calls.some((path) => path.startsWith("/api/notifications") || path.startsWith("/api/approvals"))).toBe(
      false,
    );
  });
});
