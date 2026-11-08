import { BrainError, createStubApi, type ApiClient } from "./api";
import {
  noticeKinds,
  type Approval,
  type ApprovalState,
  type AppNotification,
  type NotificationKind,
} from "../features/notifications/types";
import {
  phases,
  taskStatuses,
  type NewTask,
  type Phase,
  type Subtask,
  type Task,
  type TaskEvent,
  type TaskStatus,
} from "../features/tasks/types";
import {
  shareLevels,
  taskVisibilities,
  type Member,
  type ShareLevel,
  type ShareMember,
  type ShareSubjectType,
  type ShareUpdate,
  type TaskShare,
  type TaskVisibility,
  type Team,
  type WorkspaceRole,
} from "../features/identity/types";
import type { Chat, ChatLine, ChatSummary, LineRole } from "../features/chat/types";

type Json = Record<string, unknown>;

function isRecord(value: unknown): value is Json {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isPhase(value: unknown): value is Phase {
  return phases.some((phase) => phase === value);
}

function isStatus(value: unknown): value is TaskStatus {
  return taskStatuses.some((status) => status === value);
}

function isNoticeKind(value: unknown): value is NotificationKind {
  return noticeKinds.some((kind) => kind === value);
}

function isApprovalState(value: unknown): value is ApprovalState {
  return value === "pending" || value === "approved" || value === "denied";
}

function isShareVisibility(value: unknown): value is TaskVisibility {
  return (taskVisibilities as readonly string[]).includes(value as string);
}

function isShareLevel(value: unknown): value is ShareLevel {
  return (shareLevels as readonly string[]).includes(value as string);
}

function isRole(value: unknown): value is LineRole {
  return value === "user" || value === "assistant";
}

function record(value: unknown, what: string): Json {
  if (!isRecord(value)) throw new Error(`The brain sent no ${what}.`);
  return value;
}

function text(source: Json, key: string): string {
  const value = source[key];
  if (typeof value !== "string") throw new Error(`The brain sent no ${key}.`);
  return value;
}

function whole(source: Json, key: string): number {
  const value = source[key];
  if (typeof value !== "number" || !Number.isFinite(value)) throw new Error(`The brain sent no ${key}.`);
  return value;
}

function list(value: unknown, what: string): unknown[] {
  if (!Array.isArray(value)) throw new Error(`The brain sent no ${what}.`);
  return value;
}

function phaseOf(source: Json): Phase {
  const value = source.phase;
  if (!isPhase(value)) throw new Error("The brain sent an unknown phase.");
  return value;
}

function statusOf(source: Json): TaskStatus {
  const value = source.status;
  if (!isStatus(value)) throw new Error("The brain sent an unknown status.");
  return value;
}

function kindOf(source: Json): NotificationKind {
  const value = source.kind;
  if (!isNoticeKind(value)) throw new Error("The brain sent an unknown kind.");
  return value;
}

function stateOf(source: Json): ApprovalState {
  const value = source.state;
  if (!isApprovalState(value)) throw new Error("The brain sent an unknown approval state.");
  return value;
}

/** Cents as the stub writes them: €2.10. */
function euros(cents: number): string {
  return `€${(cents / 100).toFixed(2)}`;
}

/** The time of day, as the stub writes it. A stamp that is not a date is shown as sent. */
function clock(stamp: string): string {
  const date = new Date(stamp);
  return Number.isNaN(date.getTime()) ? stamp : date.toTimeString().slice(0, 8);
}

const causeSentences: Readonly<Record<string, string>> = {
  parent_cancelled: "The parent task was cancelled.",
};

/** The brain sends the cause of a failed task as a code. Known codes read as a sentence; an unknown one is shown as sent. */
export function causeSentence(code: string): string {
  return Object.hasOwn(causeSentences, code) ? (causeSentences[code] ?? code) : code;
}

function toEvent(value: unknown): TaskEvent {
  const source = record(value, "event");
  return {
    id: text(source, "id"),
    time: clock(text(source, "time")),
    phase: phaseOf(source),
    type: text(source, "type"),
    message: text(source, "message"),
  };
}

function toSubtask(value: unknown): Subtask {
  const source = record(value, "subtask");
  return { id: text(source, "id"), title: text(source, "title"), status: statusOf(source) };
}

/**
 * A task as the brain sends it: GET /api/tasks gives the light fields, and
 * GET /api/tasks/{id} adds the result, detail, subtasks and events. What the
 * brain does not send (model, engine, tokens, sources, artifacts, preparing)
 * stays absent. The money fields are worked out from the cents it does send.
 */
export function toTask(value: unknown): Task {
  const source = record(value, "task");
  const spent = whole(source, "spent_cents");
  const limit = whole(source, "limit_cents");
  const task: Task = {
    id: text(source, "id"),
    title: text(source, "title"),
    status: statusOf(source),
    phase: phaseOf(source),
    result: "",
    detail: "",
  };
  // No budget is no figure: a task without a limit has nothing to show against.
  if (limit > 0) {
    task.cost = euros(spent);
    task.spendLimit = euros(limit);
    task.spentCents = spent;
    task.limitCents = limit;
  }
  if ("result" in source) task.result = text(source, "result");
  // On a failed task the detail is the cause code; on a done one it is the result's own detail.
  if ("detail" in source) {
    const detail = text(source, "detail");
    task.detail = task.status === "Failed" ? causeSentence(detail) : detail;
  }
  if ("subtasks" in source) task.subtasks = list(source.subtasks, "subtasks").map(toSubtask);
  if ("events" in source) task.events = list(source.events, "events").map(toEvent);
  return task;
}

function toNewTask(value: unknown): NewTask {
  const source = record(value, "task");
  if (!isPhase(source.status)) throw new Error("The brain sent an unknown status.");
  return { id: text(source, "id"), status: source.status, goal: text(source, "goal") };
}

/** An approval as GET /api/approvals and the decide routes send it. */
export function toApproval(value: unknown): Approval {
  const source = record(value, "approval");
  return {
    id: text(source, "id"),
    taskId: text(source, "task_id"),
    title: text(source, "title"),
    action: text(source, "action"),
    state: stateOf(source),
  };
}

/** A notification as GET /api/notifications sends it: task_id only when there is a task. */
export function toNotification(value: unknown): AppNotification {
  const source = record(value, "notification");
  const item: AppNotification = { id: text(source, "id"), kind: kindOf(source), title: text(source, "title") };
  if (typeof source.task_id === "string") item.taskId = source.task_id;
  return item;
}

/** A team as GET /api/teams sends it. */
export function toTeam(value: unknown): Team {
  const source = record(value, "team");
  return { id: text(source, "id"), name: text(source, "name") };
}

/** A workspace role as the brain writes it. A custom role reads as a member. */
function workspaceRole(system: unknown): WorkspaceRole {
  return system === "owner" || system === "admin" || system === "viewer" ? system : "member";
}

/** A member as GET /api/members sends it: teams by name, the role nested. */
export function toMember(value: unknown): Member {
  const source = record(value, "member");
  const teams = list(source.teams, "teams");
  if (teams.some((team) => typeof team !== "string")) throw new Error("The brain sent no teams.");
  return {
    id: text(source, "id"),
    name: text(source, "name"),
    email: text(source, "email"),
    role: workspaceRole(record(source.role, "role").system),
    teams: teams as string[],
  };
}

function toShareMember(value: unknown): ShareMember {
  const source = record(value, "share member");
  const type = source.subject_type;
  if (type !== "person" && type !== "team") throw new Error("The brain sent an unknown subject.");
  const role = source.role;
  if (!isShareLevel(role)) throw new Error("The brain sent an unknown share level.");
  return {
    subject_type: type as ShareSubjectType,
    subject_id: text(source, "subject_id"),
    name: text(source, "name"),
    role,
  };
}

/** A share as GET and PUT /api/tasks/{id}/share send it: the owner entry first. */
export function toTaskShare(value: unknown): TaskShare {
  const source = record(value, "share");
  const visibility = source.visibility;
  if (!isShareVisibility(visibility)) throw new Error("The brain sent an unknown visibility.");
  const share: TaskShare = {
    visibility,
    members: list(source.members, "members").map(toShareMember),
  };
  if (typeof source.team_id === "string") share.team_id = source.team_id;
  return share;
}

/** The PUT body of a share: team_id only when the owner names a team. */
export function shareBody(update: ShareUpdate): Json {
  return {
    visibility: update.visibility,
    ...(update.visibility === "team" && update.team_id ? { team_id: update.team_id } : {}),
    members: update.members.map(({ subject_type, subject_id, role }) => ({ subject_type, subject_id, role })),
  };
}

/** A chat as GET /api/chats lists it: named by its first user line, with its newest line as the preview. */
export function toChatSummary(value: unknown): ChatSummary {
  const source = record(value, "chat");
  return {
    id: text(source, "id"),
    title: text(source, "title"),
    preview: text(source, "preview"),
    last_activity: text(source, "last_activity"),
  };
}

/**
 * A line as GET /api/chats/{id} sends it. A user line carries no model; a cost
 * the brain does not know stays null, so it never reads as free.
 */
export function toChatLine(value: unknown): ChatLine {
  const source = record(value, "line");
  const role = source.role;
  if (!isRole(role)) throw new Error("The brain sent an unknown role.");
  const model = source.model;
  if (model !== null && typeof model !== "string") throw new Error("The brain sent no model.");
  const cost = source.cost_usd;
  if (cost !== null && typeof cost !== "number") throw new Error("The brain sent no cost_usd.");
  return {
    id: text(source, "id"),
    role,
    body: text(source, "body"),
    model,
    tokens: whole(source, "tokens"),
    cost_usd: cost,
    at: text(source, "at"),
  };
}

/** A chat in full, as GET /api/chats/{id} sends it: lines oldest first. */
export function toChat(value: unknown): Chat {
  const source = record(value, "chat");
  return { id: text(source, "id"), lines: list(source.lines, "lines").map(toChatLine) };
}

async function call(path: string, init?: { method: "POST" | "PUT" | "DELETE"; body?: unknown }): Promise<unknown> {
  const response = await fetch(path, {
    credentials: "include",
    ...(init
      ? {
          method: init.method,
          ...(init.body !== undefined
            ? { headers: { "content-type": "application/json" }, body: JSON.stringify(init.body) }
            : {}),
        }
      : {}),
  });
  // 202 Accepted and 204 No Content: a stored chat line (202) and a dismissed
  // notification (204) both answer with no body to read.
  if (response.status === 202 || response.status === 204) return null;
  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
    // A refusal can come with no JSON body.
    if (response.ok) throw new Error("The brain sent no JSON.");
  }
  if (!response.ok) {
    throw new BrainError(response.status, isRecord(body) && typeof body.error === "string" ? body.error : undefined);
  }
  return body;
}

