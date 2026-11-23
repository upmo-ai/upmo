import {
  BrainError,
  createStubApi,
  type AgentChange,
  type AgentInput,
  type AgentProfile,
  type ApiClient,
  type CatalogModel,
  type EnabledModel,
  type MicroRates,
  type ModelPricing,
  type PriceOverride,
  type PriceRate,
  type PriceSource,
  type PriceWarning,
  type ProviderAdd,
  type ProviderChange,
  type ProviderPlanKind,
  type ProviderProbe,
  type ProviderStatus,
  type ProviderUses,
  type ProviderVendor,
  type ProviderVisibility,
  type SeenModel,
  type TestResult,
  type Usage,
  type UsageBucket,
  type UsageBucketUnit,
  type UsageGroup,
  type UsageLimitRow,
  type UsageLimits,
  type UsageModel,
  type UsageModelShare,
  type UsageProvider,
  type UsageProviderShare,
  type UsageRange,
  type UsageRow,
  type UsageSource,
  type UsageTask,
  type UsageTaskModel,
  type WorkspaceUsage,
} from "./api";
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
import {
  artifactKinds,
  type ArtifactDiff,
  type ArtifactKind,
  type ArtifactRecord,
} from "../features/artifacts/types";
import type { Chat, ChatAttachment, ChatLine, ChatSummary, LineRole } from "../features/chat/types";
import { routeCapabilities, type GatewayRoute, type RouteCapability, type RouteDecision, type RouteHop } from "../features/models/types";
import { adapterKey } from "../features/providers/rules";
import type { ListedProvider } from "../features/providers/sharing";

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

/** A string, or null when the brain sends null. Anything else is refused. */
function textOrNull(source: Json, key: string): string | null {
  const value = source[key];
  if (value === null) return null;
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
  parent_canceled: "The parent task was canceled.",
};

/** The brain sends the cause of a failed task as a code. Known codes read as a sentence; an unknown one is shown as sent. */
export function causeSentence(code: string): string {
  return Object.hasOwn(causeSentences, code) ? (causeSentences[code] ?? code) : code;
}

/** An event as the brain sends it: in the detail response and on the task stream. */
export function toEvent(value: unknown): TaskEvent {
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
  if (typeof source.private_read === "boolean") share.private_read = source.private_read;
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
  const parsed: ChatLine = {
    id: text(source, "id"),
    role,
    body: text(source, "body"),
    model,
    tokens: whole(source, "tokens"),
    cost_usd: cost,
    at: text(source, "at"),
  };
  if (source.author_id === null || typeof source.author_id === "string") parsed.author_id = source.author_id;
  if (source.author_name === null || typeof source.author_name === "string") parsed.author_name = source.author_name;
  if (source.author_avatar === null || typeof source.author_avatar === "string") {
    parsed.author_avatar = source.author_avatar;
  }
  // The reply fields are sent by a brain that writes replies; an older one omits them.
  const status = source.status;
  if (status === "done" || status === "failed") parsed.status = status;
  if (typeof source.reply_to === "string") parsed.reply_to = source.reply_to;
  if (source.pending === true) parsed.pending = true;
  if (Array.isArray(source.attempts) && source.attempts.length > 0) {
    parsed.attempts = source.attempts.map((item) => {
      const attempt = record(item, "attempt");
      return { model: text(attempt, "model"), reason: text(attempt, "reason") };
    });
  }
  if (Array.isArray(source.attachments) && source.attachments.length > 0) {
    parsed.attachments = source.attachments.map((item) => {
      const att = record(item, "attachment");
      const parsedAtt: ChatAttachment = {
        name: text(att, "name"),
        mime: text(att, "mime"),
        size_bytes: whole(att, "size_bytes"),
      };
      if (typeof att.sha256 === "string" && att.sha256 !== "") parsedAtt.sha256 = att.sha256;
      return parsedAtt;
    });
  }
  return parsed;
}

function toRouteHop(value: unknown): RouteHop {
  const source = record(value, "hop");
  const hop: RouteHop = {
    position: whole(source, "position"),
    adapter: text(source, "adapter"),
    model: text(source, "model"),
    weight: whole(source, "weight"),
    retry: whole(source, "retry"),
  };
  if (typeof source.account_id === "string" && source.account_id !== "") hop.account_id = source.account_id;
  return hop;
}

/**
 * A route as GET /api/gateway/routes sends it. The brain sends no scope: every
 * route listed is the workspace's, so it reads as global.
 */
export function toGatewayRoute(value: unknown): GatewayRoute {
  const source = record(value, "route");
  const capability = source.capability;
  if (!(routeCapabilities as readonly unknown[]).includes(capability)) {
    throw new Error("The brain sent an unknown route capability.");
  }
  const decision = source.decision;
  if (decision !== "" && decision !== "yesno" && decision !== "choice") {
    throw new Error("The brain sent an unknown route decision.");
  }
  if (typeof source.usable !== "boolean") throw new Error("The brain sent no usable.");
  return {
    name: text(source, "name"),
    scope: source.scope === "own" ? "own" : "global",
    capability: capability as RouteCapability,
    decision: decision as RouteDecision,
    usable: source.usable,
    hops: list(source.hops, "hops").map(toRouteHop),
  };
}

