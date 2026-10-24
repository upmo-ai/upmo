import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi } from "../../app/api";
import { AppProviders } from "../../theme";
import { ApprovalAnnouncer } from "./approval-card";

const placeholder = "The final result shows up here when the task is done.";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

function sidebar() {
  return screen.getByRole("navigation", { name: "Main" });
}

function chooseFilter(name: "All" | "Underway" | "Needs approval" | "Done" | "Failed") {
  const radio = within(screen.getByTestId("task-filter")).getByRole("radio", { name });
  act(() => {
    radio.click();
  });
}

function headerStatus() {
  return document.querySelector(".task-header .upmo-status-badge")?.textContent;
}

const approvalName = "Send the daily finance digest";

function approvalCard() {
  return screen.findByRole("group", { name: approvalName });
}

function taskList() {
  return screen.getByRole("list", { name: "Tasks" });
}

describe("tasks", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("filters the list and counts the visible tasks", async () => {
    renderAt("/tasks");
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    expect(screen.getByTestId("task-count").textContent).toBe("4 tasks");
    const alert = await approvalCard();
    expect(alert.textContent).toContain(approvalName);
    expect(alert.textContent).toContain(
      "Approve to continue, deny to let the agent find another way.",
    );
    expect(alert.compareDocumentPosition(taskList()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    chooseFilter("Underway");
    expect(within(taskList()).getByText("Mail triage")).toBeTruthy();
    expect(within(taskList()).queryByText("Daily finance digest")).toBeNull();
    expect(screen.getByTestId("task-count").textContent).toBe("1 task");

    chooseFilter("Needs approval");
    expect(within(taskList()).getByText("Daily finance digest")).toBeTruthy();
    expect(within(taskList()).queryByText("Mail triage")).toBeNull();
    expect(screen.getByTestId("task-count").textContent).toBe("1 task");

    chooseFilter("Done");
    expect(within(taskList()).getByText("File the receipts")).toBeTruthy();
    expect(screen.getByTestId("task-count").textContent).toBe("1 task");

    chooseFilter("Failed");
    expect(within(taskList()).getByText("Weekly close")).toBeTruthy();
    expect(within(taskList()).getByText("Failed")).toBeTruthy();
    expect(screen.getByTestId("task-count").textContent).toBe("1 task");

    chooseFilter("All");
    expect(within(taskList()).getAllByTestId("task-status").map((badge) => badge.textContent)).toEqual([
      "Needs approval",
      "Preparing",
      "Done",
      "Failed",
    ]);
    expect(screen.getByTestId("task-count").textContent).toBe("4 tasks");
  });

  it("walks a task through its phases to Done", async () => {
    renderAt("/tasks/aster-mail");
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(headerStatus()).toBe("Preparing");
    expect(screen.getByText("Sort the morning mail.")).toBeTruthy();
    expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Preparing");

    act(() => {
      screen.getByRole("tab", { name: "Result" }).click();
    });
    expect(screen.getByTestId("task-result").textContent).toBe(placeholder);

    act(() => {
      screen.getByRole("tab", { name: "Activity" }).click();
    });

    for (const phase of ["Planning", "Working", "Checking"]) {
      act(() => {
        screen.getByRole("button", { name: "Next phase" }).click();
      });
      await waitFor(() => {
        expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe(phase);
      });
    }

    act(() => {
      screen.getByRole("button", { name: "Next phase" }).click();
    });
    await waitFor(() => {
      expect(headerStatus()).toBe("Done");
    });
    expect(screen.queryByRole("listitem", { current: "step" })).toBeNull();

    act(() => {
      screen.getByRole("tab", { name: "Result" }).click();
    });
    expect(screen.getByTestId("task-result").textContent).toBe("Mail triage is finished.");
  });

  it("shows a nested subtask under its parent", async () => {
    renderAt("/tasks/aster-mail");
    const tree = await screen.findByRole("list", { name: "Subtasks" });
    const parent = within(tree).getByText("Read the mailbox").closest("li");
    expect(parent).toBeTruthy();
    expect(within(parent as HTMLElement).getByText("Flag the invoices")).toBeTruthy();
    expect(within(parent as HTMLElement).queryByText("Draft the replies")).toBeNull();
    expect(within(tree).getByText("Draft the replies")).toBeTruthy();
  });

  it("hides the subtask tree when a task has none", async () => {
    renderAt("/tasks/aster-done");
    expect(await screen.findByRole("heading", { level: 1, name: "File the receipts" })).toBeTruthy();
    expect(screen.queryByRole("list", { name: "Subtasks" })).toBeNull();
  });

  it("shows the budget with its cost, limit and pause threshold in words", async () => {
    renderAt("/tasks/aster-done");
    expect(await screen.findByRole("heading", { level: 4, name: "Budget" })).toBeTruthy();
    expect(screen.getByRole("progressbar", { name: "Budget used" })).toBeTruthy();
    expect(screen.getByText(/of .* · \d+k tokens · pauses at \d+%/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "See usage for this task" })).toBeTruthy();
  });

  it("approves a waiting task and then advances it", async () => {
    renderAt("/tasks/aster-digest");
    const heading = await screen.findByRole("heading", { level: 1, name: "Daily finance digest" });
    const alert = await approvalCard();
    expect(alert.compareDocumentPosition(heading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(headerStatus()).toBe("Needs approval");
    expect((screen.getByRole("button", { name: "Next phase" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Planning");
    expect(screen.getByRole("button", { name: "Notifications, 4 unread" })).toBeTruthy();

    act(() => {
      within(alert).getByRole("button", { name: "Approve" }).click();
    });

    await waitFor(() => {
      expect(screen.queryByRole("group", { name: approvalName })).toBeNull();
      expect(headerStatus()).toBe("Planning");
      expect(screen.getByRole("button", { name: "Notifications, 3 unread" })).toBeTruthy();
    });
    expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Planning");

    act(() => {
      screen.getByRole("button", { name: "Next phase" }).click();
    });
    await waitFor(() => {
      expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Working");
    });

    const current = within(sidebar())
      .getAllByRole("link")
      .filter((link) => link.getAttribute("aria-current") === "page");
    expect(current.map((link) => link.textContent?.trim())).toEqual(["Tasks"]);
  });

  it("deny keeps the task working", async () => {
    renderAt("/tasks/aster-digest");
    const alert = await approvalCard();
    act(() => {
      within(alert).getByRole("button", { name: "Deny" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("group", { name: approvalName })).toBeNull();
      expect(headerStatus()).toBe("Planning");
      expect(screen.getByRole("button", { name: "Notifications, 3 unread" })).toBeTruthy();
    });

    act(() => {
      screen.getByRole("button", { name: "Next phase" }).click();
    });
    await waitFor(() => {
      expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Working");
    });
  });

  it("returns copies and refuses the next phase while an approval is waiting", async () => {
    const api = createStubApi();
    const listed = await api.listTasks();
    listed[0]!.title = "Mutated";
    const again = await api.listTasks();
    expect(again[0]?.title).toBe("Daily finance digest");

    const stuck = await api.nextPhase("aster-digest");
    expect(stuck.status).toBe("Needs approval");
    expect(stuck.phase).toBe("Planning");
    stuck.status = "Done";
    const still = await api.listTasks();
    expect(still.find((task) => task.id === "aster-digest")?.status).toBe("Needs approval");

    const approved = await api.approvals.approve("aster-approval");
    expect(approved.state).toBe("approved");
    approved.action = "Mutated";
    const stored = (await api.approvals.list()).find((item) => item.id === "aster-approval");
    expect(stored?.state).toBe("approved");
    expect(stored?.action).toBe("Send the daily finance digest");

    const moved = await api.nextPhase("aster-digest");
    expect(moved.phase).toBe("Working");
    expect(moved.status).toBe("Working");

    const deniedApi = createStubApi();
    const denied = await deniedApi.approvals.deny("aster-approval");
    expect(denied.state).toBe("denied");
    const digest = (await deniedApi.listTasks()).find((task) => task.id === "aster-digest");
    expect(digest?.status).toBe("Working");
  });

  it("shows a failed task on the phase it failed in", async () => {
    renderAt("/tasks/aster-failed");
    expect(await screen.findByRole("heading", { level: 1, name: "Weekly close" })).toBeTruthy();
    expect(headerStatus()).toBe("Failed");
    const rail = screen.getByRole("list", { name: "Phases" });
    expect(within(rail).getByRole("listitem", { current: "step" }).textContent).toBe("Checking");
    expect((screen.getByRole("button", { name: "Next phase" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    expect(screen.getByRole("img", { name: "Run 4: Done" })).toBeTruthy();
  });

  it("marks a paused task and resumes it", async () => {
    renderAt("/tasks/aster-mail");
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Pause" }).click();
    });
    expect(headerStatus()).toBe("Paused");
    act(() => {
      screen.getByRole("button", { name: "Resume" }).click();
    });
    expect(headerStatus()).toBe("Preparing");
  });

  it("offers no Pause on a Done task", async () => {
    renderAt("/tasks/aster-done");
    expect(await screen.findByRole("heading", { level: 1, name: "File the receipts" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Pause" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
  });

  it("offers no Pause on a Failed task", async () => {
    renderAt("/tasks/aster-failed");
    expect(await screen.findByRole("heading", { level: 1, name: "Weekly close" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Pause" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
  });

  it("shows the spent share of the budget, not the pause threshold", async () => {
    renderAt("/tasks/aster-digest");
    expect(await screen.findByRole("heading", { level: 1, name: "Daily finance digest" })).toBeTruthy();
    expect(screen.getByText("42%")).toBeTruthy();
    expect(screen.getByRole("progressbar", { name: "Budget used" }).getAttribute("aria-valuenow")).toBe(
      "42",
    );
    expect(screen.getByText(/pauses at 90%/)).toBeTruthy();
  });

  it("lists the events with their tool calls", async () => {
    renderAt("/tasks/aster-digest");
    expect(await screen.findByRole("heading", { level: 1, name: "Daily finance digest" })).toBeTruthy();
    act(() => {
      screen.getByRole("radio", { name: "All events" }).click();
    });
    const stream = screen.getByRole("region", { name: "Activity" });
    expect(within(stream).getByText("10 events")).toBeTruthy();
    expect(within(stream).getByText("Plan written: 4 steps")).toBeTruthy();
    const trace = within(stream).getByRole("button", { name: /write_artifact/ });
    expect(trace.getAttribute("aria-expanded")).toBe("false");
    act(() => {
      trace.click();
    });
    expect(trace.getAttribute("aria-expanded")).toBe("true");
    expect(within(stream).getByText("Arguments")).toBeTruthy();
  });

  it("shows the artifacts and where the result goes", async () => {
    renderAt("/tasks/aster-digest");
    expect(await screen.findByRole("heading", { level: 1, name: "Daily finance digest" })).toBeTruthy();
    expect(screen.getByText("#finance on Slack")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Change delivery channel" })).toBeNull();
    act(() => {
      screen.getByRole("tab", { name: "Result" }).click();
    });
    const list = screen.getByRole("tablist", { name: "Artifacts" });
    act(() => {
      within(list).getByRole("tab", { name: /invoices-oct.csv/ }).click();
    });
    expect(screen.getByRole("columnheader", { name: "Vendor" })).toBeTruthy();
    expect(screen.getByRole("cell", { name: "Hetzner" })).toBeTruthy();
  });

  it("counts the working tasks in the shell", async () => {
    renderAt("/tasks");
    expect(await screen.findByText("1 task underway")).toBeTruthy();
  });

  it("announces an approval that arrives after the first render", () => {
    const approval = {
      id: "late",
      taskId: "aster-mail",
      title: "Approve the replies",
      action: "Send 3 replies",
      state: "pending" as const,
    };
    const { rerender } = render(<ApprovalAnnouncer pending={undefined} />, { wrapper: AppProviders });
    const region = screen.getByRole("status");
    expect(region.getAttribute("aria-live")).toBe("polite");
    rerender(<ApprovalAnnouncer pending={[]} />);
    expect(region.textContent).toBe("");
    rerender(<ApprovalAnnouncer pending={[approval]} />);
    expect(region.textContent).toBe("Approval needed: Send 3 replies");
  });

  it("stays quiet about approvals already there on first render", () => {
    const approval = {
      id: "early",
      taskId: "aster-mail",
      title: "Approve the replies",
      action: "Send 3 replies",
      state: "pending" as const,
    };
    render(<ApprovalAnnouncer pending={[approval]} />, { wrapper: AppProviders });
    expect(screen.getByRole("status").textContent).toBe("");
  });
});
