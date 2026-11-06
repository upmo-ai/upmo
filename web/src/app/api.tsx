import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useState, type ReactNode } from "react";

import {
  canChangeOwnerRole,
  canEditSharing,
  canManageMembers,
  canRemovePasskey,
  canRemovePassword,
  canRemoveShare,
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
  GrantableShareLevel,
  InviteRole,
  Member,
  Passkey,
  PersonAccess,
  TaskShare,
  TaskVisibility,
  Team,
  ViewRole,
  WorkspaceRole,
} from "../features/identity/types";
import { createKnowledgeStub } from "../features/knowledge/stub";
import type { KnowledgeApi } from "../features/knowledge/types";
import { createChatsStub } from "../features/chat/stub";
import type { Chat, ChatSummary } from "../features/chat/types";
import { canManageSettings } from "../features/models/rules";
import { sampleGates, sampleRoutes } from "../features/models/sample";
import type { DecisionGate, GatewayRoute } from "../features/models/types";
import type { Approval, AppNotification } from "../features/notifications/types";
import { goalProblem, messageProblem, repoProblem } from "../features/tasks/rules";
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
export type { Chat, ChatLine, ChatSummary, LineRole } from "../features/chat/types";
export type {
  DecisionGate,
  GateKind,
  GateVerdict,
  GatewayRoute,
  RouteCapability,
  RouteDecision,
  RouteHop,
} from "../features/models/types";
export type { Approval, AppNotification, NotificationKind } from "../features/notifications/types";
export type { Phase, Task, TaskStatus } from "../features/tasks/types";
export type { NewTask, TaskEvent } from "../features/tasks/types";
export type {
  ApiToken,
  CreatedToken,
  CurrentUser,
  GrantableShareLevel,
  InviteRole,
  Member,
  Passkey,
  ShareLevel,
  TaskShare,
  TaskVisibility,
  Team,
  TokenExpiry,
  ViewRole,
  WorkspaceRole,
} from "../features/identity/types";