/** A chat in full, as GET /api/chats/{id} sends it: lines oldest first. */
export function toChat(value: unknown): Chat {
  const source = record(value, "chat");
  const chat: Chat = { id: text(source, "id"), lines: list(source.lines, "lines").map(toChatLine) };
  if (typeof source.tainted === "boolean") chat.tainted = source.tainted;
  if (typeof source.private_read === "boolean") chat.private_read = source.private_read;
  if (typeof source.can_edit === "boolean") chat.can_edit = source.can_edit;
  if (typeof source.can_delete === "boolean") chat.can_delete = source.can_delete;
  if (typeof source.title === "string") chat.title = source.title;
  return chat;
}

const planKinds: readonly ProviderPlanKind[] = ["metered", "subscription", "local"];
const providerStatuses: readonly ProviderStatus[] = [
  "Connected",
  "Not connected",
  "Rate limited",
  "Key rejected",
  "Offline",
  "Turned off by your workspace",
];

/** A list of text. A missing list, or one that is not all text, is refused. */
function strings(value: unknown, what: string): string[] {
  const items = list(value, what);
  if (items.some((item) => typeof item !== "string")) throw new Error(`The brain sent no ${what}.`);
  return items as string[];
}

const providerVisibilities: readonly ProviderVisibility[] = ["private", "team", "everyone"];

/**
 * A provider account as GET /api/providers and the write routes send it.
 * base_url is left out when the account has none. The brain never sends a key,
 * so none is read. visibility is private, team or everyone. can_manage says
 * who may change the account, can_use who may route a call through it, and
 * is_owner whether this person owns it. is_owner is for the screen only.
 */
export function toProvider(value: unknown): ListedProvider {
  const source = record(value, "provider");
  const plan = source.plan_kind;
  if (!planKinds.some((kind) => kind === plan)) throw new Error("The brain sent an unknown plan kind.");
  const status = source.status;
  if (!providerStatuses.some((known) => known === status)) throw new Error("The brain sent an unknown status.");
  const visibility = source.visibility;
  if (!providerVisibilities.some((known) => known === visibility)) {
    throw new Error("The brain sent an unknown visibility.");
  }
  if (typeof source.usable !== "boolean") throw new Error("The brain sent no usable.");
  if (typeof source.can_manage !== "boolean") throw new Error("The brain sent no can_manage.");
  if (typeof source.can_use !== "boolean") throw new Error("The brain sent no can_use.");
  if (typeof source.is_owner !== "boolean") throw new Error("The brain sent no is_owner.");
  const account: ListedProvider = {
    id: text(source, "id"),
    provider: text(source, "provider"),
    name: text(source, "name"),
    adapter: text(source, "adapter"),
    plan_kind: plan as ProviderPlanKind,
    status: status as ProviderStatus,
    models: strings(source.models, "models"),
    usable: source.usable,
    usable_models: strings(source.usable_models, "usable_models"),
    visibility: visibility as ProviderVisibility,
    team_ids: strings(source.team_ids, "team_ids"),
    team_names: strings(source.team_names, "team_names"),
    can_manage: source.can_manage,
    can_use: source.can_use,
    is_owner: source.is_owner,
  };
  if (typeof source.base_url === "string" && source.base_url !== "") account.base_url = source.base_url;
  if (typeof source.pricing_provider === "string") account.pricing_provider = source.pricing_provider;
  if (isRecord(source.pricing_models)) {
    account.pricing_models = Object.fromEntries(Object.entries(source.pricing_models).filter((entry): entry is [string, string] => typeof entry[1] === "string"));
  }
  account.enabled = source.enabled !== false;
  if (typeof source.key_hint === "string" && source.key_hint !== "") account.key_hint = source.key_hint;
  if (typeof source.key_changed_at === "string") account.key_changed_at = source.key_changed_at;
  if (typeof source.key_changed_by === "string" && source.key_changed_by !== "") {
    account.key_changed_by = source.key_changed_by;
  }
  account.enabled_models = Array.isArray(source.enabled_models)
    ? source.enabled_models.map(toEnabledModel)
    : account.models.map((id) => ({ id, listed: true, retired: false }));
  return account;
}

function toEnabledModel(value: unknown): EnabledModel {
  const source = record(value, "enabled model");
  return { id: text(source, "id"), listed: source.listed === true, retired: source.retired === true };
}

