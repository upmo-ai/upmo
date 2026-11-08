import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useState, type ReactNode } from "react";

import {
  canChangeOwnerRole,
  canEditSharing,
  canManageMembers,
  canRemovePasskey,
  canRemovePassword,
  isAllowedScope,
  isGrantableShareLevel,
  isInviteRole,
  isViewRole,
  lastOwnerReason,
  lastPasskeyReason,
  lastPasswordReason,
  ownerLockReason,
} from "../features/identity/rules";
import type {
  ApiToken,
  CreatedToken,
  CurrentUser,
  InviteRole,
  Member,
  Passkey,
  ShareGrant,
  ShareUpdate,
  SignInMethod,
  TaskShare,
  TaskVisibility,
  Team,
  ViewRole,
  WorkspaceRole,
} from "../features/identity/types";
import { createArtifactsStub } from "../features/artifacts/stub";
import type { ArtifactsApi } from "../features/artifacts/types";
import { createKnowledgeStub } from "../features/knowledge/stub";
import type { KnowledgeApi } from "../features/knowledge/types";
import { createChatsStub } from "../features/chat/stub";
import { usageFor } from "../features/usage/stub";
import type { Chat, ChatSummary } from "../features/chat/types";
import { sampleDecisions, sampleGates, sampleRoutes } from "../features/models/sample";
import type { DecisionBinding, DecisionGate, GatewayRoute } from "../features/models/types";
import type { Approval, AppNotification } from "../features/notifications/types";
import { goalProblem, messageProblem, repoProblem } from "../features/tasks/rules";
import {
  finishPreparing,
  preparingProbeFailed,
  preparingReady,
  preparingReadyBare,
  preparingRunning,
} from "../features/tasks/preparing-sample";
import {
  messageEventType,
  phases,
  type NewTask,
  type Phase,
  type Task,
  type TaskEvent,
} from "../features/tasks/types";

export type {
  FileInput,
  KnowledgeApi,
  KnowledgeFolder,
  KnowledgeHit,
  KnowledgeSource,
  MarkdownInput,
  SourceKind,
  SourceProvenance,
  SourceStatus,
  UrlInput,
} from "../features/knowledge/types";
export type {
  ArtifactDiff,
  ArtifactDiffFile,
  ArtifactKind,
  ArtifactRecord,
  ArtifactsApi,
} from "../features/artifacts/types";
export type { Chat, ChatLine, ChatSummary, LineRole } from "../features/chat/types";
export type {
  DecisionBinding,
  DecisionEval,
  DecisionGate,
  GateKind,
  GateVerdict,
  GatewayRoute,
  RouteCapability,
  RouteDecision,
  RouteHop,
  Scope,
} from "../features/models/types";
export type { Approval, AppNotification, NotificationKind } from "../features/notifications/types";
export type { Phase, Task, TaskStatus } from "../features/tasks/types";
export type { NewTask, TaskEvent } from "../features/tasks/types";
export type {
  PrepareStep,
  PrepareStepName,
  PrepareStepState,
  Preparing,
  Runtime,
  RuntimeLimits,
  RuntimeTier,
} from "../features/tasks/types";
export type {
  ApiToken,
  CreatedToken,
  CurrentUser,
  GrantableShareLevel,
  InviteRole,
  Member,
  Passkey,
  ShareGrant,
  ShareLevel,
  ShareMember,
  ShareSubjectType,
  ShareUpdate,
  SignInMethod,
  TaskShare,
  TaskVisibility,
  Team,
  TokenExpiry,
  ViewRole,
  WorkspaceRole,
} from "../features/identity/types";

export type ConnectorGrant = {
  connector: string;
  /**
   * Who owns the connector. A person manages only their own; a global
   * (workspace) connector shows its rules read only.
   */
  scope: "global" | "own";
  resource_scope: "all" | "these";
  resource_ids: string[];
  changes: "allow" | "ask" | "block";
};

// Connector action rules: GET and PUT /api/connectors/{id}/rules.
export type RulePolicy = "allow" | "ask" | "block";

/** Delete is fixed at ask by the server, so it is read only here. */
export type ConnectorRules = {
  read: RulePolicy;
  change: RulePolicy;
  delete: "ask";
};

export type ConnectorRulesInput = {
  read: RulePolicy;
  change: RulePolicy;
};

export type ProviderAccount = {
  id: string;
  provider: string;
  name: string;
  adapter: string;
  plan_kind: "metered" | "subscription" | "local";
  base_url?: string;
  status: "Connected" | "Not connected" | "Rate limited" | "Key rejected" | "Offline";
  models: string[];
};

// Usage: GET /api/usage?range=today|7|30|90
//
// Everything is the signed-in person's own usage. Global providers and models
// appear read-only, marked by scope. Every spend field is null when the cost is
// not known (a call with no configured price); null is never sent as 0.
export type UsageRange = "today" | 7 | 30 | 90;

/** "hour" buckets for today, "day" buckets for 7, 30 and 90. */
export type UsageBucketUnit = "hour" | "day";

export type UsageProviderShare = {
  spend_usd: number | null;
  requests: number;
  errors: number;
  /** input + output + cache_read + cache_write. */
  tokens: number;
};

export type UsageModelShare = {
  spend_usd: number | null;
  /** input + output + cache_read + cache_write. */
  tokens: number;
};

export type UsageBucket = {
  /** UTC start of the bucket: YYYY-MM-DD for a day, YYYY-MM-DDTHH:00:00Z for an hour. */
  at: string;
  requests: number;
  errors: number;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  /** Estimated tokens spent on tool definitions. Part of input, not added to it. */
  tool_defs_est: number;
  /** Null when the cost of the bucket is not known. Never 0 for unknown. */
  spend_usd: number | null;
  /** Keyed by provider id. Providers with no calls in the bucket are absent. */
  by_provider: Record<string, UsageProviderShare>;
  /** Keyed by model id. Models with no calls in the bucket are absent. */
  by_model: Record<string, UsageModelShare>;
};

export type UsageProvider = {
  id: string;
  name: string;
  /** "global" rows are shared and read-only; "own" rows belong to the person. */
  scope: "global" | "own";
  spend_usd: number | null;
  requests: number;
  errors: number;
  /** End to end latency of a call, in milliseconds. */
  latency_ms: { p50: number; p95: number; p99: number };
};

export type UsageModel = {
  id: string;
  name: string;
  provider_id: string;
  spend_usd: number | null;
  /** input + output + cache_read + cache_write. */
  tokens: number;
};

