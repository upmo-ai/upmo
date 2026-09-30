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
import type { Approval, AppNotification } from "../features/notifications/types";
import { phases, type Phase, type Task } from "../features/tasks/types";

export type { Approval, AppNotification, NotificationKind } from "../features/notifications/types";
export type { Phase, Task, TaskStatus } from "../features/tasks/types";
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

export interface ApiClient {
  listTasks(): Promise<Task[]>;
  listNotifications(): Promise<AppNotification[]>;
  approveNotification(id: string): Promise<void>;
  nextPhase(taskId: string): Promise<Task>;
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
  tokens: {
    list(): Promise<ApiToken[]>;
    create(input: { name: string; scopes: string[]; days: number }): Promise<CreatedToken>;
    revoke(id: string): Promise<void>;
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
    subtasks: [{ id: "digest-figures", title: "Pull yesterday's figures", status: "Needs approval" }],
  },
  {
    id: "aster-mail",
    title: "Mail triage",
    status: "Working",
    phase: "Preparing",
    result: "Mail triage is finished.",
    detail: "Sort the morning mail.",
    subtasks: [
      {
        id: "mail-read",
        title: "Read the inbox",
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
  },
  {
    id: "aster-failed",
    title: "Weekly close",
    status: "Failed",
    phase: "Checking",
    result: "",
    detail: "Close the week for Kodeeo Limited.",
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

function blocked(task: Task, approvals: Approval[]) {
  if (task.status === "Needs approval" || task.status === "Done" || task.status === "Failed") {
    return true;
  }
  return approvals.some((item) => item.taskId === task.id && item.state === "pending");
}

function advance(task: Task) {
  const index = phases.indexOf(task.phase);
  if (index >= 0 && index < phases.length - 1) {
    task.phase = phases[index + 1] as Phase;
    task.status = "Working";
    return;
  }
  task.status = "Done";
}

function isVisibility(value: string): value is TaskVisibility {
  return value === "private" || value === "team" || value === "everyone";
}

export function createStubApi(): ApiClient {
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
