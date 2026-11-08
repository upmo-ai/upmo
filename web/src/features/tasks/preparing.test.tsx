import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient, type Task } from "../../app/api";
import { AppProviders } from "../../theme";
import {
  PREPARING_POLL_MS,
  formatCpu,
  formatDuration,
  formatMemory,
  formatTimeout,
  preparingPollMs,
  totalMillis,
} from "./preparing";
import type { PrepareStep, PrepareStepName, PrepareStepState, Preparing } from "./types";

const SETTLE = { timeout: 5000 };

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
}

function headerStatus() {
  return document.querySelector(".task-header .upmo-status-badge")?.textContent;
}

function steps() {
  return screen.getByRole("list", { name: "Preparation steps" });
}

function stepNames() {
  return within(steps())
    .getAllByRole("listitem")
    .map((item) => item.querySelector(".task-prepare-step-name")?.textContent);
}

function stepItem(name: string) {
  const item = within(steps())
    .getAllByRole("listitem")
    .find((candidate) => candidate.querySelector(".task-prepare-step-name")?.textContent === name);
  if (!item) throw new Error(`no step named ${name}`);
  return item;
}

function stepState(name: string) {
  return stepItem(name).querySelector(".task-prepare-step-state")?.textContent;
}

function runtime() {
  const section = screen.getByRole("region", { name: "Runtime settings" });
  const rows = Array.from(section.querySelectorAll(".task-runtime-row"));
  return Object.fromEntries(rows.map((row) => [row.querySelector("dt")?.textContent, row.querySelector("dd")?.textContent]));
}

async function open(id: string, title: string) {
  renderAt(`/tasks/${id}`);
  await screen.findByRole("heading", { level: 1, name: title });
}

/** The stub client with the open task's payload replaced on every read. */
function clientServing(change: (tasks: Task[]) => Task[]): ApiClient {
  const stub = createStubApi();
  return {
    ...stub,
    listTasks: async () => change(await stub.listTasks()),
    getTask: async (taskId) => {
      const task = change(await stub.listTasks()).find((item) => item.id === taskId);
      if (!task) throw new Error("task missing");
      return task;
    },
  };
}

function withPreparing(tasks: Task[], id: string, change: (preparing: Preparing) => Preparing): Task[] {
  return tasks.map((task) =>
    task.id === id && task.preparing ? { ...task, preparing: change(task.preparing) } : task,
  );
}

function withStates(preparing: Preparing, states: PrepareStepState[]): Preparing {
  return {
    ...preparing,
    steps: preparing.steps.map((step, index) => ({ ...step, state: states[index] ?? step.state })),
  };
}