export type UsageTaskModel = {
  model_id: string;
  /** Null when the cost of this model for the task is not known. */
  cost_usd: number | null;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
};

export type UsageTask = {
  task_id: string;
  goal: string;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  /** Null when the cost of the task is not known. Never 0 for unknown. */
  cost_usd: number | null;
  /** The task split by model. Token fields add up to the task's, costs to cost_usd. */
  by_model: UsageTaskModel[];
};

export type Usage = {
  range: UsageRange;
  bucket: UsageBucketUnit;
  /** Calls in the range with no configured price. They are left out of every spend figure. */
  unpriced_calls: number;
  /** Oldest first. Empty when nothing was recorded in the range. */
  buckets: UsageBucket[];
  providers: UsageProvider[];
  models: UsageModel[];
  tasks: UsageTask[];
};

export class BrainError extends Error {
  readonly status: number;
  readonly error?: string;

  constructor(status: number, error?: string, message?: string) {
    super(message ?? error ?? `HTTP ${status}`);
    this.name = "BrainError";
    this.status = status;
    this.error = error;
  }
}

export interface TestResult {
  ok: boolean;
  latency_ms?: number;
  error?: string;
  model?: string;
}

export interface ApiClient {
  /**
   * GET /api/tasks and GET /api/tasks/{id}. A task whose sandbox is prepared
   * carries `preparing: {steps, runtime}`: clone, toolchain and probe steps with
   * their state, message and timestamps, and the runtime settings. A task
   * without one has no block.
   */
  listTasks(): Promise<Task[]>;
  /**
   * GET /api/tasks/{id}: the task in full, with its events, subtasks and
   * result. A task the person cannot see is a BrainError 404.
   */
  getTask(taskId: string): Promise<Task>;
  /**
   * GET /api/notifications?limit=N: newest first, at most `limit` (default 50,
   * max 100) and never one that was dismissed. task_id is left out when the
   * notification is about no task.
   */
  listNotifications(options?: { limit?: number }): Promise<AppNotification[]>;
  /** DELETE /api/notifications/{id}: 204, again when it was dismissed already. */
  dismissNotification(id: string): Promise<void>;
  nextPhase(taskId: string): Promise<Task>;
  /**
   * POST /api/tasks {goal, repo?, tool_classes?} answers 201 {id, status, goal}.
   * 503 when the task needs a sandbox and none is configured.
   */
  createTask(goal: string, repo?: string): Promise<NewTask>;
  /**
   * POST /api/tasks/{id} {kind: "chat", body}. The message shows up as a task
   * event of type "message".
   */
  postTaskMessage(taskId: string, body: string): Promise<TaskEvent>;
  approvals: {
    /**
     * GET /api/approvals: pending first, newest first, at most 50. A closed row
     * (its task ended while it waited) is never listed.
     */
    list(): Promise<Approval[]>;
    /**
     * POST /api/approvals/{id}/approve. Resumes the task and answers the
     * approval with its new state. 409 when it was decided already or the task
     * no longer waits.
     */
    approve(id: string): Promise<Approval>;
    /** POST /api/approvals/{id}/deny, with the same refusals as approve. */
    deny(id: string): Promise<Approval>;
  };
  session: {
    get(): Promise<CurrentUser>;
  };
  me: {
    current(): Promise<CurrentUser>;
    signIn(method: "passkey" | "password"): Promise<CurrentUser>;
    dismissPasskeyOffer(): Promise<void>;
    viewAs(role: ViewRole): Promise<CurrentUser>;
  };
  passkeys: {
    list(): Promise<Passkey[]>;
    add(name: string): Promise<Passkey>;
    rename(id: string, name: string): Promise<Passkey>;
    remove(id: string): Promise<void>;
  };
  password: {
    set(): Promise<void>;
    change(): Promise<void>;
    remove(): Promise<void>;
  };
  members: {
    list(): Promise<Member[]>;
    invite(input: { email: string; role: InviteRole }): Promise<Member>;
    setRole(id: string, role: WorkspaceRole): Promise<Member>;
    remove(id: string): Promise<void>;
  };
  shares: {
    /** The teams a task can be shared with. Backend: GET /api/teams. */
    teams(): Promise<Team[]>;
    /** GET /api/tasks/{id}/share */
    get(taskId: string): Promise<TaskShare>;
    /**
     * PUT /api/tasks/{id}/share. Owner only: anyone else gets a BrainError 403,
     * which is a refusal and not a sign-out.
     */
    put(taskId: string, update: ShareUpdate): Promise<TaskShare>;
  };
  knowledge: KnowledgeApi;
  tokens: {
    list(): Promise<ApiToken[]>;
    create(input: { name: string; scopes: string[]; days: number }): Promise<CreatedToken>;
    revoke(id: string): Promise<void>;
  };
  connectors: {
    list(): Promise<ConnectorGrant[]>;
    put(grant: ConnectorGrant): Promise<ConnectorGrant>;
    remove(connector: string): Promise<void>;
    test(config: { connector: string; token?: string; endpoint?: string }): Promise<TestResult>;
    rules: {
      /** GET /api/connectors/{id}/rules. 404 when the connector is not connected. */
      get(connector: string): Promise<ConnectorRules>;
      /**
       * PUT /api/connectors/{id}/rules {read, change}. Delete is not sent: it
       * stays at ask. 404 when the connector is not connected, 403 for a global
       * connector, 400 for an unknown policy. A refusal is a BrainError and
       * does not end the session.
       */
      put(connector: string, rules: ConnectorRulesInput): Promise<ConnectorRules>;
    };
  };
  providers: {
    list(): Promise<ProviderAccount[]>;
    add(input: {
      provider: string;
      name: string;
      adapter: string;
      plan_kind: "metered" | "subscription" | "local";
      base_url?: string;
      api_key?: string;
      models: string[];
    }): Promise<ProviderAccount>;
    update(
      id: string,
      input: {
        name?: string;
        plan_kind?: "metered" | "subscription" | "local";
        base_url?: string;
        api_key?: string;
        models?: string[];
      },
    ): Promise<ProviderAccount>;
    remove(id: string): Promise<void>;
    test(config: {
      provider: string;
      api_key?: string;
      base_url?: string;
      model?: string;
    }): Promise<TestResult>;
  };
  chats: {
    /** GET /api/chats, newest activity first. */
    list(): Promise<ChatSummary[]>;
    /** POST /api/chats. An empty chat titled "New chat". */
    create(): Promise<ChatSummary>;
    /** GET /api/chats/{id}. Lines are oldest first. */
    get(id: string): Promise<Chat>;
    /**
     * POST /api/chats/{id} with {body}. Resolves once the line is stored (202).
     * The assistant line is written later: read the chat again to see it.
     */
    send(id: string, body: string): Promise<void>;
  };
  // Models and routes. Every signed-in person can read both. Each row carries
  // scope: global rows are read-only for everyone, own rows belong to the person.
  gateway: {
    /** GET /api/gateway/routes */
    routes(): Promise<GatewayRoute[]>;
  };
  decisions: {
    /** GET /api/decisions/gates */
    gates(): Promise<DecisionGate[]>;
    /**
     * GET /api/decisions. Every decision with the model and route it is bound
     * to (null when unbound) and its last eval (null when never evaluated).
     */
    list(): Promise<DecisionBinding[]>;
  };
  /** GET /api/usage?range=today|7|30|90 */
  usage(range: UsageRange): Promise<Usage>;

