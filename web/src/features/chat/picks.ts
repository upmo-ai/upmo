import type { AgentProfile, ChatPicks, ProviderAccount } from "../../app/api";
import type { GatewayRoute } from "../models/types";

// The value of the Auto choice on every axis: nothing is sent for it.
export const AUTO = "auto";

/** What the composer has picked on each axis: Auto, an agent id, a route name, a model choice value. */
export type Picks = { agent: string; route: string; model: string };

export const autoPicks: Picks = { agent: AUTO, route: AUTO, model: AUTO };

/** One model a person can pin: an account they can see and a model id on it. */
export type ModelChoice = { value: string; account_id: string; model: string; provider: string; account: string; scope: ProviderAccount["scope"]; share?: ProviderAccount["share"] };

export function modelDescription(item: ModelChoice): string {
  if (item.scope !== "global") return item.account;
  if (item.share?.scope === "team") {
    return `${item.account} (${item.share.team_names.length > 0 ? item.share.team_names.join(", ") : "team"})`;
  }
  return `${item.account} (workspace)`;
}

/** Every usable model of every account, in the order the brain lists the accounts (own first). */
export function modelChoices(accounts: readonly ProviderAccount[]): ModelChoice[] {
  return accounts.flatMap((account) =>
    account.usable_models.map((model) => ({
      value: `${account.id}/${model}`,
      account_id: account.id,
      model,
      provider: account.provider,
      account: account.name,
      scope: account.scope,
      share: account.share,
    })),
  );
}

/**
 * The routes a reply can be written on: an embed or decide route cannot, and
 * one with no usable hop reads as inactive and is not offered.
 */
export function chatRoutes(routes: readonly GatewayRoute[]): GatewayRoute[] {
  return routes.filter(
    (item) => (item.capability === "chat" || item.capability === "stream") && item.usable,
  );
}

/** The agents a person can pick: the enabled ones whose model or route works. */
export function pickableAgents(agents: readonly AgentProfile[]): AgentProfile[] {
  return agents.filter((item) => item.enabled && item.usable);
}

/** A pick that names something no longer there is Auto again. */
export function validPicks(
  picks: Picks,
  agents: readonly AgentProfile[],
  routes: readonly GatewayRoute[],
  models: readonly ModelChoice[],
): Picks {
  return {
    agent: agents.some((item) => item.id === picks.agent) ? picks.agent : AUTO,
    route: routes.some((item) => item.name === picks.route) ? picks.route : AUTO,
    model: models.some((item) => item.value === picks.model) ? picks.model : AUTO,
  };
}

/** The picks as the chat API takes them: Auto leaves its field out. */
export function chatPicks(picks: Picks, models: readonly ModelChoice[]): ChatPicks {
  const out: ChatPicks = {};
  if (picks.agent !== AUTO) out.agent_id = picks.agent;
  if (picks.route !== AUTO) out.route = picks.route;
  const model = models.find((item) => item.value === picks.model);
  if (model) out.pin = { account_id: model.account_id, model: model.model };
  return out;
}

/** "provider/model", how a model reads in the composer. */
export function modelLabel(provider: string, model: string): string {
  return `${provider}/${model}`;
}

const STORAGE_KEY = "upmo.chat.picks";

/** The picks this browser remembered, Auto where there are none or storage fails. */
export function loadPicks(): Picks {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null");
    if (typeof raw !== "object" || raw === null) return autoPicks;
    const field = (name: keyof Picks) => {
      const value = (raw as Record<string, unknown>)[name];
      return typeof value === "string" && value !== "" ? value : AUTO;
    };
    return { agent: field("agent"), route: field("route"), model: field("model") };
  } catch {
    return autoPicks;
  }
}

export function savePicks(picks: Picks): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(picks));
  } catch {
    // Storage is a convenience; the page works without it.
  }
}