/** A whole number, or undefined when the brain left it out. */
function optionalWhole(source: Json, key: string): number | undefined {
  const value = source[key];
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/** A rate in USD per million tokens, or null when the brain sends none. */
function usdRate(source: Json, key: string): number | null {
  const value = source[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

/** A rate in micro-USD per million tokens, or null when it is not set. */
function microRate(source: Json, key: string): number | null {
  const value = source[key];
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

/** One vendor of GET /api/providers/vendors. A plan the web does not know is no suggestion. */
export function toVendor(value: unknown): ProviderVendor {
  const source = record(value, "vendor");
  const vendor: ProviderVendor = {
    key: text(source, "key"),
    name: text(source, "name"),
    adapter: text(source, "adapter"),
    default_base_url: text(source, "default_base_url"),
  };
  const plan = source.suggested_plan;
  if (planKinds.some((kind) => kind === plan)) vendor.suggested_plan = plan as ProviderPlanKind;
  return vendor;
}

/** One model a provider listed. Pricing keeps only its text rates. */
export function toSeenModel(value: unknown): SeenModel {
  const source = record(value, "model");
  const seen: SeenModel = { id: text(source, "id") };
  if (typeof source.display_name === "string" && source.display_name !== "") seen.display_name = source.display_name;
  const context = optionalWhole(source, "context_length");
  if (context !== undefined && context > 0) seen.context_length = context;
  if (isRecord(source.pricing)) {
    const pricing: Record<string, string> = {};
    for (const [key, rate] of Object.entries(source.pricing)) {
      if (typeof rate === "string") pricing[key] = rate;
    }
    seen.pricing = pricing;
  }
  return seen;
}

/** One models.dev row of GET /api/providers/catalog. */
export function toCatalogModel(value: unknown): CatalogModel {
  const source = record(value, "catalog row");
  const row: CatalogModel = {
    model: text(source, "model"),
    kind: typeof source.kind === "string" ? source.kind : "",
    context_window: optionalWhole(source, "context_window") ?? 0,
    input_usd_per_mtok: usdRate(source, "input_usd_per_mtok"),
    output_usd_per_mtok: usdRate(source, "output_usd_per_mtok"),
    cache_read_usd_per_mtok: usdRate(source, "cache_read_usd_per_mtok"),
    cache_write_usd_per_mtok: usdRate(source, "cache_write_usd_per_mtok"),
  };
  if (typeof source.lifecycle === "string" && source.lifecycle !== "") row.lifecycle = source.lifecycle;
  if (typeof source.retires_at === "string" && source.retires_at !== "") row.retires_at = source.retires_at;
  return row;
}

const priceRates: readonly PriceRate[] = ["input", "output", "cache_read", "cache_write"];
const priceSources: readonly PriceSource[] = ["override", "provider", "models_dev", "other_provider", "none"];

function priceSource(value: unknown): PriceSource {
  return priceSources.some((known) => known === value) ? (value as PriceSource) : "none";
}

/** The merged view names each rate in USD; the override and warnings in micro-USD. */
const usdKeys: Readonly<Record<PriceRate, string>> = {
  input: "input_usd_per_mtok",
  output: "output_usd_per_mtok",
  cache_read: "cache_read_usd_per_mtok",
  cache_write: "cache_write_usd_per_mtok",
};

const microKeys: Readonly<Record<PriceRate, string>> = {
  input: "input_micro_usd_per_mtok",
  output: "output_micro_usd_per_mtok",
  cache_read: "cache_read_micro_usd_per_mtok",
  cache_write: "cache_write_micro_usd_per_mtok",
};

function toPriceOverride(value: unknown): PriceOverride {
  const source = record(value, "override");
  const override: PriceOverride = {
    input: microRate(source, microKeys.input),
    output: microRate(source, microKeys.output),
    cache_read: microRate(source, microKeys.cache_read),
    cache_write: microRate(source, microKeys.cache_write),
  };
  if (typeof source.updated_by === "string" && source.updated_by !== "") override.updated_by = source.updated_by;
  if (typeof source.updated_at === "string") override.updated_at = source.updated_at;
  return override;
}

function toPriceWarning(value: unknown): PriceWarning | null {
  const source = record(value, "warning");
  const rate = priceRates.find((item) => microKeys[item] === source.field);
  const override = microRate(source, "override_micro_usd_per_mtok");
  const base = microRate(source, "base_micro_usd_per_mtok");
  if (!rate || override === null || base === null) return null;
  return { rate, override, base, base_source: priceSource(source.base_source) };
}

/**
 * GET /api/providers/{id}/pricing and the override writes. A rate the
 * override set is taken from the override in micro-USD, exactly; any other is
 * the merged USD rate, rounded to whole micro-USD.
 */
export function toModelPricing(value: unknown): ModelPricing {
  const source = record(value, "pricing");
  const merged = record(source.merged, "pricing");
  const sources = isRecord(merged.sources) ? merged.sources : {};
  const override = source.override == null ? undefined : toPriceOverride(source.override);
  const rates = {} as MicroRates;
  const from = {} as Record<PriceRate, PriceSource>;
  for (const rate of priceRates) {
    from[rate] = priceSource(sources[usdKeys[rate]]);
    const usd = usdRate(merged, usdKeys[rate]);
    const exact = from[rate] === "override" ? override?.[rate] : undefined;
    rates[rate] = exact ?? (usd === null ? null : Math.round(usd * 1_000_000));
  }
  const pricing: ModelPricing = {
    model: text(merged, "model"),
    rates,
    sources: from,
    warnings: Array.isArray(source.warnings)
      ? source.warnings.map(toPriceWarning).filter((item): item is PriceWarning => item !== null)
      : [],
  };
  if (override) pricing.override = override;
  if (typeof merged.lifecycle === "string" && merged.lifecycle !== "") pricing.lifecycle = merged.lifecycle;
  if (typeof merged.retires_at === "string" && merged.retires_at !== "") pricing.retires_at = merged.retires_at;
  return pricing;
}

/** GET /api/providers/{id}/uses. */
export function toProviderUses(value: unknown): ProviderUses {
  const source = record(value, "uses");
  const names = (key: string) => (Array.isArray(source[key]) ? strings(source[key], key) : []);
  return { routes: names("routes"), agents: names("agents"), automations: names("automations") };
}

/** The PUT body of an override: every rate, null where it is not set. */
export function priceOverrideBody(model: string, rates: MicroRates): Json {
  const body: Json = { model };
  for (const rate of priceRates) body[microKeys[rate]] = rates[rate];
  return body;
}

/** An agent as GET /api/agents and the write routes send it. */
export function toAgent(value: unknown): AgentProfile {
  const source = record(value, "agent");
  const visibility = source.visibility;
  if (!providerVisibilities.some((known) => known === visibility)) {
    throw new Error("The brain sent an unknown visibility.");
  }
  if (typeof source.enabled !== "boolean") throw new Error("The brain sent no enabled.");
  if (typeof source.usable !== "boolean") throw new Error("The brain sent no usable.");
  if (typeof source.can_manage !== "boolean") throw new Error("The brain sent no can_manage.");
  if (typeof source.can_use !== "boolean") throw new Error("The brain sent no can_use.");
  return {
    id: text(source, "id"),
    name: text(source, "name"),
    description: text(source, "description"),
    prompt_overlay: text(source, "prompt_overlay"),
    route: text(source, "route"),
    model_provider: text(source, "model_provider"),
    model_id: text(source, "model_id"),
    enabled: source.enabled,
    usable: source.usable,
    visibility: visibility as ProviderVisibility,
    team_ids: strings(source.team_ids, "team_ids"),
    team_names: strings(source.team_names, "team_names"),
    can_use: source.can_use,
    can_manage: source.can_manage,
  };
}

/** A kind the web knows. Any other string is a file with no viewer. */
function artifactKind(value: unknown): ArtifactKind {
  if (typeof value !== "string") throw new Error("The brain sent no kind.");
  return (artifactKinds as readonly string[]).includes(value) ? (value as ArtifactKind) : "binary";
}

/** Size in bytes: a whole number, never negative. */
function sizeBytes(source: Json): number {
  const value = source.size;
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0) {
    throw new Error("The brain sent no size.");
  }
  return value;
}

/**
 * An artifact as GET /api/tasks/{id}/artifacts sends it. A kind this web does
 * not know reads as a plain file, so a newer server still lists the row.
 */
export function toArtifactRecord(value: unknown): ArtifactRecord {
  const source = record(value, "artifact");
  return {
    id: text(source, "id"),
    name: text(source, "name"),
    kind: artifactKind(source.kind),
    mime: text(source, "mime"),
    size: sizeBytes(source),
    created_at: text(source, "created_at"),
  };
}

/**
 * A diff as GET /api/artifacts/{id}/diff sends it. An empty files list is a
 * diff with no changes. A file needs a path and at least one side.
 */
export function toArtifactDiff(value: unknown): ArtifactDiff {
  const source = record(value, "diff");
  return {
    files: list(source.files, "files").map((item) => {
      const file = record(item, "file");
      const path = text(file, "path");
      if (path.trim() === "") throw new Error("The brain sent no path.");
      const before = textOrNull(file, "before");
      const after = textOrNull(file, "after");
      if (before === null && after === null) throw new Error("The brain sent no change.");
      return { path, before, after };
    }),
  };
}

/** The answer of POST /api/providers/test: ok, and a plain sentence when it is not. */
export function toTestResult(value: unknown): TestResult {
  const source = record(value, "test result");
  if (typeof source.ok !== "boolean") throw new Error("The brain sent no ok.");
  const result: TestResult = { ok: source.ok };
  if (typeof source.error === "string" && source.error !== "") result.error = source.error;
  if (Array.isArray(source.models)) result.models = source.models.map(toSeenModel);
  return result;
}

/** A field with a value, or nothing: an add and a test never send an empty value. */
function given(key: string, value: string | undefined): Json {
  return value === undefined || value === "" ? {} : { [key]: value };
}

/**
 * The sharing of a write. A new personal account, and a write that leaves
 * sharing alone, send none. A change to private sends private and no teams.
 * A team account names its teams. A workspace account takes none.
 */
function sharing(input: { visibility?: ProviderVisibility; team_ids?: string[] }, change = false): Json {
  if (input.visibility === undefined) return {};
  if (input.visibility === "private") return change ? { visibility: "private" } : {};
  return input.visibility === "team"
    ? { visibility: "team", team_ids: input.team_ids ?? [] }
    : { visibility: "everyone" };
}

/** The POST body of a new account: the picker's adapter becomes the brain's adapter key. */
export function providerAddBody(input: ProviderAdd): Json {
  return {
    provider: input.provider,
    name: input.name,
    adapter: adapterKey(input.adapter),
    plan_kind: input.plan_kind,
    ...given("base_url", input.base_url),
    ...given("api_key", input.api_key),
    models: input.models,
    ...sharing(input),
  };
}

/**
 * The PUT body of a change. A field left out stays as it is; an emptied base
 * URL is sent as "" so the brain clears the saved address, and without an
 * api_key the saved key stays.
 */
export function providerChangeBody(input: ProviderChange): Json {
  return {
    ...(input.name !== undefined ? { name: input.name } : {}),
    ...(input.plan_kind !== undefined ? { plan_kind: input.plan_kind } : {}),
    ...(input.base_url !== undefined ? { base_url: input.base_url } : {}),
    ...given("api_key", input.api_key),
    ...(input.models !== undefined ? { models: input.models } : {}),
    ...(input.enabled !== undefined ? { enabled: input.enabled } : {}),
    ...sharing(input, true),
  };
}

/**
 * The body of a test. plan_kind is sent whenever it is known, because the base
 * URL rules depend on it. An id with no api_key tests the saved key.
 */
export function providerTestBody(config: ProviderProbe): Json {
  return {
    provider: config.provider,
    ...(config.adapter !== undefined ? { adapter: adapterKey(config.adapter) } : {}),
    ...(config.plan_kind !== undefined ? { plan_kind: config.plan_kind } : {}),
    ...given("base_url", config.base_url),
    ...given("api_key", config.api_key),
    ...given("model", config.model),
    ...given("id", config.id),
  };
}

async function call(path: string, init?: { method: "POST" | "PUT" | "PATCH" | "DELETE"; body?: unknown }): Promise<unknown> {
  const isForm = typeof FormData !== "undefined" && init?.body instanceof FormData;
  const response = await fetch(path, {
    credentials: "include",
    ...(init
      ? {
          method: init.method,
          ...(init.body !== undefined
            ? isForm
              ? { body: init.body as FormData }
              : { headers: { "content-type": "application/json" }, body: JSON.stringify(init.body) }
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

/** GET a route whose body is text, not JSON. A refusal still carries the JSON error the brain sends. */
async function callText(path: string): Promise<string> {
  const response = await fetch(path, { credentials: "include" });
  if (!response.ok) {
    let message: string | undefined;
    try {
      const body: unknown = await response.json();
      if (isRecord(body) && typeof body.error === "string") message = body.error;
    } catch {
      // A refusal can come with no JSON body.
    }
    throw new BrainError(response.status, message);
  }
  return response.text();
}

/**
 * Money as whole micro-USD, or null when the brain says the cost is unknown.
 * A field that is missing is refused, so an absent cost never reads as unknown
 * and a null never reads as 0.
 */
function microOrNull(source: Json, key: string): number | null {
  const value = source[key];
  if (value === null) return null;
  if (typeof value !== "number" || !Number.isSafeInteger(value)) throw new Error(`The brain sent no ${key}.`);
  return value;
}

function micro(source: Json, key: string): number {
  const value = microOrNull(source, key);
  if (value === null) throw new Error(`The brain sent no ${key}.`);
  return value;
}

function flag(source: Json, key: string): boolean {
  const value = source[key];
  if (typeof value !== "boolean") throw new Error(`The brain sent no ${key}.`);
  return value;
}

function usageRange(value: unknown): UsageRange {
  if (value === "today" || value === 7 || value === 30 || value === 90) return value;
  throw new Error("The brain sent an unknown range.");
}

function bucketUnit(value: unknown): UsageBucketUnit {
  if (value === "hour" || value === "day") return value;
  throw new Error("The brain sent an unknown bucket.");
}

const usageGroups: readonly string[] = ["person", "team", "agent", "model", "provider", "source"];

function usageGroup(value: unknown): UsageGroup {
  if (typeof value === "string" && usageGroups.includes(value)) return value as UsageGroup;
  throw new Error("The brain sent an unknown group.");
}

function toProviderShare(value: unknown): UsageProviderShare {
  const source = record(value, "provider share");
  return {
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
    requests: whole(source, "requests"),
    errors: whole(source, "errors"),
    tokens: whole(source, "tokens"),
  };
}

function toModelShare(value: unknown): UsageModelShare {
  const source = record(value, "model share");
  return { spend_micro_usd: microOrNull(source, "spend_micro_usd"), tokens: whole(source, "tokens") };
}

function shares<T>(value: unknown, what: string, read: (item: unknown) => T): Record<string, T> {
  const source = record(value, what);
  return Object.fromEntries(Object.entries(source).map(([key, item]) => [key, read(item)]));
}

function toUsageBucket(value: unknown): UsageBucket {
  const source = record(value, "bucket");
  return {
    at: text(source, "at"),
    requests: whole(source, "requests"),
    errors: whole(source, "errors"),
    input: whole(source, "input"),
    output: whole(source, "output"),
    cache_read: whole(source, "cache_read"),
    cache_write: whole(source, "cache_write"),
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
    estimated: flag(source, "estimated"),
    list_price_micro_usd: microOrNull(source, "list_price_micro_usd"),
    by_provider: shares(source.by_provider, "provider shares", toProviderShare),
    by_model: shares(source.by_model, "model shares", toModelShare),
  };
}

function toUsageProvider(value: unknown): UsageProvider {
  const source = record(value, "provider");
  const scope = source.scope;
  if (scope !== "global" && scope !== "own") throw new Error("The brain sent an unknown scope.");
  const latency = record(source.latency_ms, "latency");
  return {
    id: text(source, "id"),
    name: text(source, "name"),
    scope,
    plan_kind: text(source, "plan_kind"),
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
    requests: whole(source, "requests"),
    errors: whole(source, "errors"),
    latency_ms: { p50: whole(latency, "p50"), p95: whole(latency, "p95"), p99: whole(latency, "p99") },
  };
}

function toUsageModel(value: unknown): UsageModel {
  const source = record(value, "model");
  return {
    id: text(source, "id"),
    name: text(source, "name"),
    provider_id: text(source, "provider_id"),
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
    tokens: whole(source, "tokens"),
  };
}

function toUsageSource(value: unknown): UsageSource {
  const source = record(value, "source");
  return {
    source: text(source, "source"),
    requests: whole(source, "requests"),
    tokens: whole(source, "tokens"),
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
  };
}

function toUsageTaskModel(value: unknown): UsageTaskModel {
  const source = record(value, "task model");
  return {
    model_id: text(source, "model_id"),
    cost_micro_usd: microOrNull(source, "cost_micro_usd"),
    input: whole(source, "input"),
    output: whole(source, "output"),
    cache_read: whole(source, "cache_read"),
    cache_write: whole(source, "cache_write"),
  };
}

function toUsageTask(value: unknown): UsageTask {
  const source = record(value, "task");
  return {
    task_id: text(source, "task_id"),
    goal: text(source, "goal"),
    input: whole(source, "input"),
    output: whole(source, "output"),
    cache_read: whole(source, "cache_read"),
    cache_write: whole(source, "cache_write"),
    cost_micro_usd: microOrNull(source, "cost_micro_usd"),
    by_model: list(source.by_model, "task models").map(toUsageTaskModel),
  };
}

/** GET /api/usage as the gateway sends it. */
export function toUsage(value: unknown): Usage {
  const source = record(value, "usage");
  return {
    range: usageRange(source.range),
    bucket: bucketUnit(source.bucket),
    time_zone: text(source, "time_zone"),
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
    estimated: flag(source, "estimated"),
    unpriced_calls: whole(source, "unpriced_calls"),
    list_price_micro_usd: microOrNull(source, "list_price_micro_usd"),
    buckets: list(source.buckets, "buckets").map(toUsageBucket),
    providers: list(source.providers, "providers").map(toUsageProvider),
    models: list(source.models, "models").map(toUsageModel),
    sources: list(source.sources, "sources").map(toUsageSource),
    tasks: list(source.tasks, "tasks").map(toUsageTask),
  };
}

function toLimitRow(value: unknown): UsageLimitRow {
  const source = record(value, "limit row");
  return {
    kind: text(source, "kind"),
    ...(typeof source.subject_id === "string" ? { subject_id: source.subject_id } : {}),
    amount_micro_usd: microOrNull(source, "amount_micro_usd"),
    spent_micro_usd: micro(source, "spent_micro_usd"),
    held_micro_usd: micro(source, "held_micro_usd"),
  };
}

/**
 * GET /api/usage/limits as the gateway sends it. `rows` is left out unless the
 * gateway sent a list (even an empty one), because its presence says the person
 * holds usage:view. A null or any other value is not a list and does not count.
 */
export function toUsageLimits(value: unknown): UsageLimits {
  const source = record(value, "limits");
  const own = record(source.own, "own limits");
  return {
    period_start: text(source, "period_start"),
    own: {
      limit_micro_usd: microOrNull(own, "limit_micro_usd"),
      workspace_limit_micro_usd: microOrNull(own, "workspace_limit_micro_usd"),
      spent_micro_usd: micro(own, "spent_micro_usd"),
      held_micro_usd: micro(own, "held_micro_usd"),
    },
    ...(Array.isArray(source.rows) ? { rows: source.rows.map(toLimitRow) } : {}),
  };
}

function toUsageRow(value: unknown): UsageRow {
  const source = record(value, "row");
  return {
    id: text(source, "id"),
    name: text(source, "name"),
    requests: whole(source, "requests"),
    errors: whole(source, "errors"),
    input: whole(source, "input"),
    output: whole(source, "output"),
    cache_read: whole(source, "cache_read"),
    cache_write: whole(source, "cache_write"),
    reasoning: whole(source, "reasoning"),
    tokens: whole(source, "tokens"),
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
    estimated: flag(source, "estimated"),
    unpriced_calls: whole(source, "unpriced_calls"),
    list_price_micro_usd: microOrNull(source, "list_price_micro_usd"),
  };
}

/** GET /api/usage/workspace as the gateway sends it. */
export function toWorkspaceUsage(value: unknown): WorkspaceUsage {
  const source = record(value, "workspace usage");
  return {
    range: usageRange(source.range),
    bucket: bucketUnit(source.bucket),
    time_zone: text(source, "time_zone"),
    group: usageGroup(source.group),
    spend_micro_usd: microOrNull(source, "spend_micro_usd"),
    estimated: flag(source, "estimated"),
    unpriced_calls: whole(source, "unpriced_calls"),
    list_price_micro_usd: microOrNull(source, "list_price_micro_usd"),
    buckets: list(source.buckets, "buckets").map(toUsageBucket),
    rows: list(source.rows, "rows").map(toUsageRow),
  };
}

/** The query of a usage read: the range as the route spells it. */
export function usageQuery(range: UsageRange, group?: UsageGroup): string {
  return `range=${encodeURIComponent(String(range))}${group === undefined ? "" : `&group=${encodeURIComponent(group)}`}`;
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

function providerPath(id: string) {
  return `/api/providers/${encodeURIComponent(id)}`;
}

function agentPath(id: string) {
  return `/api/agents/${encodeURIComponent(id)}`;
}

function artifactPath(id: string) {
  return `/api/artifacts/${encodeURIComponent(id)}`;
}

type Source = "brain" | "stub";

/**
 * The stub client with its task methods (list, get, advance, create, message),
 * its approvals, its notifications, its member list, its task shares, its
 * chats, its provider accounts, its agents and its artifacts answered by the
 * brain instead. Everything else stays on the stub.
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
    // The brain streams task events (GET /api/tasks/{id}/stream); the stub has
    // no stream and keeps polling. Says so only once the probe settled.
    get taskEventsStream() {
      return settled === "brain";
    },
    // The same for a chat's stream (GET /api/chats/{id}/stream).
    get chatStream() {
      return settled === "brain";
    },
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
    providers: {
      async list() {
        if ((await chosen()) === "stub") return stub.providers.list();
        return list(await call("/api/providers"), "providers").map(toProvider);
      },
      async add(input) {
        if ((await chosen()) === "stub") return stub.providers.add(input);
        return toProvider(await call("/api/providers", { method: "POST", body: providerAddBody(input) }));
      },
      async update(id, input) {
        if ((await chosen()) === "stub") return stub.providers.update(id, input);
        return toProvider(await call(providerPath(id), { method: "PUT", body: providerChangeBody(input) }));
      },
      async remove(id) {
        if ((await chosen()) === "stub") return stub.providers.remove(id);
        await call(providerPath(id), { method: "DELETE" });
      },
      async test(config) {
        if ((await chosen()) === "stub") return stub.providers.test(config);
        return toTestResult(await call("/api/providers/test", { method: "POST", body: providerTestBody(config) }));
      },
      async vendors() {
        if ((await chosen()) === "stub") return stub.providers.vendors();
        return list(await call("/api/providers/vendors"), "vendors").map(toVendor);
      },
      async catalog(vendor) {
        if ((await chosen()) === "stub") return stub.providers.catalog(vendor);
        const path = `/api/providers/catalog?vendor=${encodeURIComponent(vendor)}`;
        return list(await call(path), "catalog").map(toCatalogModel);
      },
      async models(id) {
        if ((await chosen()) === "stub") return stub.providers.models(id);
        return list(await call(`${providerPath(id)}/models`), "models").map(toSeenModel);
      },
      async pricing(id, model) {
        if ((await chosen()) === "stub") return stub.providers.pricing(id, model);
        return toModelPricing(await call(`${providerPath(id)}/pricing?model=${encodeURIComponent(model)}`));
      },
      async uses(id) {
        if ((await chosen()) === "stub") return stub.providers.uses(id);
        return toProviderUses(await call(`${providerPath(id)}/uses`));
      },
      async setPrice(id, model, rates) {
        if ((await chosen()) === "stub") return stub.providers.setPrice(id, model, rates);
        const body = priceOverrideBody(model, rates);
        return toModelPricing(await call(`${providerPath(id)}/overrides`, { method: "PUT", body }));
      },
      async clearPrice(id, model) {
        if ((await chosen()) === "stub") return stub.providers.clearPrice(id, model);
        return toModelPricing(await call(`${providerPath(id)}/overrides`, { method: "DELETE", body: { model } }));
      },
      async setPricingMap(id, pricing_provider, models) {
        if ((await chosen()) === "stub") return stub.providers.setPricingMap(id, pricing_provider, models);
        await call(`${providerPath(id)}/pricing-map`, { method: "PUT", body: { ...(pricing_provider === undefined ? {} : { pricing_provider }), models } });
      },
    },
    agents: {
      async list() {
        if ((await chosen()) === "stub") return stub.agents.list();
        return list(await call("/api/agents"), "agents").map(toAgent);
      },
      async create(input: AgentInput) {
        if ((await chosen()) === "stub") return stub.agents.create(input);
        return toAgent(await call("/api/agents", { method: "POST", body: { ...input } }));
      },
      async update(id: string, input: AgentChange) {
        if ((await chosen()) === "stub") return stub.agents.update(id, input);
        return toAgent(await call(agentPath(id), { method: "PATCH", body: { ...input } }));
      },
      async remove(id: string) {
        if ((await chosen()) === "stub") return stub.agents.remove(id);
        await call(agentPath(id), { method: "DELETE" });
      },
    },
    gateway: {
      async routes() {
        if ((await chosen()) === "stub") return stub.gateway.routes();
        return list(await call("/api/gateway/routes"), "routes").map(toGatewayRoute);
      },
    },
    chats: {
      // The stub plays the assistant a moment later; the brain only stores the
      // line, so a reply is worth waiting for only while the stub owes one.
      get repliesLater() {
        return settled === "stub";
      },
      async list(query) {
        if ((await chosen()) === "stub") return stub.chats.list(query);
        const path = query?.trim() ? `/api/chats?q=${encodeURIComponent(query.trim())}` : "/api/chats";
        return list(await call(path), "chats").map(toChatSummary);
      },
      async create() {
        if ((await chosen()) === "stub") return stub.chats.create();
        return toChatSummary(await call("/api/chats", { method: "POST" }));
      },
      async get(id) {
        if ((await chosen()) === "stub") return stub.chats.get(id);
        return toChat(await call(chatPath(id)));
      },
      async send(id, body, picks = {}) {
        if ((await chosen()) === "stub") return stub.chats.send(id, body, picks);
        if (picks.files && picks.files.length > 0) {
          const form = new FormData();
          form.set("body", body);
          if (picks.route) form.set("route", picks.route);
          if (picks.agent_id) form.set("agent_id", picks.agent_id);
          if (picks.pin) {
            form.set("pin_account_id", picks.pin.account_id);
            form.set("pin_model", picks.pin.model);
          }
          for (const file of picks.files) {
            form.append("files", file, file.name);
          }
          await call(chatPath(id), { method: "POST", body: form });
          return;
        }
        const { files: _files, ...jsonPicks } = picks;
        await call(chatPath(id), { method: "POST", body: { body, ...jsonPicks } });
      },
      async retry(id, lineId, picks) {
        if ((await chosen()) === "stub") return stub.chats.retry(id, lineId, picks);
        await call(`${chatPath(id)}/lines/${encodeURIComponent(lineId)}/retry`, { method: "POST", body: { ...picks } });
      },
      async getShare(id) {
        if ((await chosen()) === "stub") return stub.chats.getShare(id);
        return toTaskShare(await call(`${chatPath(id)}/share`));
      },
      async putShare(id, update) {
        if ((await chosen()) === "stub") return stub.chats.putShare(id, update);
        return toTaskShare(await call(`${chatPath(id)}/share`, { method: "PUT", body: shareBody(update) }));
      },
      async rename(id, title) {
        if ((await chosen()) === "stub") return stub.chats.rename(id, title);
        return toChatSummary(await call(chatPath(id), { method: "PATCH", body: { title } }));
      },
      async remove(id) {
        if ((await chosen()) === "stub") return stub.chats.remove(id);
        await call(chatPath(id), { method: "DELETE" });
      },
    },
    async usage(range) {
      if ((await chosen()) === "stub") return stub.usage(range);
      return toUsage(await call(`/api/usage?${usageQuery(range)}`));
    },
    async usageLimits() {
      if ((await chosen()) === "stub") return stub.usageLimits();
      return toUsageLimits(await call("/api/usage/limits"));
    },
    async workspaceUsage(range, group) {
      if ((await chosen()) === "stub") return stub.workspaceUsage(range, group);
      return toWorkspaceUsage(await call(`/api/usage/workspace?${usageQuery(range, group)}`));
    },
    artifacts: {
      async list(taskId) {
        if ((await chosen()) === "stub") return stub.artifacts.list(taskId);
        return list(await call(`${taskPath(taskId)}/artifacts`), "artifacts").map(toArtifactRecord);
      },
      async text(id) {
        if ((await chosen()) === "stub") return stub.artifacts.text(id);
        return callText(`${artifactPath(id)}/text`);
      },
      async diff(id) {
        if ((await chosen()) === "stub") return stub.artifacts.diff(id);
        return toArtifactDiff(await call(`${artifactPath(id)}/diff`));
      },
      // Synchronous, so it can only use the probe once it has answered. Before
      // that, and whenever the session stayed on the stub, the sample URL stands.
      downloadUrl(id) {
        if (settled === "brain") return `${artifactPath(id)}/download`;
        return stub.artifacts.downloadUrl(id);
      },
    },
  };
}