  /** Files a task produced: GET /api/tasks/{id}/artifacts and GET /api/artifacts/{id}/download. */
  artifacts: ArtifactsApi;
}

export const TASKS_KEY = ["tasks"] as const;
export const APPROVALS_KEY = ["approvals"] as const;
export const NOTIFICATIONS_KEY = ["notifications"] as const;
export const SESSION_KEY = ["session"] as const;
export const PASSKEYS_KEY = ["passkeys"] as const;
export const MEMBERS_KEY = ["members"] as const;
export const TEAMS_KEY = ["teams"] as const;
export const TOKENS_KEY = ["tokens"] as const;
export const CONNECTORS_KEY = ["connectors"] as const;
export const PROVIDERS_KEY = ["providers"] as const;
export const CHATS_KEY = ["chats"] as const;

export const USAGE_KEY = ["usage"] as const;

export function usageQueryKey(range: UsageRange) {
  return [...USAGE_KEY, range] as const;
}

export function tasksQueryKey() {
  return TASKS_KEY;
}

/**
 * One task in full. It sits under TASKS_KEY, so invalidating the task list
 * invalidates every open task with it.
 */
export function taskQueryKey(taskId: string) {
  return [...TASKS_KEY, taskId] as const;
}

export function approvalsQueryKey() {
  return APPROVALS_KEY;
}

export function notificationsQueryKey() {
  return NOTIFICATIONS_KEY;
}

export function meQueryKey() {
  return SESSION_KEY;
}

export function connectorsQueryKey() {
  return CONNECTORS_KEY;
}

export function connectorRulesQueryKey(connector: string) {
  return [...CONNECTORS_KEY, "rules", connector] as const;
}

export function providersQueryKey() {
  return PROVIDERS_KEY;
}

export const GATEWAY_ROUTES_KEY = ["gateway", "routes"] as const;
export const DECISION_GATES_KEY = ["decisions", "gates"] as const;
export const DECISIONS_KEY = ["decisions", "list"] as const;

export function gatewayRoutesQueryKey() {
  return GATEWAY_ROUTES_KEY;
}

export function decisionGatesQueryKey() {
  return DECISION_GATES_KEY;
}

export function decisionsQueryKey() {
  return DECISIONS_KEY;
}

export function sessionQueryKey() {
  return SESSION_KEY;
}

export function passkeysQueryKey() {
  return PASSKEYS_KEY;
}

export function membersQueryKey() {
  return MEMBERS_KEY;
}

export function shareQueryKey(taskId: string) {
  return ["share", taskId] as const;
}

export function teamsQueryKey() {
  return TEAMS_KEY;
}

export function tokensQueryKey() {
  return TOKENS_KEY;
}

export const KNOWLEDGE_FOLDERS_KEY = ["knowledge", "folders"] as const;
export const KNOWLEDGE_SEARCH_KEY = ["knowledge", "search"] as const;

export function knowledgeFoldersQueryKey() {
  return KNOWLEDGE_FOLDERS_KEY;
}

export function knowledgeSourcesQueryKey(folderId: string) {
  return ["knowledge", "sources", folderId] as const;
}

export function knowledgeSearchQueryKey(folderId: string, query: string) {
  return ["knowledge", "search", folderId, query] as const;
}

export function chatsQueryKey() {
  return CHATS_KEY;
}

export function chatQueryKey(id: string) {
  return ["chat", id] as const;
}

export const ARTIFACTS_KEY = ["artifacts"] as const;

export function taskArtifactsQueryKey(taskId: string) {
  return [...ARTIFACTS_KEY, "task", taskId] as const;
}

export function artifactTextQueryKey(id: string) {
  return [...ARTIFACTS_KEY, "text", id] as const;
}

export function artifactDiffQueryKey(id: string) {
  return [...ARTIFACTS_KEY, "diff", id] as const;
}

export function useWrite<TData, TVariables = void>(
  write: (variables: TVariables) => Promise<TData>,
  keys: readonly (readonly unknown[])[],
  options?: {
    shown?: boolean;
    onSuccess?: (data: TData, variables: TVariables) => void | Promise<void>;
    onError?: (error: Error, variables: TVariables) => void | Promise<void>;
  },
) {
  const queryClient = useQueryClient();
  return useMutation<TData, Error, TVariables>({
    mutationFn: write,
    meta: {
      shown: options?.shown ?? false,
      keys,
    },
    onSuccess: async (data, variables) => {
      for (const key of keys) {
        await queryClient.invalidateQueries({ queryKey: key });
      }
      await options?.onSuccess?.(data, variables);
    },
    onError: async (error, variables) => {
      await options?.onError?.(error, variables);
    },
  });
}

/** JSON round-trip so a caller cannot change the stored record. */
function copy<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

