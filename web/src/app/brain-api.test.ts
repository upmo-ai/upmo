import { describe, expect, it } from "vitest";

import {
  causeSentence,
  shareBody,
  toApproval,
  toChat,
  toChatLine,
  toChatSummary,
  toMember,
  toNotification,
  toTask,
  toTaskShare,
  toTeam,
} from "./brain-api";
import type { ShareGrant } from "../features/identity/types";

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

describe("toTeam", () => {
  it("maps a team to its id and name", () => {
    expect(toTeam({ id: "01990000-0000-7000-8000-0000000000d1", name: "Finance" })).toEqual({
      id: "01990000-0000-7000-8000-0000000000d1",
      name: "Finance",
    });
    expect(() => toTeam({ id: "01990000-0000-7000-8000-0000000000d1" })).toThrow("no name");
    expect(() => toTeam([])).toThrow("no team");
  });
});

describe("toMember", () => {
  const sent = {
    id: "01990000-0000-7000-8000-0000000000a1",
    name: "Ada Lovelace",
    email: "ada@example.com",
    invited: false,
    teams: ["Finance"],
    role: { id: "01990000-0000-7000-8000-0000000000r1", name: "Owner", system: "owner" },
  };

  it("flattens the role to its system key and keeps the team names", () => {
    expect(toMember(sent)).toEqual({
      id: sent.id,
      name: "Ada Lovelace",
      email: "ada@example.com",
      role: "owner",
      teams: ["Finance"],
    });
    expect(toMember({ ...sent, teams: [] }).teams).toEqual([]);
  });

  it("reads a role the web does not know as a member", () => {
    expect(toMember({ ...sent, role: { ...sent.role, system: "" } }).role).toBe("member");
    expect(toMember({ ...sent, role: { ...sent.role, system: "billing" } }).role).toBe("member");
  });

  it("refuses a member with no team list", () => {
    expect(() => toMember({ ...sent, teams: "Finance" })).toThrow("no teams");
    expect(() => toMember({ ...sent, teams: [7] })).toThrow("no teams");
    expect(() => toMember({ ...sent, role: "owner" })).toThrow("no role");
  });
});

describe("toTaskShare", () => {
  const members = [
    { subject_type: "person", subject_id: "01990000-0000-7000-8000-0000000000a1", name: "Ada Lovelace", role: "owner" },
    { subject_type: "team", subject_id: "01990000-0000-7000-8000-0000000000d1", name: "Finance", role: "viewer" },
  ];

  it("carries the team only when the visibility names one", () => {
    expect(toTaskShare({ visibility: "team", team_id: "01990000-0000-7000-8000-0000000000d1", members })).toEqual({
      visibility: "team",
      team_id: "01990000-0000-7000-8000-0000000000d1",
      members,
    });
    expect(toTaskShare({ visibility: "private", members: [members[0]!] })).toEqual({
      visibility: "private",
      members: [members[0]!],
    });
    expect(toTaskShare({ visibility: "everyone", team_id: null, members: [members[0]!] })).not.toHaveProperty(
      "team_id",
    );
  });

  it("refuses a visibility, subject or level the web does not know", () => {
    expect(() => toTaskShare({ visibility: "public", members })).toThrow("unknown visibility");
    expect(() => toTaskShare({ visibility: "private", members: [{ ...members[0]!, subject_type: "bot" }] })).toThrow(
      "unknown subject",
    );
    expect(() => toTaskShare({ visibility: "private", members: [{ ...members[1]!, role: "guest" }] })).toThrow(
      "unknown share level",
    );
    expect(() => toTaskShare({ visibility: "private", members: "ada" })).toThrow("no members");
    expect(() => toTaskShare("private")).toThrow("no share");
  });
});

describe("shareBody", () => {
  const grants: ShareGrant[] = [
    { subject_type: "person", subject_id: "01990000-0000-7000-8000-0000000000a1", role: "owner" },
    { subject_type: "person", subject_id: "01990000-0000-7000-8000-0000000000a2", role: "viewer" },
  ];

  it("sends team_id only for a team visibility that names one", () => {
    expect(shareBody({ visibility: "team", team_id: "01990000-0000-7000-8000-0000000000d1", members: grants })).toEqual(
      { visibility: "team", team_id: "01990000-0000-7000-8000-0000000000d1", members: grants },
    );
    // A stale team never rides along on a visibility that has no team.
    expect(
      shareBody({ visibility: "everyone", team_id: "01990000-0000-7000-8000-0000000000d1", members: grants }),
    ).not.toHaveProperty("team_id");
    expect(
      shareBody({ visibility: "private", team_id: "01990000-0000-7000-8000-0000000000d1", members: grants }),
    ).not.toHaveProperty("team_id");
    expect(shareBody({ visibility: "team", members: grants })).not.toHaveProperty("team_id");
  });
});

describe("toChatSummary", () => {
  const sent = {
    id: "01990000-0000-7000-8000-0000000000c1",
    title: "What should the October close check first?",
    preview: "Start with the supplier statements.",
    last_activity: "2026-11-08T09:10:00Z",
  };

  it("carries the list fields as the wire names them", () => {
    expect(toChatSummary(sent)).toEqual(sent);
  });

  it("refuses a chat with no title, and one that is not a chat", () => {
    expect(() => toChatSummary({ ...sent, title: 7 })).toThrow("no title");
    expect(() => toChatSummary("chat")).toThrow("no chat");
  });
});

describe("toChat and toChatLine", () => {
  const userLine = {
    id: "01990000-0000-7000-8000-00000000l101",
    role: "user",
    body: "What should the October close check first?",
    model: null,
    tokens: 0,
    cost_usd: null,
    at: "2026-11-08T09:11:00Z",
  };
  const assistantLine = {
    id: "01990000-0000-7000-8000-00000000l102",
    role: "assistant",
    body: "Start with the supplier statements.",
    model: "claude-opus-5-5",
    tokens: 412,
    cost_usd: 0.0031,
    at: "2026-11-08T09:12:00Z",
  };

  it("keeps a user line bare and an assistant line's model, tokens and cost", () => {
    expect(toChatLine(userLine)).toEqual(userLine);
    expect(toChatLine(assistantLine)).toEqual(assistantLine);
    expect(toChat({ id: "01990000-0000-7000-8000-0000000000c1", lines: [userLine, assistantLine] })).toEqual({
      id: "01990000-0000-7000-8000-0000000000c1",
      lines: [userLine, assistantLine],
    });
  });

  it("keeps an unknown assistant cost null, never zero", () => {
    expect(toChatLine({ ...assistantLine, cost_usd: null }).cost_usd).toBeNull();
    expect(toChat({ id: "01990000-0000-7000-8000-0000000000c1", lines: [] }).lines).toEqual([]);
  });

  it("refuses a role the web does not know, and a model or cost that is neither a value nor null", () => {
    expect(() => toChatLine({ ...assistantLine, role: "system" })).toThrow("unknown role");
    expect(() => toChatLine({ ...assistantLine, model: undefined })).toThrow("no model");
    expect(() => toChatLine({ ...assistantLine, cost_usd: "0.0031" })).toThrow("no cost_usd");
    expect(() => toChat({ id: "01990000-0000-7000-8000-0000000000c1", lines: "none" })).toThrow("no lines");
  });
});
