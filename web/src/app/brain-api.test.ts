import { afterEach, describe, expect, it, vi } from "vitest";

import { BrainError, createStubApi } from "./api";
import {
  causeSentence,
  createBrainApi,
  providerAddBody,
  providerChangeBody,
  providerTestBody,
  shareBody,
  toAgent,
  toApproval,
  toArtifactDiff,
  toArtifactRecord,
  toChat,
  toChatLine,
  toChatSummary,
  toGatewayRoute,
  toMember,
  toNotification,
  toProvider,
  toTask,
  toTaskShare,
  toTeam,
  toTestResult,
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

  it("keeps a reply's status, the line it answers and the models it tried", () => {
    const failed = {
      ...assistantLine,
      model: null,
      status: "failed",
      reply_to: userLine.id,
      attempts: [{ model: "gpt-x", reason: "rate limited" }],
    };
    expect(toChatLine(failed)).toEqual(failed);
    expect(toChatLine({ ...assistantLine, status: "done", reply_to: null, attempts: [] })).toEqual({
      ...assistantLine,
      status: "done",
    });
    expect(() => toChatLine({ ...failed, attempts: [{ model: "gpt-x" }] })).toThrow("no reason");
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

describe("toProvider", () => {
  const sent = {
    id: "01990000-0000-7000-8000-0000000000e1",
    provider: "openai",
    name: "OpenAI Work",
    adapter: "openai_compat",
    plan_kind: "metered",
    base_url: "https://api.openai.com",
    status: "Not connected",
    models: ["gpt-4o", "o3-mini"],
    usable: false,
    usable_models: [],
    visibility: "private",
    team_ids: [],
    team_names: [],
    can_manage: true,
    can_use: false,
    is_owner: true,
  };

  it("keeps the account as the brain sends it, status and visibility included", () => {
    expect(toProvider(sent)).toEqual(sent);
    expect(toProvider({ ...sent, visibility: "everyone", status: "Key rejected", can_manage: false })).toMatchObject({
      visibility: "everyone",
      status: "Key rejected",
      can_manage: false,
    });
  });

  it("reads the usable answer with the models that count", () => {
    expect(
      toProvider({ ...sent, status: "Connected", usable: true, usable_models: ["gpt-4o"] }),
    ).toMatchObject({ usable: true, usable_models: ["gpt-4o"] });
    expect(() => toProvider({ ...sent, usable: undefined })).toThrow("no usable");
    expect(() => toProvider({ ...sent, usable_models: ["gpt-4o", 4] })).toThrow("no usable_models");
  });

  it("leaves base_url out when the account has none", () => {
    const { base_url: _gone, ...bare } = sent;
    expect(toProvider(bare)).not.toHaveProperty("base_url");
    expect(toProvider({ ...bare, base_url: "" })).not.toHaveProperty("base_url");
  });

  it("refuses an unknown plan, status or visibility, and models that are not text", () => {
    expect(() => toProvider({ ...sent, plan_kind: "free" })).toThrow("unknown plan kind");
    expect(() => toProvider({ ...sent, status: "Broken" })).toThrow("unknown status");
    expect(() => toProvider({ ...sent, visibility: "workspace" })).toThrow("unknown visibility");
    expect(() => toProvider({ ...sent, visibility: undefined })).toThrow("unknown visibility");
    expect(() => toProvider({ ...sent, can_manage: undefined })).toThrow("no can_manage");
    expect(() => toProvider({ ...sent, can_use: "yes" })).toThrow("no can_use");
    expect(() => toProvider({ ...sent, is_owner: undefined })).toThrow("no is_owner");
    expect(() => toProvider({ ...sent, models: ["gpt-4o", 4] })).toThrow("no models");
    expect(() => toProvider({ ...sent, models: null })).toThrow("no models");
  });

  it("reads no key, even if one were sent", () => {
    expect(toProvider({ ...sent, api_key: "sk-secret" })).not.toHaveProperty("api_key");
  });
});

describe("toTestResult", () => {
  it("keeps ok and the sentence of a failed test", () => {
    expect(toTestResult({ ok: true })).toEqual({ ok: true });
    expect(toTestResult({ ok: true, error: "" })).toEqual({ ok: true });
    expect(toTestResult({ ok: false, error: "The key was rejected." })).toEqual({
      ok: false,
      error: "The key was rejected.",
    });
  });

  it("refuses an answer with no ok", () => {
    expect(() => toTestResult({ error: "x" })).toThrow("no ok");
    expect(() => toTestResult({ ok: "yes" })).toThrow("no ok");
  });
});

describe("provider request bodies", () => {
  const add = {
    provider: "anthropic",
    name: "Claude Team",
    adapter: "anthropic",
    plan_kind: "metered" as const,
    base_url: "https://api.anthropic.com",
    api_key: "sk-ant-1",
    models: ["claude-3-7-sonnet"],
  };

  it("sends the brain's adapter key for a new account and the key only in the body", () => {
    expect(providerAddBody(add)).toEqual({
      provider: "anthropic",
      name: "Claude Team",
      adapter: "anthropic",
      plan_kind: "metered",
      base_url: "https://api.anthropic.com",
      api_key: "sk-ant-1",
      models: ["claude-3-7-sonnet"],
    });
    expect(providerAddBody({ ...add, provider: "openai", adapter: "openai" })).toMatchObject({
      provider: "openai",
      adapter: "openai_compat",
    });
    expect(providerAddBody({ ...add, adapter: "fake", plan_kind: "local" })).toMatchObject({ adapter: "fake" });
  });

  it("leaves out an empty base URL and an empty key", () => {
    const body = providerAddBody({ ...add, base_url: "", api_key: "" });
    expect(body).not.toHaveProperty("base_url");
    expect(body).not.toHaveProperty("api_key");
    expect(providerAddBody({ ...add, base_url: undefined, api_key: undefined })).not.toHaveProperty("api_key");
  });

  it("sends a change with only what was given, and no api_key keeps the saved key", () => {
    expect(providerChangeBody({ name: "Renamed", models: ["a"] })).toEqual({ name: "Renamed", models: ["a"] });
    expect(providerChangeBody({ plan_kind: "local", api_key: "" })).toEqual({ plan_kind: "local" });
    expect(providerChangeBody({ api_key: "sk-new" })).toEqual({ api_key: "sk-new" });
    expect(providerChangeBody({})).toEqual({});
    expect(providerChangeBody({ models: [] })).toEqual({ models: [] });
    expect(providerChangeBody({ base_url: "" })).toEqual({ base_url: "" });
    expect(providerChangeBody({ base_url: undefined })).toEqual({});
  });

  it("always sends plan_kind in a test when it is known, with the brain's adapter key", () => {
    expect(
      providerTestBody({ provider: "ollama", adapter: "ollama", plan_kind: "local", base_url: "http://ollama:11434", model: "llama3.3:70b" }),
    ).toEqual({
      provider: "ollama",
      adapter: "openai_compat",
      plan_kind: "local",
      base_url: "http://ollama:11434",
      model: "llama3.3:70b",
    });
    expect(providerTestBody({ provider: "anthropic", adapter: "anthropic", plan_kind: "metered" })).toMatchObject({
      adapter: "anthropic",
    });
  });

  it("tests a saved account by id with no key, and a typed key by value", () => {
    expect(providerTestBody({ provider: "openai", plan_kind: "metered", id: "p1" })).toEqual({
      provider: "openai",
      plan_kind: "metered",
      id: "p1",
    });
    expect(providerTestBody({ provider: "openai", plan_kind: "metered", api_key: "sk-typed" })).toEqual({
      provider: "openai",
      plan_kind: "metered",
      api_key: "sk-typed",
    });
  });
});

describe("toGatewayRoute", () => {
  const sent = {
    name: "chat",
    capability: "chat",
    decision: "",
    usable: true,
    hops: [{ position: 1, adapter: "anthropic", model: "claude-opus-5-5", weight: 3, retry: 1 }],
  };

  it("reads a route and its hops, as a workspace route since the brain sends no scope", () => {
    expect(toGatewayRoute(sent)).toEqual({ ...sent, scope: "global" });
    expect(toGatewayRoute({ ...sent, scope: "own", capability: "decide", decision: "yesno" })).toMatchObject({
      scope: "own",
      capability: "decide",
      decision: "yesno",
    });
  });

  it("reads the account a hop names, when the brain sends one", () => {
    expect(
      toGatewayRoute({ ...sent, hops: [{ ...sent.hops[0], account_id: "01990000-0000-7000-8000-0000000000e1" }] }),
    ).toMatchObject({ hops: [{ account_id: "01990000-0000-7000-8000-0000000000e1" }] });
    expect(toGatewayRoute(sent).hops[0]).not.toHaveProperty("account_id");
  });

  it("refuses a capability or decision the web does not know, and a hop with no model", () => {
    expect(() => toGatewayRoute({ ...sent, capability: "image" })).toThrow("capability");
    expect(() => toGatewayRoute({ ...sent, decision: "maybe" })).toThrow("decision");
    expect(() => toGatewayRoute({ ...sent, hops: [{ ...sent.hops[0], model: 4 }] })).toThrow("no model");
    expect(() => toGatewayRoute("chat")).toThrow("no route");
  });

  it("refuses an answer with no usable flag", () => {
    expect(() => toGatewayRoute({ ...sent, usable: undefined })).toThrow("no usable");
  });
});

describe("toAgent", () => {
  const sent = {
    id: "01990000-0000-7000-8000-0000000000a1",
    name: "Research",
    description: "Reads widely",
    prompt_overlay: "Cite sources.",
    route: "deep",
    model_provider: "OpenAI",
    model_id: "gpt-x",
    enabled: true,
    usable: true,
    visibility: "private",
    team_ids: [],
    team_names: [],
    can_use: true,
    can_manage: true,
  };

  it("reads an agent as the brain sends it", () => {
    expect(toAgent(sent)).toEqual(sent);
    expect(toAgent({ ...sent, visibility: "everyone", can_manage: false, route: "", model_provider: "", model_id: "" })).toMatchObject({
      visibility: "everyone",
      can_manage: false,
      route: "",
    });
  });

  it("refuses what is not an agent", () => {
    expect(() => toAgent(null)).toThrow("The brain sent no agent.");
    expect(() => toAgent({ ...sent, visibility: "workspace" })).toThrow("unknown visibility");
    expect(() => toAgent({ ...sent, enabled: "yes" })).toThrow("The brain sent no enabled.");
    expect(() => toAgent({ ...sent, usable: undefined })).toThrow("The brain sent no usable.");
    expect(() => toAgent({ ...sent, can_manage: undefined })).toThrow("no can_manage");
    expect(() => toAgent({ ...sent, name: 5 })).toThrow("The brain sent no name.");
  });
});

describe("toArtifactRecord", () => {
  const sent = {
    id: "018f3b2c-7a10-7c4a-8e11-444444444444",
    name: "run-notes.txt",
    kind: "text",
    mime: "text/plain",
    size: 128,
    created_at: "2026-11-18T10:00:00Z",
  };

  it("reads an artifact as the brain sends it", () => {
    expect(toArtifactRecord(sent)).toEqual(sent);
    expect(toArtifactRecord({ ...sent, size: 0, kind: "diff" })).toMatchObject({ size: 0, kind: "diff" });
  });

  it("reads a kind the web does not know as a plain file", () => {
    expect(toArtifactRecord({ ...sent, kind: "video" }).kind).toBe("binary");
    expect(toArtifactRecord({ ...sent, kind: "" }).kind).toBe("binary");
  });

  it("refuses an artifact that is not one", () => {
    expect(() => toArtifactRecord(null)).toThrow("no artifact");
    expect(() => toArtifactRecord([])).toThrow("no artifact");
    expect(() => toArtifactRecord({ ...sent, id: 1 })).toThrow("no id");
    expect(() => toArtifactRecord({ ...sent, name: 1 })).toThrow("no name");
    expect(() => toArtifactRecord({ ...sent, kind: 1 })).toThrow("no kind");
    expect(() => toArtifactRecord({ ...sent, mime: 1 })).toThrow("no mime");
    expect(() => toArtifactRecord({ ...sent, size: "128" })).toThrow("no size");
    expect(() => toArtifactRecord({ ...sent, size: -1 })).toThrow("no size");
    expect(() => toArtifactRecord({ ...sent, size: 1.5 })).toThrow("no size");
    expect(() => toArtifactRecord({ ...sent, created_at: 1 })).toThrow("no created_at");
  });
});

describe("toArtifactDiff", () => {
  const sent = {
    files: [
      { path: "triage.py", before: "old", after: "new" },
      { path: "rules/receipts.yaml", before: null, after: "added" },
      { path: "rules/legacy.yaml", before: "gone", after: null },
    ],
  };

  it("reads a diff, including one with no changes", () => {
    expect(toArtifactDiff(sent)).toEqual(sent);
    expect(toArtifactDiff({ files: [] })).toEqual({ files: [] });
  });

  it("refuses a diff that is not one", () => {
    expect(() => toArtifactDiff(null)).toThrow("no diff");
    expect(() => toArtifactDiff({ files: null })).toThrow("no files");
    expect(() => toArtifactDiff({})).toThrow("no files");
    expect(() => toArtifactDiff({ files: [null] })).toThrow("no file");
    expect(() => toArtifactDiff({ files: [{ before: "a", after: "b" }] })).toThrow("no path");
    expect(() => toArtifactDiff({ files: [{ path: "  ", before: "a", after: "b" }] })).toThrow("no path");
    expect(() => toArtifactDiff({ files: [{ path: "a.py", after: "b" }] })).toThrow("no before");
    expect(() => toArtifactDiff({ files: [{ path: "a.py", before: 1, after: "b" }] })).toThrow("no before");
    expect(() => toArtifactDiff({ files: [{ path: "a.py", before: null, after: null }] })).toThrow("no change");
  });
});

const taskId = "01990000-0000-7000-8000-000000000001";
const artifactId = "018f3b2c-7a10-7c4a-8e11-444444444444";

describe("createBrainApi artifacts", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads the live routes once the brain answers, and maps an unknown kind to a file", async () => {
    const record = {
      id: artifactId,
      name: "run-notes.txt",
      kind: "text",
      mime: "text/plain",
      size: 128,
      created_at: "2026-11-18T10:00:00Z",
    };
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input);
        calls.push(`${init?.method ?? "GET"} ${path}`);
        expect(init?.credentials).toBe("include");
        if (path === "/api/tasks") return Response.json([]);
        if (path === `/api/tasks/${taskId}/artifacts`) {
          return Response.json([record, { ...record, id: "other", name: "clip.mov", kind: "video" }]);
        }
        if (path === `/api/tasks/empty/artifacts`) return Response.json([]);
        if (path === `/api/artifacts/${artifactId}/text`) {
          return new Response("Run 14", { status: 200, headers: { "content-type": "text/plain; charset=utf-8" } });
        }
        if (path === `/api/artifacts/${artifactId}/diff`) {
          return Response.json({ files: [{ path: "a.py", before: null, after: "x" }] });
        }
        return Response.json({ error: "no such artifact" }, { status: 404 });
      }),
    );

    const stub = createStubApi();
    const api = createBrainApi(stub);
    expect(api.artifacts.downloadUrl("run-notes")).toBe(stub.artifacts.downloadUrl("run-notes"));

    const listed = await api.artifacts.list(taskId);
    expect(listed[0]).toEqual(record);
    expect(listed[1]).toMatchObject({ id: "other", kind: "binary" });
    expect(await api.artifacts.list("empty")).toEqual([]);
    expect(await api.artifacts.text(artifactId)).toBe("Run 14");
    expect(await api.artifacts.diff(artifactId)).toEqual({ files: [{ path: "a.py", before: null, after: "x" }] });
    expect(api.artifacts.downloadUrl(artifactId)).toBe(`/api/artifacts/${artifactId}/download`);
    expect(api.artifacts.downloadUrl("a/b")).toBe("/api/artifacts/a%2Fb/download");
    expect(calls).toEqual([
      "GET /api/tasks",
      `GET /api/tasks/${taskId}/artifacts`,
      "GET /api/tasks/empty/artifacts",
      `GET /api/artifacts/${artifactId}/text`,
      `GET /api/artifacts/${artifactId}/diff`,
    ]);
  });

  it("reports a text or diff the brain refuses", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input: RequestInfo | URL) => {
        const path = String(input);
        if (path === "/api/tasks") return Response.json([]);
        if (path.endsWith("/text")) return Response.json({ error: "artifact is not text" }, { status: 415 });
        if (path.endsWith("/diff")) return Response.json({ error: "artifact is not a diff" }, { status: 415 });
        return Response.json({ error: "no such artifact" }, { status: 404 });
      }),
    );
    const api = createBrainApi();
    await expect(api.artifacts.text(artifactId)).rejects.toBeInstanceOf(BrainError);
    await expect(api.artifacts.text(artifactId)).rejects.toMatchObject({ status: 415, error: "artifact is not text" });
    await expect(api.artifacts.diff(artifactId)).rejects.toMatchObject({ status: 415, error: "artifact is not a diff" });
  });

  it("stays on the stub before the probe settles and when GET /api/tasks is 404", async () => {
    const fetcher = vi.fn<typeof fetch>(async () => Response.json({ error: "missing" }, { status: 404 }));
    vi.stubGlobal("fetch", fetcher);
    const stub = createStubApi();
    const api = createBrainApi(stub);

    expect(api.artifacts.downloadUrl("run-notes")).toBe(stub.artifacts.downloadUrl("run-notes"));
    expect(await api.artifacts.list("aster-digest")).toEqual(await stub.artifacts.list("aster-digest"));
    expect(await api.artifacts.text("run-notes")).toBe(await stub.artifacts.text("run-notes"));
    expect(await api.artifacts.diff("triage-changes")).toEqual(await stub.artifacts.diff("triage-changes"));
    expect(api.artifacts.downloadUrl("run-notes")).toBe(stub.artifacts.downloadUrl("run-notes"));
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(String(fetcher.mock.calls[0]?.[0])).toBe("/api/tasks");
  });
});