const taskFixtures: Task[] = [
  {
    id: "aster-digest",
    title: "Daily finance digest",
    status: "Needs approval",
    phase: "Planning",
    result: "The daily finance digest is ready.",
    detail: "Send the daily finance digest.",
    cost: "€2.10",
    tokens: 38200,
    spendLimit: "€5.00",
    spentCents: 210,
    limitCents: 500,
    pausePercent: 90,
    runNumber: 14,
    model: "gpt-5.2",
    engine: "Upmo",
    startedAt: "08:00",
    deliveryChannel: "#finance on Slack",
    deliveryWhen: "After the task is done",
    sources: ["Finance mailbox", "finance-policies", "vendor-contracts"],
    runHistory: [
      { run: 13, status: "Failed" },
      { run: 12, status: "Done" },
    ],
    artifacts: [
      {
        id: "digest-draft",
        name: "digest-draft.md",
        kind: "markdown",
        size: "2.8 KB",
        time: "08:03",
        content: `## October invoices over threshold

Three of 31 invoices are over €1,000. Total spend this month is **€18,420**.

- Hetzner: €2,140, due 24 Oct
- Vercel: €1,310, due 28 Oct
- Acme Design: €1,050, due 31 Oct

Draft. Upmo sends it to finance after approval.`,
      },
      {
        id: "invoices-oct",
        name: "invoices-oct.csv",
        kind: "csv",
        size: "12 KB",
        time: "08:01",
        content: `Vendor,Amount,Due
Hetzner,€2,140.00,24 Oct
Vercel,€1,310.00,28 Oct
Acme Design,€1,050.00,31 Oct
GitHub,€420.00,1 Nov
Linear,€192.00,3 Nov`,
      },
    ],
    events: [
      { id: "e1", time: "08:00:00", phase: "Preparing", message: "Task started by an automation" },
      { id: "e2", time: "08:00:02", phase: "Preparing", message: "Found 2 facts in memory", detail: ["Hetzner invoices arrive on the 20th of each month.", "Finance wants invoices over €1,000 flagged."] },
      { id: "e3", time: "08:00:04", phase: "Preparing", message: "Files ready: 2 sources read", detail: ["Finance mailbox", "finance-policies"] },
      { id: "e4", time: "08:00:16", phase: "Planning", message: "Plan written: 4 steps", detail: ["1. Fetch October invoices", "2. Extract vendor, amount, due date", "3. Match contracts and flag over threshold", "4. Write the digest and email finance"] },
      { id: "e5", time: "08:01:02", phase: "Working", message: "Step 1 of 4 · Fetch October invoices" },
      { id: "e6", time: "08:01:15", phase: "Working", message: "Model turn done · 12.9 s · Upmo · gpt-5.2 · 6 tool calls" },
      { id: "e7", time: "08:01:16", phase: "Working", message: "Artifact saved: invoices-oct.csv" },
      { id: "e8", time: "08:02:51", phase: "Working", message: "Model turn done · 9.4 s · Upmo · gpt-5.2 · 1 tool call", tools: [{ id: "t1", name: "write_artifact", status: "success", args: { name: "digest-draft.md", kind: "markdown" }, result: { saved: true, size: "2.8 KB" }, duration: "0.4 s" }] },
      { id: "e9", time: "08:03:05", phase: "Working", message: "Artifact saved: digest-draft.md" },
      { id: "e10", time: "08:03:06", phase: "Working", message: "Approval asked: email to 3 external recipients", detail: ["ap@hetzner.com", "billing@vercel.com", "lead@acme.example"] },
    ],
    subtasks: [{ id: "digest-figures", title: "Pull yesterday's figures", status: "Needs approval" }],
    preparing: preparingReady,
  },
  {
    id: "aster-mail",
    title: "Mail triage",
    status: "Working",
    phase: "Preparing",
    result: "Mail triage is finished.",
    detail: "Sort the morning mail.",
    cost: "€0.45",
    tokens: 8900,
    spendLimit: "€2.00",
    spentCents: 45,
    limitCents: 200,
    pausePercent: 85,
    runNumber: 3,
    model: "gpt-5.2",
    engine: "Upmo",
    startedAt: "08:15",
    deliveryChannel: "#general on Slack",
    deliveryWhen: "After the task is done",
    sources: ["Support mailbox", "mail-rules"],
    runHistory: [
      { run: 2, status: "Done" },
      { run: 1, status: "Done" },
    ],
    artifacts: [
      {
        id: "triage-notes",
        name: "triage-notes.md",
        kind: "markdown",
        size: "1.4 KB",
        time: "08:16",
        content: `## Morning Mail Triage

- 24 messages checked
- 3 invoices forwarded to finance
- 2 tickets escalated to tier 2`,
      },
    ],
    events: [
      { id: "em1", time: "08:15:00", phase: "Preparing", message: "Started mail triage" },
      { id: "em2", time: "08:15:05", phase: "Preparing", message: "Connected to Support mailbox" },
    ],
    subtasks: [
      {
        id: "mail-read",
        title: "Read the mailbox",
        status: "Done",
        children: [{ id: "mail-flag", title: "Flag the invoices", status: "Working" }],
      },
      { id: "mail-draft", title: "Draft the replies", status: "Working" },
    ],
    preparing: preparingRunning,
  },
  {
    id: "aster-done",
    title: "File the receipts",
    status: "Done",
    phase: "Checking",
    result: "The receipts are filed.",
    detail: "File the receipts for Kodeeo Limited.",
    cost: "€1.20",
    tokens: 24100,
    spendLimit: "€3.00",
    spentCents: 120,
    limitCents: 300,
    pausePercent: 90,
    runNumber: 8,
    model: "gpt-5.2",
    engine: "Upmo",
    startedAt: "07:30",
    deliveryChannel: "Accounting drive",
    deliveryWhen: "Immediately",
    sources: ["Receipts folder"],
    runHistory: [
      { run: 7, status: "Done" },
      { run: 6, status: "Done" },
    ],
    artifacts: [
      {
        id: "receipts-filed",
        name: "receipts-filed.csv",
        kind: "csv",
        size: "8.5 KB",
        time: "07:32",
        content: `Receipt ID,Category,Amount
REC-001,Travel,€84.50
REC-002,Software,€49.00
REC-003,Meals,€32.00`,
      },
    ],
    events: [
      { id: "ed1", time: "07:30:00", phase: "Preparing", message: "Task started by you" },
      { id: "ed2", time: "07:30:04", phase: "Preparing", message: "Files ready: 1 source read", detail: ["Receipts folder"] },
      { id: "ed3", time: "07:30:16", phase: "Planning", message: "Plan written: 2 steps", detail: ["1. Read each receipt", "2. File it under its category"] },
      { id: "ed4", time: "07:31:02", phase: "Working", message: "Step 1 of 2 · Read each receipt" },
      { id: "ed5", time: "07:32:10", phase: "Working", message: "Artifact saved: receipts-filed.csv" },
      { id: "ed6", time: "07:32:40", phase: "Checking", message: "Checks passed: 3 of 3 receipts filed" },
    ],
    preparing: preparingReadyBare,
  },
  {
    id: "aster-failed",
    title: "Weekly close",
    status: "Failed",
    phase: "Checking",
    result: "",
    detail: "Close the week for Kodeeo Limited.",
    cost: "€0.85",
    tokens: 14300,
    spendLimit: "€5.00",
    spentCents: 85,
    limitCents: 500,
    pausePercent: 90,
    runNumber: 5,
    model: "gpt-5.2",
    engine: "Upmo",
    startedAt: "06:00",
    deliveryChannel: "#finance on Slack",
    deliveryWhen: "On error",
    sources: ["Ledger database"],
    runHistory: [{ run: 4, status: "Done" }],
    events: [
      { id: "ef1", time: "06:00:00", phase: "Preparing", message: "Task started by an automation" },
      { id: "ef2", time: "06:00:05", phase: "Preparing", message: "Files ready: 1 source read", detail: ["Ledger database"] },
      { id: "ef3", time: "06:00:18", phase: "Planning", message: "Plan written: 3 steps", detail: ["1. Pull the week's entries", "2. Reconcile the accounts", "3. Close the period"] },
      { id: "ef4", time: "06:01:30", phase: "Working", message: "Step 2 of 3 · Reconcile the accounts" },
      { id: "ef5", time: "06:03:12", phase: "Checking", message: "Check failed: 2 accounts do not reconcile" },
    ],
    preparing: preparingReadyBare,
  },
  {
    id: "aster-probe",
    title: "Fix the flaky login test",
    status: "Failed",
    phase: "Preparing",
    result: "",
    detail: "Find out why the login test fails now and then.",
    cost: "€0.02",
    tokens: 600,
    spendLimit: "€3.00",
    spentCents: 2,
    limitCents: 300,
    pausePercent: 90,
    runNumber: 1,
    model: "gpt-5.2",
    engine: "Upmo",
    startedAt: "09:10",
    sources: ["upmo-ai/upmo"],
    events: [
      { id: "ep1", time: "09:10:00", phase: "Preparing", message: "Task started by you" },
      { id: "ep2", time: "09:10:05", phase: "Preparing", message: "Probe failed: the work directory is not writable" },
    ],
    preparing: preparingProbeFailed,
  },
];

