import { BrainError, createStubApi, type ApiClient } from "./api";
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

async function call(path: string, init?: { method: "POST"; body: unknown }): Promise<unknown> {
  const response = await fetch(path, {
    credentials: "include",
    ...(init
      ? { method: init.method, headers: { "content-type": "application/json" }, body: JSON.stringify(init.body) }
      : {}),
  });
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

type Source = "brain" | "stub";

/**
 * The stub client with its task methods (list, get, advance, create, message)
 * answered by the brain instead. Everything else stays on the stub.
 *
 * The first task call asks GET /api/tasks. When that fails the way a missing
 * brain does (no network, or 404), every task method uses the stub for the rest
 * of the session. Any other failure, such as 401 or 5xx, is thrown and the
 * question is asked again on the next call, so a refusal is never hidden by
 * sample data.
 */
export function createBrainApi(stub: ApiClient = createStubApi()): ApiClient {
  let source: Promise<Source> | undefined;

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
    source ??= probe().catch((cause: unknown) => {
      source = undefined;
      throw cause;
    });
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
  };
}
