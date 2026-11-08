import { describe, expect, it } from "vitest";

import { toApproval, toNotification, toTask, causeSentence } from "./brain-api";

const head = {
  id: "01990000-0000-7000-8000-000000000001",
  title: "Sort the morning mail",
  status: "Working",
  phase: "Planning",
  spent_cents: 210,
  limit_cents: 500,
  updated_at: "2026-11-08T09:00:00Z",
};

describe("toTask", () => {
  it("passes status and phase through and writes the money as the stub does", () => {
    expect(toTask(head)).toEqual({
      id: head.id,
      title: head.title,
      status: "Working",
      phase: "Planning",
      result: "",
      detail: "",
      cost: "€2.10",
      spendLimit: "€5.00",
      spentCents: 210,
      limitCents: 500,
    });
  });

  it("shows no money for a task with no budget, and invents nothing else", () => {
    const task = toTask({ ...head, spent_cents: 0, limit_cents: 0 });
    for (const key of ["cost", "spendLimit", "spentCents", "limitCents", "model", "engine", "tokens", "sources", "artifacts", "preparing"]) {
      expect(task).not.toHaveProperty(key);
    }
  });

  it("carries the result, detail, subtasks and events of a task in full", () => {
    const task = toTask({
      ...head,
      result: "done",
      detail: "why",
      subtasks: [{ id: "s1", title: "Flag", status: "Done" }],
      events: [{ id: "e1", time: "not a time", phase: "Working", type: "message", message: "hi" }],
    });
    expect(task.result).toBe("done");
    expect(task.detail).toBe("why");
    expect(task.subtasks).toEqual([{ id: "s1", title: "Flag", status: "Done" }]);
    expect(task.events).toEqual([{ id: "e1", time: "not a time", phase: "Working", type: "message", message: "hi" }]);
  });

  it("refuses a status or phase the web does not know", () => {
    expect(() => toTask({ ...head, status: "Paused" })).toThrow("unknown status");
    expect(() => toTask({ ...head, phase: "Idle" })).toThrow("unknown phase");
  });

  it("reads the cause code of a failed task as a sentence, and leaves other details alone", () => {
    expect(toTask({ ...head, status: "Failed", detail: "parent_cancelled" }).detail).toBe(
      "The parent task was cancelled.",
    );
    expect(toTask({ ...head, status: "Done", detail: "parent_cancelled" }).detail).toBe("parent_cancelled");
  });
});

describe("toApproval", () => {
  const sent = {
    id: "01990000-0000-7000-8000-0000000000a1",
    task_id: "01990000-0000-7000-8000-000000000001",
    title: "Email the weekly summary",
    action: "Send the weekly summary to the finance list",
    state: "pending",
  };

  it("maps task_id to taskId and lets the state through", () => {
    expect(toApproval(sent)).toEqual({
      id: sent.id,
      taskId: sent.task_id,
      title: sent.title,
      action: sent.action,
      state: "pending",
    });
    expect(toApproval({ ...sent, state: "denied" }).state).toBe("denied");
  });

  it("refuses a state the web does not know, and an approval that is not one", () => {
    expect(() => toApproval({ ...sent, state: "closed" })).toThrow("unknown approval state");
    expect(() => toApproval({ ...sent, state: 7 })).toThrow("unknown approval state");
    expect(() => toApproval({ ...sent, action: 7 })).toThrow("no action");
    expect(() => toApproval([])).toThrow("no approval");
  });
});

describe("toNotification", () => {
  const sent = {
    id: "01990000-0000-7000-8000-0000000000c4",
    kind: "question",
    title: "Where should the invoices go?",
  };

  it("carries the task the notification is about, and nothing when there is none", () => {
    expect(toNotification(sent)).toEqual({ id: sent.id, kind: "question", title: sent.title });
    expect(toNotification({ ...sent, kind: "finished", task_id: "01990000-0000-7000-8000-000000000003" })).toEqual({
      id: sent.id,
      kind: "finished",
      title: sent.title,
      taskId: "01990000-0000-7000-8000-000000000003",
    });
    // The brain leaves task_id out when there is no task, but a null reads the same.
    expect(toNotification({ ...sent, task_id: null })).not.toHaveProperty("taskId");
  });

  it("refuses a kind the brain does not send, and a notification that is not one", () => {
    expect(() => toNotification({ ...sent, kind: "approval" })).toThrow("unknown kind");
    expect(() => toNotification({ ...sent, kind: "reminder" })).toThrow("unknown kind");
    expect(() => toNotification("x")).toThrow("no notification");
  });
});

describe("causeSentence", () => {
  it("turns a known cause code into a sentence", () => {
    expect(causeSentence("parent_cancelled")).toBe("The parent task was cancelled.");
  });

  it("shows an unknown code as it is, including names that exist on every object", () => {
    expect(causeSentence("disk_full")).toBe("disk_full");
    expect(causeSentence("constructor")).toBe("constructor");
    expect(causeSentence("")).toBe("");
  });
});