const notificationFixtures: AppNotification[] = [
  { id: "aster-failed", kind: "failed", title: "Weekly close failed", taskId: "aster-failed" },
  { id: "aster-finished", kind: "finished", title: "Mail triage done", taskId: "aster-mail" },
  { id: "aster-proposal", kind: "proposal", title: "Archive the filed receipts", taskId: "aster-done" },
  { id: "aster-question", kind: "question", title: "Question about the budget" },
];

const approvalFixtures: Approval[] = [
  {
    id: "aster-approval",
    taskId: "aster-digest",
    title: "Approve the digest",
    action: "Send the daily finance digest",
    state: "pending",
  },
];

const memberSeed: Member[] = [
  {
    id: "ada",
    name: "Ada Okonkwo",
    email: "ada@aster.example",
    role: "owner",
    teams: ["Finance"],
  },
  {
    id: "sam",
    name: "Sam Rivera",
    email: "sam@aster.example",
    role: "admin",
    teams: ["Finance"],
  },
  {
    id: "lee",
    name: "Lee Chen",
    email: "lee@aster.example",
    role: "member",
    teams: ["Ops"],
  },
  {
    id: "noor",
    name: "Noor Ali",
    email: "noor@aster.example",
    role: "viewer",
    teams: ["Ops"],
  },
];

const teamFixtures: Team[] = [
  { id: "finance", name: "Finance" },
  { id: "ops", name: "Ops" },
];

const viewPersona: Record<ViewRole, string> = {
  admin: "sam",
  member: "lee",
  viewer: "noor",
};

interface StoredShare {
  visibility: TaskVisibility;
  members: ShareGrant[];
}

function asterShare(): StoredShare {
  return {
    visibility: "private",
    members: [
      { subject_type: "person", subject_id: "sam", role: "owner" },
      { subject_type: "person", subject_id: "lee", role: "viewer" },
    ],
  };
}

// Checking is the last step a person can ask for. The checks decide when a
// task is Done, so advancing from Checking is refused, as the backend does.
function blocked(task: Task, approvals: Approval[]) {
  if (task.status === "Needs approval" || task.status === "Done" || task.status === "Failed") {
    return true;
  }
  if (task.phase === "Checking") return true;
  return approvals.some((item) => item.taskId === task.id && item.state === "pending");
}

function advance(task: Task) {
  const index = phases.indexOf(task.phase);
  if (index < 0 || index >= phases.length - 1) return;
  // Leaving Preparing means every step ran, so the payload no longer says one is waiting.
  if (task.phase === "Preparing" && task.preparing) finishPreparing(task.preparing);
  task.phase = phases[index + 1] as Phase;
  task.status = "Working";
}

function clock(date: Date) {
  return date.toTimeString().slice(0, 8);
}

function isVisibility(value: string): value is TaskVisibility {
  return value === "private" || value === "team" || value === "everyone";
}

/** Where a member's dismissal of the passkey offer is kept between page loads. */
export function passkeyOfferStorageKey(memberId: string): string {
  return `upmo-passkey-offer-dismissed:${memberId}`;
}

export interface StubOptions {
  /** How long the stub takes to write an assistant reply, in milliseconds. */
  replyDelayMs?: number;
  knowledgeIngestMs?: number;
  /** Start with no usage recorded, so the usage page shows its empty states. */
  emptyUsage?: boolean;
}