describe("preparing block", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-64 lists clone, toolchain and probe in order with what each one is doing", async () => {
    await open("aster-mail", "Mail triage");
    expect(screen.getByRole("heading", { level: 2, name: "Preparing" })).toBeTruthy();
    expect(stepNames()).toEqual(["Clone", "Toolchain", "Probe"]);
    expect(stepState("Clone")).toBe("Done");
    expect(stepState("Toolchain")).toBe("Running");
    expect(stepState("Probe")).toBe("Waiting");
    expect(within(steps()).getByText("Cloned upmo-ai/mail-rules")).toBeTruthy();
    expect(within(steps()).getByText("3.4 s")).toBeTruthy();
  });

  it("UPM-64 announces each step once and tells a screen reader when a step changes", async () => {
    await open("aster-mail", "Mail triage");
    // The dot is a visual mark: the name and state in text are what a screen reader reads.
    expect(within(steps()).queryAllByRole("img")).toHaveLength(0);
    expect(steps().getAttribute("aria-live")).toBe("polite");
    const clone = stepItem("Clone");
    expect(clone.querySelector(".task-prepare-step-state")?.getAttribute("aria-hidden")).toBeNull();
    expect(clone.querySelector("[aria-hidden='true'] .upmo-status-dot")).not.toBeNull();
    expect(within(clone).getAllByText("Clone")).toHaveLength(1);
  });

  it("UPM-64 shows the runtime settings the stub reports for the task", async () => {
    await open("aster-mail", "Mail triage");
    expect(runtime()).toEqual({
      Tier: "Container",
      Repository: "upmo-ai/mail-rules",
      Toolchain: "python 3.13",
      CPU: "1 CPU",
      Memory: "512 MB",
      Timeout: "10 min",
    });
  });

  it("UPM-64 shows a failed probe with its message and keeps the steps in view", async () => {
    await open("aster-probe", "Fix the flaky login test");
    expect(headerStatus()).toBe("Failed");
    expect(stepState("Clone")).toBe("Done");
    expect(stepState("Toolchain")).toBe("Done");
    expect(stepState("Probe")).toBe("Failed");
    const alert = within(steps()).getByRole("alert");
    expect(alert.textContent).toContain("The work directory is not writable.");
    expect(runtime()).toMatchObject({ Tier: "Kata", Repository: "upmo-ai/upmo", Memory: "4 GB", Timeout: "30 min" });
    expect(screen.queryByTestId("preparing-summary")).toBeNull();
  });

  it("UPM-64 collapses to a summary once the task is past Preparing", async () => {
    await open("aster-digest", "Daily finance digest");
    expect(screen.queryByRole("list", { name: "Preparation steps" })).toBeNull();
    expect(screen.getByRole("heading", { level: 2, name: "Prepared" })).toBeTruthy();
    expect(screen.getByTestId("preparing-summary").textContent).toBe("The workspace was ready in 4.0 s.");
    expect(runtime()).toMatchObject({ Tier: "gVisor", Repository: "upmo-ai/finance-digest", Memory: "1 GB" });
  });

  it("UPM-64 says so when a task has no repository, toolchain or limits", async () => {
    await open("aster-done", "File the receipts");
    expect(runtime()).toEqual({ Tier: "Container", Repository: "None", Toolchain: "None", Limits: "Not set" });
  });

  it("UPM-64 collapses the steps when the task moves on from Preparing", async () => {
    await open("aster-mail", "Mail triage");
    expect(screen.getByRole("list", { name: "Preparation steps" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Next phase" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("list", { name: "Preparation steps" })).toBeNull();
    });
    expect(screen.getByRole("heading", { level: 2, name: "Prepared" })).toBeTruthy();
    // Clone took 3.4 s and the two steps that had not finished take a second each.
    expect(screen.getByTestId("preparing-summary").textContent).toBe("The workspace was ready in 5.4 s.");
  });

  it("UPM-64 gives every step a start and finish when the stub moves a task past Preparing", async () => {
    const stub = createStubApi();
    await stub.nextPhase("aster-mail");
    const task = (await stub.listTasks()).find((item) => item.id === "aster-mail");
    expect(task?.phase).toBe("Planning");
    for (const step of task?.preparing?.steps ?? []) {
      expect(step.state).toBe("done");
      expect(step.started_at).not.toBeNull();
      expect(step.finished_at).not.toBeNull();
    }
    const clone = task?.preparing?.steps[0];
    expect(clone?.finished_at).toBe("2026-11-05T08:15:03.400Z");
  });

  it("UPM-64 follows the payload as the steps progress, without a click", async () => {
    // What the server says now. The test moves it on; the page has to notice by itself.
    let stage: "toolchain" | "probe" | "planning" = "toolchain";
    const client = clientServing((tasks) => {
      if (stage === "probe") {
        return withPreparing(tasks, "aster-mail", (p) => withStates(p, ["done", "done", "running"]));
      }
      if (stage === "planning") {
        return tasks.map((task) =>
          task.id === "aster-mail" && task.preparing
            ? { ...task, phase: "Planning" as const, preparing: withStates(task.preparing, ["done", "done", "done"]) }
            : task,
        );
      }
      return tasks;
    });
    renderAt("/tasks/aster-mail", client);
    await screen.findByRole("heading", { level: 1, name: "Mail triage" });
    expect(stepState("Toolchain")).toBe("Running");

    stage = "probe";
    await waitFor(() => {
      expect(stepState("Toolchain")).toBe("Done");
    }, SETTLE);
    expect(stepState("Probe")).toBe("Running");

    stage = "planning";
    await waitFor(() => {
      expect(screen.queryByRole("list", { name: "Preparation steps" })).toBeNull();
    }, SETTLE);
    expect(screen.getByTestId("preparing-summary")).toBeTruthy();
    expect(headerStatus()).toBe("Planning");
  });

  it("UPM-64 does not put a time on the summary when the payload has unfinished steps", async () => {
    const client = clientServing((tasks) =>
      tasks.map((task) => (task.id === "aster-mail" ? { ...task, phase: "Planning" as const } : task)),
    );
    renderAt("/tasks/aster-mail", client);
    await screen.findByRole("heading", { level: 1, name: "Mail triage" });
    expect(screen.getByTestId("preparing-summary").textContent).toBe("The workspace was ready.");
  });

  it("UPM-64 renders nothing when the payload carries no preparing block", async () => {
    const client = clientServing((tasks) => tasks.map((task) => ({ ...task, preparing: undefined })));
    renderAt("/tasks/aster-mail", client);
    await screen.findByRole("heading", { level: 1, name: "Mail triage" });
    expect(screen.queryByRole("list", { name: "Preparation steps" })).toBeNull();
    expect(screen.queryByRole("heading", { level: 2, name: "Preparing" })).toBeNull();
    expect(screen.queryByRole("heading", { level: 2, name: "Prepared" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Runtime settings" })).toBeNull();
  });

  it("UPM-64 invents no steps for a task the stub creates", async () => {
    const stub = createStubApi();
    const created = await stub.createTask("Tidy the shared drive");
    const task = (await stub.listTasks()).find((item) => item.id === created.id);
    expect(task?.preparing).toBeUndefined();
  });

  it("UPM-64 reads the task again only while it is working and Preparing", async () => {
    const tasks = await createStubApi().listTasks();
    expect(preparingPollMs(tasks, "aster-mail")).toBe(PREPARING_POLL_MS);
    expect(preparingPollMs(tasks, "aster-probe")).toBe(false);
    expect(preparingPollMs(tasks, "aster-digest")).toBe(false);
    expect(preparingPollMs(tasks, "aster-done")).toBe(false);
    expect(preparingPollMs(tasks, "missing")).toBe(false);
    expect(preparingPollMs(undefined, "aster-mail")).toBe(false);
  });

  it("UPM-64 carries a rounded duration into the next unit instead of showing 60", () => {
    expect(formatDuration(999.6)).toBe("1.0 s");
    expect(formatDuration(59_940)).toBe("59.9 s");
    expect(formatDuration(59_960)).toBe("1 min");
    expect(formatDuration(119_600)).toBe("2 min");
    expect(formatDuration(179_700)).toBe("3 min");
    expect(formatDuration(119_400)).toBe("1 min 59 s");
  });

  it("UPM-64 reports no total while a step has not finished", () => {
    const stamp = (second: number) => `2026-11-05T08:00:${String(second).padStart(2, "0")}.000Z`;
    const step = (name: PrepareStepName, from: number | null, to: number | null): PrepareStep => ({
      name,
      state: to === null ? "running" : "done",
      message: null,
      started_at: from === null ? null : stamp(from),
      finished_at: to === null ? null : stamp(to),
    });
    const runtimeOnly: Preparing["runtime"] = { tier: "container", repo: null, toolchain: null, limits: null };
    const partial: Preparing = {
      steps: [step("clone", 0, 3), step("toolchain", 3, null), step("probe", null, null)],
      runtime: runtimeOnly,
    };
    expect(totalMillis(partial)).toBeNull();
    const finished: Preparing = {
      steps: [step("clone", 0, 3), step("toolchain", 3, 4), step("probe", 4, 6)],
      runtime: runtimeOnly,
    };
    expect(totalMillis(finished)).toBe(6000);
  });

  it("UPM-64 formats durations and limits the way the block reads them", () => {
    expect(formatDuration(420)).toBe("420 ms");
    expect(formatDuration(4000)).toBe("4.0 s");
    expect(formatDuration(65_000)).toBe("1 min 5 s");
    expect(formatDuration(120_000)).toBe("2 min");
    expect(formatCpu(1)).toBe("1 CPU");
    expect(formatCpu(2)).toBe("2 CPUs");
    expect(formatMemory(512)).toBe("512 MB");
    expect(formatMemory(2048)).toBe("2 GB");
    expect(formatMemory(1536)).toBe("1536 MB");
    expect(formatTimeout(45)).toBe("45 s");
    expect(formatTimeout(900)).toBe("15 min");
  });
});