function taskPath(taskId: string) {
  return `/api/tasks/${encodeURIComponent(taskId)}`;
}

function sharePath(taskId: string) {
  return `${taskPath(taskId)}/share`;
}

function decisionPath(id: string, decision: "approve" | "deny") {
  return `/api/approvals/${encodeURIComponent(id)}/${decision}`;
}

function notificationPath(id: string) {
  return `/api/notifications/${encodeURIComponent(id)}`;
}

function chatPath(id: string) {
  return `/api/chats/${encodeURIComponent(id)}`;
}

type Source = "brain" | "stub";

/**
 * The stub client with its task methods (list, get, advance, create, message),
 * its approvals, its notifications, its member list, its task shares and its
 * chats answered by the brain instead. Everything else stays on the stub.
 *
 * The first task call asks GET /api/tasks. When that fails the way a missing
 * brain does (no network, or 404), every task method uses the stub for the rest
 * of the session. Any other failure, such as 401 or 5xx, is thrown and the
 * question is asked again on the next call, so a refusal is never hidden by
 * sample data.
 */
export function createBrainApi(stub: ApiClient = createStubApi()): ApiClient {
  let source: Promise<Source> | undefined;
  // The source the session settled on, once the probe has answered. Until then
  // no chat has been read, so nothing is awaited yet.
  let settled: Source | undefined;

  async function probe(): Promise<Source> {
    try {
      await call("/api/tasks");
      return "brain";
    } catch (cause) {
      if (cause instanceof BrainError && cause.status !== 404) throw cause;
      return "stub";
    }
  }

  function chosen(): Promise<Source> {
    source ??= probe().then(
      (value) => {
        settled = value;
        return value;
      },
      (cause: unknown) => {
        source = undefined;
        throw cause;
      },
    );
    return source;
  }

  return {
    ...stub,
    async listTasks() {
      if ((await chosen()) === "stub") return stub.listTasks();
      return list(await call("/api/tasks"), "tasks").map(toTask);
    },
    async getTask(taskId) {
      if ((await chosen()) === "stub") return stub.getTask(taskId);
      return toTask(await call(taskPath(taskId)));
    },
    async nextPhase(taskId) {
      if ((await chosen()) === "stub") return stub.nextPhase(taskId);
      return toTask(await call(taskPath(taskId), { method: "POST", body: { kind: "advance" } }));
    },
    async createTask(goal, repo) {
      if ((await chosen()) === "stub") return stub.createTask(goal, repo);
      return toNewTask(await call("/api/tasks", { method: "POST", body: { goal, ...(repo ? { repo } : {}) } }));
    },
    async postTaskMessage(taskId, body) {
      if ((await chosen()) === "stub") return stub.postTaskMessage(taskId, body);
      return toEvent(await call(taskPath(taskId), { method: "POST", body: { kind: "chat", body } }));
    },
    async listNotifications(query) {
      if ((await chosen()) === "stub") return stub.listNotifications(query);
      const limit = query?.limit;
      const path = limit === undefined ? "/api/notifications" : `/api/notifications?limit=${limit}`;
      return list(await call(path), "notifications").map(toNotification);
    },
    async dismissNotification(id) {
      if ((await chosen()) === "stub") return stub.dismissNotification(id);
      await call(notificationPath(id), { method: "DELETE" });
    },
    approvals: {
      async list() {
        if ((await chosen()) === "stub") return stub.approvals.list();
        return list(await call("/api/approvals"), "approvals").map(toApproval);
      },
      async approve(id) {
        if ((await chosen()) === "stub") return stub.approvals.approve(id);
        return toApproval(await call(decisionPath(id, "approve"), { method: "POST" }));
      },
      async deny(id) {
        if ((await chosen()) === "stub") return stub.approvals.deny(id);
        return toApproval(await call(decisionPath(id, "deny"), { method: "POST" }));
      },
    },
    members: {
      ...stub.members,
      async list() {
        if ((await chosen()) === "stub") return stub.members.list();
        return list(await call("/api/members"), "members").map(toMember);
      },
    },
    shares: {
      async teams() {
        if ((await chosen()) === "stub") return stub.shares.teams();
        return list(await call("/api/teams"), "teams").map(toTeam);
      },
      async get(taskId) {
        if ((await chosen()) === "stub") return stub.shares.get(taskId);
        return toTaskShare(await call(sharePath(taskId)));
      },
      async put(taskId, update) {
        if ((await chosen()) === "stub") return stub.shares.put(taskId, update);
        return toTaskShare(await call(sharePath(taskId), { method: "PUT", body: shareBody(update) }));
      },
    },
    chats: {
      // The stub plays the assistant a moment later; the brain only stores the
      // line, so a reply is worth waiting for only while the stub owes one.
      get repliesLater() {
        return settled === "stub";
      },
      async list() {
        if ((await chosen()) === "stub") return stub.chats.list();
        return list(await call("/api/chats"), "chats").map(toChatSummary);
      },
      async create() {
        if ((await chosen()) === "stub") return stub.chats.create();
        return toChatSummary(await call("/api/chats", { method: "POST" }));
      },
      async get(id) {
        if ((await chosen()) === "stub") return stub.chats.get(id);
        return toChat(await call(chatPath(id)));
      },
      async send(id, body) {
        if ((await chosen()) === "stub") return stub.chats.send(id, body);
        await call(chatPath(id), { method: "POST", body: { body } });
      },
    },
  };
}
