import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { BrainError, createStubApi, type ApiClient, type Task } from "../../app/api";
import { AppProviders } from "../../theme";
import { ApprovalAnnouncer } from "./approval-card";

const placeholder = "The final result shows up here when the task is done.";

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
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

  it("UPM-199 walks a task through its phases and stops at Checking", async () => {
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

    const nextPhase = screen.getByRole("button", { name: "Next phase" }) as HTMLButtonElement;
    expect(nextPhase.disabled).toBe(true);
    expect(headerStatus()).toBe("Checking");
    expect(screen.getByTestId("next-phase-note").textContent).toContain("Checking is the last step");
    expect(screen.getByTestId("next-phase-note").textContent).toContain("marks the task Done");

    act(() => {
      screen.getByRole("tab", { name: "Result" }).click();
    });
    expect(screen.getByTestId("task-result").textContent).toBe(placeholder);
  });

  it("UPM-199 gives no next-phase explanation before Checking", async () => {
    renderAt("/tasks/aster-mail");
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    expect((screen.getByRole("button", { name: "Next phase" }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect(screen.queryByTestId("next-phase-note")).toBeNull();
  });

  it("UPM-199 the stub refuses to advance a task out of Checking", async () => {
    const api = createStubApi();
    for (const phase of ["Planning", "Working", "Checking"]) {
      expect((await api.nextPhase("aster-mail")).phase).toBe(phase);
    }
    const again = await api.nextPhase("aster-mail");
    expect(again.phase).toBe("Checking");
    expect(again.status).toBe("Working");
  });

  it("UPM-200 shows a finished task's result and its recorded events", async () => {
    renderAt("/tasks/aster-done");
    expect(await screen.findByRole("heading", { level: 1, name: "File the receipts" })).toBeTruthy();
    act(() => {
      screen.getByRole("tab", { name: "Result" }).click();
    });
    expect(screen.getByTestId("task-result").textContent).toBe("The receipts are filed.");
    act(() => {
      screen.getByRole("tab", { name: "Activity" }).click();
    });
    act(() => {
      screen.getByRole("radio", { name: "All events" }).click();
    });
    expect(within(screen.getByRole("region", { name: "Activity" })).getByText("6 events")).toBeTruthy();
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
    expect(screen.getByRole("button", { name: "Notifications, 5 unread" })).toBeTruthy();

    act(() => {
      within(alert).getByRole("button", { name: "Approve" }).click();
    });

    await waitFor(() => {
      expect(screen.queryByRole("group", { name: approvalName })).toBeNull();
      expect(headerStatus()).toBe("Planning");
      expect(screen.getByRole("button", { name: "Notifications, 4 unread" })).toBeTruthy();
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
      expect(screen.getByRole("button", { name: "Notifications, 4 unread" })).toBeTruthy();
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

  it("UPM-199 creates a task from the New task dialog and opens it", async () => {
    renderAt("/tasks");
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    const goal = within(dialog).getByLabelText(/Goal/);
    expect(within(dialog).getByText("0 of 200 characters")).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: "Create task" }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(goal, { target: { value: "Tidy the shared drive" } });
    expect(within(dialog).getByText("21 of 200 characters")).toBeTruthy();
    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });

    expect(await screen.findByRole("heading", { level: 1, name: "Tidy the shared drive" })).toBeTruthy();
    expect(screen.queryByRole("dialog", { name: "New task" })).toBeNull();
    expect(headerStatus()).toBe("Preparing");
    expect(screen.getByRole("listitem", { current: "step" }).textContent).toBe("Preparing");
  });

  it("UPM-199 refuses a goal over 200 characters and an empty one", async () => {
    const stub = createStubApi();
    const createTask = vi.fn<ApiClient["createTask"]>(stub.createTask);
    renderAt("/tasks", { ...stub, createTask });
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    const goal = within(dialog).getByLabelText(/Goal/);
    const create = within(dialog).getByRole("button", { name: "Create task" }) as HTMLButtonElement;

    fireEvent.change(goal, { target: { value: "x".repeat(201) } });
    expect(within(dialog).getByText("201 of 200 characters")).toBeTruthy();
    expect(within(dialog).getByText("Keep the goal to 200 characters or fewer.")).toBeTruthy();
    expect(create.disabled).toBe(true);

    fireEvent.change(goal, { target: { value: "x".repeat(200) } });
    expect(within(dialog).queryByText("Keep the goal to 200 characters or fewer.")).toBeNull();
    expect(create.disabled).toBe(false);

    fireEvent.change(goal, { target: { value: "   " } });
    expect(within(dialog).getByText("Describe the goal in 1 to 200 characters.")).toBeTruthy();
    expect(create.disabled).toBe(true);
    expect(createTask).not.toHaveBeenCalled();
  });

  it("UPM-199 keeps the repository behind Advanced and checks its format", async () => {
    const stub = createStubApi();
    const createTask = vi.fn<ApiClient["createTask"]>(stub.createTask);
    renderAt("/tasks", { ...stub, createTask });
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    expect(within(dialog).queryByLabelText("Repository")).toBeNull();
    const advanced = within(dialog).getByRole("button", { name: "Advanced" });
    expect(advanced.getAttribute("aria-expanded")).toBe("false");
    act(() => {
      advanced.click();
    });
    expect(advanced.getAttribute("aria-expanded")).toBe("true");
    const repo = within(dialog).getByLabelText("Repository");

    fireEvent.change(within(dialog).getByLabelText(/Goal/), { target: { value: "Fix the build" } });
    fireEvent.change(repo, { target: { value: "not a repo" } });
    expect(within(dialog).getByText("Use owner/name or an https URL.")).toBeTruthy();
    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });
    expect(createTask).not.toHaveBeenCalled();

    fireEvent.change(repo, { target: { value: "http://example.com/org/name" } });
    expect(within(dialog).getByText("Use owner/name or an https URL.")).toBeTruthy();

    fireEvent.change(repo, { target: { value: "upmo-ai/upmo" } });
    expect(within(dialog).queryByText("Use owner/name or an https URL.")).toBeNull();
    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });
    await waitFor(() => {
      expect(createTask).toHaveBeenCalledWith("Fix the build", "upmo-ai/upmo");
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Fix the build" })).toBeTruthy();
    expect(screen.getByText("upmo-ai/upmo")).toBeTruthy();
  });

  it("UPM-199 accepts an https repository URL and sends it to createTask", async () => {
    const stub = createStubApi();
    const createTask = vi.fn<ApiClient["createTask"]>(stub.createTask);
    renderAt("/tasks", { ...stub, createTask });
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    fireEvent.change(within(dialog).getByLabelText(/Goal/), { target: { value: "Review the repo" } });
    act(() => {
      within(dialog).getByRole("button", { name: "Advanced" }).click();
    });
    fireEvent.change(within(dialog).getByLabelText("Repository"), {
      target: { value: "  https://github.com/upmo-ai/upmo  " },
    });
    expect(within(dialog).queryByText("Use owner/name or an https URL.")).toBeNull();
    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });
    await waitFor(() => {
      expect(createTask).toHaveBeenCalledWith("Review the repo", "https://github.com/upmo-ai/upmo");
    });
    expect(createTask).toHaveBeenCalledTimes(1);
    expect(await screen.findByRole("heading", { level: 1, name: "Review the repo" })).toBeTruthy();
  });

  it("UPM-199 moves focus to the repository field when an invalid repository opens Advanced", async () => {
    const stub = createStubApi();
    const createTask = vi.fn<ApiClient["createTask"]>(stub.createTask);
    renderAt("/tasks", { ...stub, createTask });
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    const advanced = within(dialog).getByRole("button", { name: "Advanced" });
    act(() => {
      advanced.click();
    });
    fireEvent.change(within(dialog).getByLabelText("Repository"), { target: { value: "not a repo" } });
    act(() => {
      advanced.click();
    });
    expect(within(dialog).queryByLabelText("Repository")).toBeNull();
    fireEvent.change(within(dialog).getByLabelText(/Goal/), { target: { value: "Fix the build" } });

    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });
    const repo = within(dialog).getByLabelText("Repository");
    expect(advanced.getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement).toBe(repo);
    expect(createTask).not.toHaveBeenCalled();
  });

  it("UPM-199 sends no repository when none is given", async () => {
    const stub = createStubApi();
    const createTask = vi.fn<ApiClient["createTask"]>(stub.createTask);
    renderAt("/tasks", { ...stub, createTask });
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    fireEvent.change(within(dialog).getByLabelText(/Goal/), { target: { value: "  Plain goal  " } });
    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });
    await waitFor(() => {
      expect(createTask).toHaveBeenCalledWith("Plain goal", undefined);
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Plain goal" })).toBeTruthy();
    expect(createTask.mock.calls[0]).toHaveLength(2);
  });

  it("UPM-199 explains a 503 when the sandbox is not configured and stays in the dialog", async () => {
    const stub = createStubApi();
    const createTask = vi.fn<ApiClient["createTask"]>(async () => {
      throw new BrainError(503, "sandbox_unavailable");
    });
    renderAt("/tasks", { ...stub, createTask });
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "New task" });
    fireEvent.change(within(dialog).getByLabelText(/Goal/), { target: { value: "Fix the build" } });
    act(() => {
      within(dialog).getByRole("button", { name: "Create task" }).click();
    });
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("needs a sandbox");
    expect(alert.textContent).not.toContain("503");
    expect(screen.getByRole("dialog", { name: "New task" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 1, name: "Tasks" })).toBeTruthy();
  });

  it("UPM-199 opens the New task dialog from the empty state", async () => {
    const stub = createStubApi();
    renderAt("/tasks", {
      ...stub,
      async listTasks() {
        return [];
      },
    });
    expect(await screen.findByRole("heading", { level: 3, name: "Your first task" })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "New task" })).toHaveLength(1);
    expect(screen.queryByRole("link", { name: "Create task" })).toBeNull();
    act(() => {
      screen.getByRole("button", { name: "New task" }).click();
    });
    expect(await screen.findByRole("dialog", { name: "New task" })).toBeTruthy();
  });

  it("UPM-199 posts a message to the task and shows it in the activity", async () => {
    const stub = createStubApi();
    const postTaskMessage = vi.fn<ApiClient["postTaskMessage"]>(stub.postTaskMessage);
    renderAt("/tasks/aster-mail", { ...stub, postTaskMessage });
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    const box = screen.getByLabelText("Message this task") as HTMLTextAreaElement;
    const send = screen.getByRole("button", { name: "Send message" });
    expect(send.getAttribute("aria-disabled")).toBe("true");
    expect(screen.queryByRole("list", { name: "Messages" })).toBeNull();

    fireEvent.change(box, { target: { value: "  Skip the newsletters  " } });
    expect(send.getAttribute("aria-disabled")).toBeNull();
    act(() => {
      send.click();
    });

    const list = await screen.findByRole("list", { name: "Messages" });
    expect(postTaskMessage).toHaveBeenCalledWith("aster-mail", "Skip the newsletters");
    expect(within(list).getByText("Skip the newsletters")).toBeTruthy();
    await waitFor(() => {
      expect(box.value).toBe("");
    });

    act(() => {
      screen.getByRole("radio", { name: "All events" }).click();
    });
    const stream = screen.getByRole("region", { name: "Activity" });
    expect(within(stream).getByText("3 events")).toBeTruthy();
    expect(within(stream).getByText("You: Skip the newsletters")).toBeTruthy();
  });

  it("UPM-199 sends the message with Enter and keeps Shift+Enter for a new line", async () => {
    const stub = createStubApi();
    const postTaskMessage = vi.fn<ApiClient["postTaskMessage"]>(stub.postTaskMessage);
    renderAt("/tasks/aster-mail", { ...stub, postTaskMessage });
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    const box = screen.getByLabelText("Message this task");
    fireEvent.change(box, { target: { value: "First line" } });
    fireEvent.keyDown(box, { key: "Enter", shiftKey: true });
    expect(postTaskMessage).not.toHaveBeenCalled();
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => {
      expect(postTaskMessage).toHaveBeenCalledWith("aster-mail", "First line");
    });
  });

  it("UPM-199 refuses a message over 4000 characters", async () => {
    const stub = createStubApi();
    const postTaskMessage = vi.fn<ApiClient["postTaskMessage"]>(stub.postTaskMessage);
    renderAt("/tasks/aster-mail", { ...stub, postTaskMessage });
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    const box = screen.getByLabelText("Message this task");
    const send = screen.getByRole("button", { name: "Send message" });

    fireEvent.change(box, { target: { value: "y".repeat(4001) } });
    expect(send.getAttribute("aria-disabled")).toBe("true");
    expect(screen.getByRole("alert").textContent).toContain("4000 characters");
    fireEvent.keyDown(box, { key: "Enter" });
    expect(postTaskMessage).not.toHaveBeenCalled();

    fireEvent.change(box, { target: { value: "y".repeat(4000) } });
    expect(send.getAttribute("aria-disabled")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    const stubbed = await stub.postTaskMessage("aster-mail", "y".repeat(4000));
    expect(stubbed.message).toHaveLength(4000);
    await expect(stub.postTaskMessage("aster-mail", "y".repeat(4001))).rejects.toThrow("4000");
  });

  it("UPM-199 does not send the message when Enter confirms an IME candidate", async () => {
    const stub = createStubApi();
    const postTaskMessage = vi.fn<ApiClient["postTaskMessage"]>(stub.postTaskMessage);
    renderAt("/tasks/aster-mail", { ...stub, postTaskMessage });
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    const box = screen.getByLabelText("Message this task") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "にほん" } });
    fireEvent.keyDown(box, { key: "Enter", isComposing: true });
    expect(postTaskMessage).not.toHaveBeenCalled();
    expect(box.value).toBe("にほん");
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => {
      expect(postTaskMessage).toHaveBeenCalledWith("aster-mail", "にほん");
    });
  });

  it("UPM-199 keeps what is typed while a message is being posted", async () => {
    const stub = createStubApi();
    let release: () => void = () => {};
    const postTaskMessage = vi.fn<ApiClient["postTaskMessage"]>(async (taskId, body) => {
      await new Promise<void>((resolve) => {
        release = resolve;
      });
      return stub.postTaskMessage(taskId, body);
    });
    renderAt("/tasks/aster-mail", { ...stub, postTaskMessage });
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    const box = screen.getByLabelText("Message this task") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "First" } });
    act(() => {
      screen.getByRole("button", { name: "Send message" }).click();
    });
    await waitFor(() => {
      expect(postTaskMessage).toHaveBeenCalledWith("aster-mail", "First");
    });
    fireEvent.change(box, { target: { value: "Second" } });
    act(() => {
      release();
    });
    expect(await screen.findByRole("list", { name: "Messages" })).toBeTruthy();
    expect(box.value).toBe("Second");
  });

  it("UPM-199 puts the unsent message back in front of a new draft when the post fails", async () => {
    const stub = createStubApi();
    let fail: (error: Error) => void = () => {};
    const postTaskMessage = vi.fn<ApiClient["postTaskMessage"]>(
      () =>
        new Promise((_resolve, reject) => {
          fail = reject;
        }),
    );
    renderAt("/tasks/aster-mail", { ...stub, postTaskMessage });
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    const box = screen.getByLabelText("Message this task") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "First" } });
    act(() => {
      screen.getByRole("button", { name: "Send message" }).click();
    });
    await waitFor(() => {
      expect(postTaskMessage).toHaveBeenCalledTimes(1);
    });
    expect(box.value).toBe("");
    fireEvent.change(box, { target: { value: "Second" } });
    act(() => {
      fail(new Error("The task is not taking messages."));
    });
    expect((await screen.findByRole("alert")).textContent).toBe("The task is not taking messages.");
    expect(box.value).toBe("First\nSecond");
  });

  it("UPM-199 keeps the draft and says so when the message cannot be posted", async () => {
    const stub = createStubApi();
    const postTaskMessage = vi.fn<ApiClient["postTaskMessage"]>(async () => {
      throw new Error("The task is not taking messages.");
    });
    renderAt("/tasks/aster-mail", { ...stub, postTaskMessage });
    expect(await screen.findByRole("heading", { level: 1, name: "Mail triage" })).toBeTruthy();
    const box = screen.getByLabelText("Message this task") as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "Hello" } });
    act(() => {
      screen.getByRole("button", { name: "Send message" }).click();
    });
    expect((await screen.findByRole("alert")).textContent).toBe("The task is not taking messages.");
    expect(box.value).toBe("Hello");
    expect(screen.queryByRole("list", { name: "Messages" })).toBeNull();
  });

  it("UPM-200 invents no figures for a task that has reported none", async () => {
    const bare: Task = {
      id: "bare",
      title: "Bare task",
      status: "Working",
      phase: "Planning",
      result: "",
      detail: "A task that has reported nothing yet.",
    };
    const stub = createStubApi();
    renderAt("/tasks/bare", {
      ...stub,
      async listTasks() {
        return [bare];
      },
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Bare task" })).toBeTruthy();
    const header = document.querySelector(".task-header") as HTMLElement;
    for (const invented of ["Run 14", "gpt-5.2", "started 08:00", "€2.10", "Upmo ·"]) {
      expect(header.textContent).not.toContain(invented);
    }
    expect(header.querySelector(".upmo-task-header__cost")).toBeNull();

    expect(screen.getByText("Spent: Unknown")).toBeTruthy();
    expect(screen.getByText("Limit: Unknown")).toBeTruthy();
    expect(screen.queryByRole("progressbar", { name: "Budget used" })).toBeNull();
    expect(screen.queryByRole("heading", { level: 4, name: "Delivery" })).toBeNull();
    expect(screen.queryByRole("heading", { level: 4, name: "Sources" })).toBeNull();
    expect(screen.queryByText("#finance on Slack")).toBeNull();
    expect(document.body.textContent).not.toContain("Finance mailbox");
    expect(document.body.textContent).not.toContain("38k tokens");

    act(() => {
      screen.getByRole("radio", { name: "All events" }).click();
    });
    expect(screen.getByText("No events yet.")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Activity" })).toBeNull();
    expect(document.body.textContent).not.toContain("Task initialized by user");
  });

  it("UPM-200 names a missing delivery time Unknown instead of inventing one", async () => {
    const partial: Task = {
      id: "partial",
      title: "Partial task",
      status: "Working",
      phase: "Working",
      result: "",
      detail: "Reports a channel only.",
      deliveryChannel: "#ops on Slack",
      sources: ["Ops mailbox"],
    };
    const stub = createStubApi();
    renderAt("/tasks/partial", {
      ...stub,
      async listTasks() {
        return [partial];
      },
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Partial task" })).toBeTruthy();
    expect(screen.getByText("#ops on Slack")).toBeTruthy();
    expect(screen.getByText("Unknown")).toBeTruthy();
    expect(screen.queryByText("After the task is done")).toBeNull();
    expect(screen.getByText("Ops mailbox")).toBeTruthy();
    expect(screen.queryByText("Finance mailbox")).toBeNull();
  });

  it("UPM-200 leaves a task tile without cost, tokens or model figures", async () => {
    const stub = createStubApi();
    renderAt("/tasks", {
      ...stub,
      async listTasks() {
        return [
          { id: "bare", title: "Bare task", status: "Working", phase: "Preparing", result: "", detail: "" },
        ];
      },
    });
    const tile = (await screen.findByText("Bare task")).closest("a") as HTMLElement;
    expect(tile.querySelector(".task-tile-meta")).toBeNull();
    expect(tile.textContent).not.toContain("€0.00");
    expect(tile.textContent).not.toContain("0 tokens");
  });

  it("UPM-200 keeps the fixtures rich so the demo tasks still show their figures", async () => {
    renderAt("/tasks/aster-digest");
    expect(await screen.findByRole("heading", { level: 1, name: "Daily finance digest" })).toBeTruthy();
    const header = document.querySelector(".task-header") as HTMLElement;
    expect(header.textContent).toContain("Run 14 · Upmo · gpt-5.2 · started 08:00 · €2.10");
    expect(screen.getByText(/€2.10 of €5.00 · 38k tokens · pauses at 90%/)).toBeTruthy();
    expect(screen.getByText("After the task is done")).toBeTruthy();
    expect(screen.getByText("Finance mailbox")).toBeTruthy();
    cleanup();
    renderAt("/tasks");
    expect(await screen.findByText("Daily finance digest")).toBeTruthy();
    expect(taskList().textContent).toContain("€2.10");
    expect(taskList().textContent).toContain("38k tokens");
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