export function createStubApi(options: StubOptions = {}): ApiClient {
  const tasks = copy(taskFixtures);
  let notifications = copy(notificationFixtures);
  const approvals = copy(approvalFixtures);
  const members = copy(memberSeed);
  const teams = copy(teamFixtures);
  const shares: Record<string, StoredShare> = {
    "aster-digest": asterShare(),
    "aster-mail": {
      visibility: "team",
      members: [
        { subject_type: "person", subject_id: "sam", role: "owner" },
        { subject_type: "team", subject_id: "finance", role: "editor" },
      ],
    },
    "aster-done": asterShare(),
    "aster-failed": asterShare(),
    "aster-probe": asterShare(),
  };
  const passkeys: Passkey[] = [
    {
      id: "pk-laptop",
      name: "Laptop",
      added: "2026-09-01T00:00:00.000Z",
      lastUsed: "2026-10-28T00:00:00.000Z",
    },
  ];
  const tokens: ApiToken[] = [];
  const connectorGrants: ConnectorGrant[] = [
    {
      connector: "github",
      scope: "own",
      resource_scope: "all",
      resource_ids: [],
      changes: "ask",
    },
    {
      connector: "slack",
      scope: "own",
      resource_scope: "these",
      resource_ids: ["#general", "#engineering"],
      changes: "allow",
    },
    {
      connector: "google-drive",
      scope: "global",
      resource_scope: "all",
      resource_ids: [],
      changes: "ask",
    },
  ];
  // The stored read rule per connector. The change rule is the grant's own
  // changes policy, so the card and the rules never disagree. A connector with
  // no stored read rule reads as the server decides it: allowed.
  const connectorReadRules = new Map<string, RulePolicy>([["github", "allow"]]);
  const connectorSlug = /^[a-z][a-z0-9-]{0,63}$/;
  const rulePolicies: readonly string[] = ["allow", "ask", "block"];

  function connectorGrantFor(connector: string): ConnectorGrant {
    if (!connectorSlug.test(connector)) throw new BrainError(400, "bad_request", "Unknown connector.");
    const grant = connectorGrants.find((item) => item.connector === connector);
    if (!grant) throw new BrainError(404, "not_found", "That connector is not connected.");
    return grant;
  }

  function connectorRulesFor(connector: string): ConnectorRules {
    const grant = connectorGrantFor(connector);
    return {
      read: connectorReadRules.get(connector) ?? "allow",
      change: grant.changes,
      delete: "ask",
    };
  }

  function ownConnectorFor(connector: string): ConnectorGrant {
    const grant = connectorGrantFor(connector);
    if (grant.scope === "global") {
      throw new BrainError(403, "forbidden", "Global connectors are managed by the workspace.");
    }
    return grant;
  }
  const providerAccounts: ProviderAccount[] = [
    {
      id: "provider-anthropic",
      provider: "anthropic",
      name: "Anthropic Claude",
      adapter: "anthropic",
      plan_kind: "metered",
      status: "Connected",
      models: ["claude-3-7-sonnet", "claude-3-5-haiku"],
    },
    {
      id: "provider-ollama",
      provider: "ollama",
      name: "Ollama Local",
      adapter: "ollama",
      plan_kind: "local",
      base_url: "http://localhost:11434",
      status: "Connected",
      models: ["llama3.3:70b", "deepseek-r1:14b"],
    },
  ];
  let serial = 0;
  let passwordSet = false;
  let signIn: CurrentUser["signIn"] = null;
  // Members whose offer was dismissed in this page, for when storage is blocked.
  const dismissedHere = new Set<string>();
  let view: ViewRole = "admin";
  let currentMemberId: string = viewPersona.admin;

  function nextId(prefix: string) {
    serial += 1;
    return `${prefix}-${serial}`;
  }

  function offerDismissed(): boolean {
    if (dismissedHere.has(currentMemberId)) return true;
    try {
      return localStorage.getItem(passkeyOfferStorageKey(currentMemberId)) === "1";
    } catch {
      return false;
    }
  }

  function dismissOffer() {
    dismissedHere.add(currentMemberId);
    try {
      localStorage.setItem(passkeyOfferStorageKey(currentMemberId), "1");
    } catch {
      // Storage is blocked: the offer stays dismissed for this page only.
    }
  }

  // A new sign-in starts fresh: the brain decides again whether to offer a passkey.
  function clearOffer() {
    dismissedHere.delete(currentMemberId);
    try {
      localStorage.removeItem(passkeyOfferStorageKey(currentMemberId));
    } catch {
      // Storage is blocked: only the in-page flag was kept.
    }
  }

  // passkeyCount is only ever what /api/session reported; unknown stays undefined.
  function currentUser(passkeyCount?: number): CurrentUser {
    return {
      memberId: currentMemberId,
      view,
      signIn,
      passwordSet,
      ...(passkeyCount === undefined ? {} : { passkeyCount }),
      offerDismissed: offerDismissed(),
    };
  }

  function findMember(id: string) {
    return members.find((item) => item.id === id);
  }

  function actor() {
    const member = findMember(currentMemberId);
    if (!member) throw new Error("member missing");
    return member;
  }

  function ownerCount() {
    return members.filter((item) => item.role === "owner").length;
  }

  function mustShare(taskId: string) {
    const share = shares[taskId];
    if (!share) throw new BrainError(404, "not_found", "This task is not in the workspace.");
    return share;
  }

  function presentShare(taskId: string): TaskShare {
    const share = mustShare(taskId);
    return copy({
      visibility: share.visibility,
      members: share.members.map((grant) => ({
        ...grant,
        name:
          (grant.subject_type === "team"
            ? teams.find((team) => team.id === grant.subject_id)?.name
            : findMember(grant.subject_id)?.name) ?? grant.subject_id,
      })),
    });
  }

  /** Checks an owner-only write the way the backend does, before anything is stored. */
  function checkShareUpdate(share: StoredShare, update: ShareUpdate) {
    const held = share.members.find(
      (grant) => grant.subject_type === "person" && grant.subject_id === currentMemberId,
    );
    if (!canEditSharing(held?.role ?? null)) {
      throw new BrainError(403, "forbidden", "Only the owner can change sharing.");
    }
    if (!isVisibility(update.visibility)) throw new BrainError(422, "invalid", "Unknown visibility.");
    const seen = new Set<string>();
    for (const grant of update.members) {
      if (grant.subject_type !== "person" && grant.subject_type !== "team") {
        throw new BrainError(422, "invalid", "Share with a person or a team.");
      }
      const key = `${grant.subject_type}:${grant.subject_id}`;
      if (seen.has(key)) throw new BrainError(422, "invalid", "That access is listed twice.");
      seen.add(key);
      if (grant.subject_type === "person" && !findMember(grant.subject_id)) {
        throw new BrainError(422, "invalid", "That person is not a member.");
      }
      if (grant.subject_type === "team" && !teams.some((team) => team.id === grant.subject_id)) {
        throw new BrainError(422, "invalid", "That team does not exist.");
      }
      const stored = share.members.find(
        (item) => item.subject_type === grant.subject_type && item.subject_id === grant.subject_id,
      );
      if (stored?.role === "owner") {
        if (grant.role !== "owner") throw new BrainError(422, "invalid", "The owner cannot be changed.");
      } else if (!isGrantableShareLevel(grant.role)) {
        throw new BrainError(422, "invalid", "Owner is not a share level.");
      }
    }
    for (const stored of share.members) {
      if (stored.role === "owner" && !seen.has(`${stored.subject_type}:${stored.subject_id}`)) {
        throw new BrainError(422, "invalid", "The owner cannot be removed.");
      }
    }
  }

  function addPasskey(name: string) {
    const trimmed = name.trim();
    if (!trimmed) throw new Error("Name the passkey.");
    const stamp = new Date().toISOString();
    const passkey: Passkey = { id: nextId("pk"), name: trimmed, added: stamp, lastUsed: stamp };
    passkeys.push(passkey);
    return passkey;
  }

  function settle(id: string, state: "approved" | "denied"): Approval {
    const approval = approvals.find((item) => item.id === id);
    if (!approval) throw new Error("approval missing");
    if (approval.state === "pending") {
      approval.state = state;
      const task = tasks.find((item) => item.id === approval.taskId);
      if (task && task.status === "Needs approval") task.status = "Working";
    }
    return copy(approval);
  }

  async function readSession(): Promise<CurrentUser> {
    try {
      const response = await fetch("/api/session", { credentials: "include" });
      if (response.status === 401) {
        let body: { error?: string } = {};
        try {
          body = (await response.json()) as { error?: string };
        } catch {
          // not json
        }
        if (body.error === "unauthorised") {
          throw new BrainError(401, "unauthorised");
        }
      }
      if (response.ok) {
        const body = (await response.json()) as {
          sign_in_method?: SignInMethod;
          has_password?: boolean;
          passkey_count?: number;
        };
        if (
          body.sign_in_method === "passkey" ||
          body.sign_in_method === "password" ||
          body.sign_in_method === "link"
        ) {
          signIn = body.sign_in_method;
        } else if (body.sign_in_method == null && signIn == null) {
          signIn = "password";
        }
        if (body.has_password !== undefined) {
          passwordSet = Boolean(body.has_password);
        }
        if (typeof body.passkey_count === "number" && body.passkey_count >= 0) {
          return copy(currentUser(body.passkey_count));
        }
      }
    } catch (err) {
      if (err instanceof BrainError) {
        throw err;
      }
    }
    return copy(currentUser());
  }

  return {
    async listTasks() {
      return tasks.map((task) => copy(task));
    },
    async getTask(taskId) {
      const task = tasks.find((item) => item.id === taskId);
      if (!task) throw new BrainError(404, "not_found", "This task is not in the workspace.");
      return copy(task);
    },
    async listNotifications(query) {
      const limit = query?.limit ?? notifications.length;
      return notifications.slice(0, limit).map((item) => copy(item));
    },
    async dismissNotification(id) {
      if (!notifications.some((item) => item.id === id)) throw new Error("notification missing");
      notifications = notifications.filter((item) => item.id !== id);
    },
    async nextPhase(taskId) {
      const task = tasks.find((item) => item.id === taskId);
      if (!task) throw new Error("task missing");
      if (!blocked(task, approvals)) advance(task);
      return copy(task);
    },
    async createTask(goal, repo) {
      const problem = goalProblem(goal.trim()) ?? (repo ? repoProblem(repo) : null);
      if (problem) throw new Error(problem);
      const title = goal.trim();
      const source = repo?.trim();
      const id = nextId("task");
      const task: Task = {
        id,
        title,
        status: "Working",
        phase: "Preparing",
        result: "",
        detail: title,
        events: [{ id: nextId("event"), time: clock(new Date()), phase: "Preparing", message: "Task started by you" }],
        ...(source ? { sources: [source] } : {}),
      };
      tasks.unshift(task);
      shares[id] = {
        visibility: "private",
        members: [{ subject_type: "person", subject_id: currentMemberId, role: "owner" }],
      };
      return { id, status: task.phase, goal: title };
    },
    async postTaskMessage(taskId, body) {
      const problem = messageProblem(body);
      if (problem) throw new Error(problem);
      const task = tasks.find((item) => item.id === taskId);
      if (!task) throw new Error("task missing");
      const event: TaskEvent = {
        id: nextId("event"),
        time: clock(new Date()),
        phase: task.phase,
        type: messageEventType,
        message: body.trim(),
      };
      task.events = [...(task.events ?? []), event];
      return copy(event);
    },
    approvals: {
      async list() {
        return approvals.map((item) => copy(item));
      },
      async approve(id) {
        return settle(id, "approved");
      },
      async deny(id) {
        return settle(id, "denied");
      },
    },
    session: {
      get: readSession,
    },
    me: {
      current: readSession,
      async signIn(method) {
        if (method !== "passkey" && method !== "password") throw new Error("Unknown sign-in.");
        signIn = method;
        clearOffer();
        return copy(currentUser());
      },
      async dismissPasskeyOffer() {
        dismissOffer();
      },
      async viewAs(role) {
        if (!isViewRole(role)) throw new Error("You cannot view as the owner.");
        view = role;
        currentMemberId = viewPersona[role];
        return copy(currentUser());
      },
    },
    passkeys: {
      async list() {
        return passkeys.map((item) => copy(item));
      },
      async add(name) {
        return copy(addPasskey(name));
      },
      async rename(id, name) {
        const passkey = passkeys.find((item) => item.id === id);
        if (!passkey) throw new Error("Passkey missing.");
        const trimmed = name.trim();
        if (!trimmed) throw new Error("Name the passkey.");
        passkey.name = trimmed;
        return copy(passkey);
      },
      async remove(id) {
        if (!canRemovePasskey(passkeys.length, passwordSet)) throw new Error(lastPasskeyReason);
        const index = passkeys.findIndex((item) => item.id === id);
        if (index < 0) throw new Error("Passkey missing.");
        passkeys.splice(index, 1);
      },
    },
    password: {
      async set() {
        passwordSet = true;
      },
      async change() {
        if (!passwordSet) throw new Error("No password is set.");
      },
      async remove() {
        if (!canRemovePassword(passkeys.length, passwordSet)) throw new Error(lastPasswordReason);
        passwordSet = false;
      },
    },
    members: {
      async list() {
        return members.map((item) => copy(item));
      },
      async invite(input) {
        const acting = actor();
        if (!canManageMembers(acting.role)) throw new Error("You cannot manage members.");
        if (!isInviteRole(input.role)) throw new Error("Owner is not a choosable role.");
        const email = input.email.trim();
        if (!email.includes("@")) throw new Error("Enter an email address.");
        if (members.some((item) => item.email === email)) {
          throw new Error("That email is already a member.");
        }
        const local = email.split("@")[0]?.trim();
        const member: Member = {
          id: nextId("member"),
          name: local && local.length > 0 ? local : email,
          email,
          role: input.role,
          teams: [],
        };
        members.push(member);
        return copy(member);
      },
      async setRole(id, role) {
        const acting = actor();
        const target = findMember(id);
        if (!target) throw new Error("member missing");
        const owners = ownerCount();
        if (role === "owner") throw new Error("Owner is not a choosable role.");
        if (target.role === "owner" && !canChangeOwnerRole(acting.role, owners)) {
          throw new Error(ownerLockReason(acting.role, owners) ?? lastOwnerReason);
        }
        if (!canManageMembers(acting.role)) throw new Error("You cannot manage members.");
        if (!isInviteRole(role)) throw new Error("Owner is not a choosable role.");
        target.role = role;
        return copy(target);
      },
      async remove(id) {
        const acting = actor();
        const index = members.findIndex((item) => item.id === id);
        const target = index >= 0 ? members[index] : undefined;
        if (!target) throw new Error("member missing");
        const owners = ownerCount();
        if (target.role === "owner" && !canChangeOwnerRole(acting.role, owners)) {
          throw new Error(ownerLockReason(acting.role, owners) ?? lastOwnerReason);
        }
        if (!canManageMembers(acting.role)) throw new Error("You cannot manage members.");
        members.splice(index, 1);
      },
    },
    shares: {
      async teams() {
        return teams.map((team) => copy(team));
      },
      async get(taskId) {
        return presentShare(taskId);
      },
      async put(taskId, update) {
        const share = mustShare(taskId);
        checkShareUpdate(share, update);
        share.visibility = update.visibility;
        share.members = copy(update.members);
        return presentShare(taskId);
      },
    },
    knowledge: createKnowledgeStub({ ingestMs: options.knowledgeIngestMs }),
    tokens: {
      async list() {
        return tokens.map((token) => copy(token));
      },
      async create(input) {
        const acting = actor();
        const name = input.name.trim();
        if (!name) throw new Error("Name the token.");
        if (!Number.isInteger(input.days) || input.days < 0 || input.days > 365) throw new Error("Choose an expiry.");
        if (input.scopes.length === 0) throw new Error("Choose at least one scope.");
        for (const scope of input.scopes) {
          if (!isAllowedScope(acting.role, scope)) throw new Error("That scope is not allowed.");
        }
        serial += 1;
        const body = `s${serial}7f3a9c2e1b4d`;
        const secret = `upmo_pat_${body}`;
        const hint = `upmo_pat_${body.slice(0, 4)}`;
        const createdAt = new Date();
        const expires = new Date(createdAt);
        expires.setUTCDate(expires.getUTCDate() + input.days);
        const token: ApiToken = {
          id: nextId("token"),
          name,
          hint,
          scopes: copy(input.scopes),
          created: createdAt.toISOString(),
          lastUsed: null,
          expires: expires.toISOString(),
        };
        tokens.push(token);
        return { token: copy(token), secret };
      },
      async revoke(id) {
        const index = tokens.findIndex((token) => token.id === id);
        if (index < 0) throw new Error("Token missing.");
        tokens.splice(index, 1);
      },
    },
    connectors: {
      async list() {
        return connectorGrants.map((item) => copy(item));
      },
      async put(grant) {
        const index = connectorGrants.findIndex((item) => item.connector === grant.connector);
        if (index >= 0) ownConnectorFor(grant.connector);
        // A person can only create or change their own connectors.
        const saved: ConnectorGrant = { ...copy(grant), scope: "own" };
        if (index >= 0) {
          connectorGrants[index] = saved;
        } else {
          connectorGrants.push(saved);
        }
        return copy(saved);
      },
      async remove(connector) {
        const index = connectorGrants.findIndex((item) => item.connector === connector);
        if (index >= 0) {
          ownConnectorFor(connector);
          connectorGrants.splice(index, 1);
        }
        connectorReadRules.delete(connector);
      },
      rules: {
        async get(connector) {
          return connectorRulesFor(connector);
        },
        async put(connector, input) {
          const grant = ownConnectorFor(connector);
          if (!rulePolicies.includes(input.read) || !rulePolicies.includes(input.change)) {
            throw new BrainError(400, "bad_request", "Choose allow, ask or block.");
          }
          connectorReadRules.set(connector, input.read);
          grant.changes = input.change;
          return connectorRulesFor(connector);
        },
      },
      async test(config) {
        if (!config.connector) {
          return { ok: false, error: "Connector is required." };
        }
        if (config.token && config.token.toLowerCase().includes("invalid")) {
          return { ok: false, error: "Authentication failed: Invalid credentials or token." };
        }
        return {
          ok: true,
          latency_ms: 92,
        };
      },
    },
    providers: {
      async list() {
        return providerAccounts.map((item) => copy(item));
      },
      async add(input) {
        const account: ProviderAccount = {
          id: nextId("provider"),
          provider: input.provider,
          name: input.name,
          adapter: input.adapter,
          plan_kind: input.plan_kind,
          base_url: input.base_url,
          status: "Connected",
          models: copy(input.models),
        };
        providerAccounts.push(account);
        return copy(account);
      },
      async update(id, input) {
        const account = providerAccounts.find((item) => item.id === id);
        if (!account) throw new Error("Provider missing.");
        if (input.name !== undefined) account.name = input.name;
        if (input.plan_kind !== undefined) account.plan_kind = input.plan_kind;
        if (input.base_url !== undefined) account.base_url = input.base_url;
        if (input.models !== undefined) account.models = copy(input.models);
        return copy(account);
      },
      async remove(id) {
        const index = providerAccounts.findIndex((item) => item.id === id);
        if (index >= 0) {
          providerAccounts.splice(index, 1);
        }
      },
      async test(config) {
        if (!config.provider) {
          return { ok: false, error: "Provider is required." };
        }
        if (config.api_key && config.api_key.toLowerCase().includes("invalid")) {
          return { ok: false, error: "Authentication failed: Invalid API key or unauthorized." };
        }
        return {
          ok: true,
          latency_ms: 124,
          model: config.model || "default",
        };
      },
    },
    chats: createChatsStub(options.replyDelayMs),
    gateway: {
      async routes() {
        return copy(sampleRoutes);
      },
    },
    decisions: {
      async gates() {
        return copy(sampleGates);
      },
      async list() {
        return copy(sampleDecisions);
      },
    },
    async usage(range) {
      return usageFor(range, { empty: options.emptyUsage });
    },
    artifacts: createArtifactsStub(),
  };
}

const ApiContext = createContext<ApiClient | null>(null);

export function ApiProvider({ client, children }: { client?: ApiClient; children: ReactNode }) {
  const [value] = useState(() => client ?? createStubApi());
  return <ApiContext.Provider value={value}>{children}</ApiContext.Provider>;
}

export function useApi(): ApiClient {
  const api = useContext(ApiContext);
  if (!api) throw new Error("API client missing");
  return api;
}