export type ConnectorGrant = {
  connector: string;
  resource_scope: "all" | "these";
  resource_ids: string[];
  changes: "allow" | "ask" | "block";
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
  listTasks(): Promise<Task[]>;
  listNotifications(): Promise<AppNotification[]>;
  approveNotification(id: string): Promise<void>;
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
    list(): Promise<Approval[]>;
    approve(id: string): Promise<Approval>;
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
    teams(): Promise<Team[]>;
    get(taskId: string): Promise<TaskShare>;
    setVisibility(
      taskId: string,
      visibility: TaskVisibility,
      teamId: string | null,
    ): Promise<TaskShare>;
    addPerson(taskId: string, memberId: string, level: GrantableShareLevel): Promise<TaskShare>;
    setLevel(taskId: string, memberId: string, level: GrantableShareLevel): Promise<TaskShare>;
    removePerson(taskId: string, memberId: string): Promise<TaskShare>;
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
  // Models and routes. Both reads need settings:manage.
  gateway: {
    /** GET /api/gateway/routes */
    routes(): Promise<GatewayRoute[]>;
  };
  decisions: {
    /** GET /api/decisions/gates */
    gates(): Promise<DecisionGate[]>;
  };
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

export function tasksQueryKey() {
  return TASKS_KEY;
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

export function providersQueryKey() {
  return PROVIDERS_KEY;
}

export const GATEWAY_ROUTES_KEY = ["gateway", "routes"] as const;
export const DECISION_GATES_KEY = ["decisions", "gates"] as const;

export function gatewayRoutesQueryKey() {
  return GATEWAY_ROUTES_KEY;
}

export function decisionGatesQueryKey() {
  return DECISION_GATES_KEY;
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
  },
];

const notificationFixtures: AppNotification[] = [
  { id: "aster-failed", kind: "failed", title: "Weekly close failed" },
  { id: "aster-finished", kind: "finished", title: "Mail triage done" },
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
  teamId: string | null;
  people: PersonAccess[];
}

function asterShare(): StoredShare {
  return {
    visibility: "private",
    teamId: null,
    people: [
      { memberId: "sam", level: "owner" },
      { memberId: "lee", level: "viewer" },
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
  task.phase = phases[index + 1] as Phase;
  task.status = "Working";
}

function clock(date: Date) {
  return date.toTimeString().slice(0, 8);
}

function isVisibility(value: string): value is TaskVisibility {
  return value === "private" || value === "team" || value === "everyone";
}

export interface StubOptions {
  /** How long the stub takes to write an assistant reply, in milliseconds. */
  replyDelayMs?: number;
  knowledgeIngestMs?: number;
}

export function createStubApi(options: StubOptions = {}): ApiClient {
  const tasks = copy(taskFixtures);
  let notifications = copy(notificationFixtures);
  const approvals = copy(approvalFixtures);
  const members = copy(memberSeed);
  const teams = copy(teamFixtures);
  const shares: Record<string, StoredShare> = {
    "aster-digest": asterShare(),
    "aster-mail": asterShare(),
    "aster-done": asterShare(),
    "aster-failed": asterShare(),
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
      resource_scope: "all",
      resource_ids: [],
      changes: "ask",
    },
    {
      connector: "slack",
      resource_scope: "these",
      resource_ids: ["#general", "#engineering"],
      changes: "allow",
    },
  ];
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
  let offerDismissed = false;
  let view: ViewRole = "admin";
  let currentMemberId: string = viewPersona.admin;

  function nextId(prefix: string) {
    serial += 1;
    return `${prefix}-${serial}`;
  }

  function currentUser(): CurrentUser {
    return { memberId: currentMemberId, view, signIn, passwordSet, offerDismissed };
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

  /** A signed-in person without settings:manage is refused, not signed out. */
  function requireSettingsAccess() {
    if (!canManageSettings(actor().role)) {
      throw new BrainError(403, "forbidden", "Needs settings access.");
    }
  }

  function mustShare(taskId: string) {
    const share = shares[taskId];
    if (!share) throw new Error("task missing");
    return share;
  }

  function presentShare(taskId: string): TaskShare {
    const share = mustShare(taskId);
    return copy({ taskId, visibility: share.visibility, teamId: share.teamId, people: share.people });
  }

  function assertCanEditShare(taskId: string) {
    const share = mustShare(taskId);
    const level = share.people.find((person) => person.memberId === currentMemberId)?.level ?? null;
    if (!canEditSharing(level)) throw new Error("You cannot change sharing.");
    return share;
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
          sign_in_method?: "passkey" | "password";
          has_password?: boolean;
        };
        if (body.sign_in_method === "passkey" || body.sign_in_method === "password") {
          signIn = body.sign_in_method;
        } else if (signIn == null) {
          signIn = "password";
        }
        if (body.has_password !== undefined) {
          passwordSet = Boolean(body.has_password);
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
    async listNotifications() {
      return notifications.map((item) => copy(item));
    },
    async approveNotification(id) {
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
        teamId: null,
        people: [{ memberId: currentMemberId, level: "owner" }],
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
        return copy(currentUser());
      },
      async dismissPasskeyOffer() {
        offerDismissed = true;
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
      async setVisibility(taskId, visibility, teamId) {
        const share = assertCanEditShare(taskId);
        if (!isVisibility(visibility)) throw new Error("Unknown visibility.");
        if (visibility === "team") {
          const known = teams.some((team) => team.id === teamId);
          if (!teamId || !known) throw new Error("Choose a team.");
          share.teamId = teamId;
        } else {
          share.teamId = null;
        }
        share.visibility = visibility;
        return presentShare(taskId);
      },
      async addPerson(taskId, memberId, level) {
        const share = assertCanEditShare(taskId);
        if (!isGrantableShareLevel(level)) throw new Error("Owner is not a share level.");
        if (!findMember(memberId)) throw new Error("That person is not a member.");
        if (share.people.some((person) => person.memberId === memberId)) {
          throw new Error("That person already has access.");
        }
        share.people.push({ memberId, level });
        return presentShare(taskId);
      },
      async setLevel(taskId, memberId, level) {
        const share = assertCanEditShare(taskId);
        if (!isGrantableShareLevel(level)) throw new Error("Owner is not a share level.");
        const person = share.people.find((item) => item.memberId === memberId);
        if (!person) throw new Error("That person does not have access.");
        if (!canRemoveShare(person.level)) throw new Error("The owner cannot be removed.");
        person.level = level;
        return presentShare(taskId);
      },
      async removePerson(taskId, memberId) {
        const share = assertCanEditShare(taskId);
        const person = share.people.find((item) => item.memberId === memberId);
        if (!person) throw new Error("That person does not have access.");
        if (!canRemoveShare(person.level)) throw new Error("The owner cannot be removed.");
        share.people = share.people.filter((item) => item.memberId !== memberId);
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
        const saved = copy(grant);
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
          connectorGrants.splice(index, 1);
        }
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
        requireSettingsAccess();
        return copy(sampleRoutes);
      },
    },
    decisions: {
      async gates() {
        requireSettingsAccess();
        return copy(sampleGates);
      },
    },
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
