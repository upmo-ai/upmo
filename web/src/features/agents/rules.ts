import { BrainError, type AgentInput, type AgentProfile, type ProviderAccount } from "../../app/api";

export const nameMax = 60;
export const descriptionMax = 300;
export const overlayMax = 4000;

const routeName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

/** What the form holds for an agent: the input, with the model as one picked value. */
export type AgentDraft = AgentInput;

export function emptyDraft(): AgentDraft {
  return {
    name: "",
    description: "",
    prompt_overlay: "",
    route: "",
    model_provider: "",
    model_id: "",
    enabled: true,
  };
}

export function draftOf(agent: AgentProfile): AgentDraft {
  return {
    name: agent.name,
    description: agent.description,
    prompt_overlay: agent.prompt_overlay,
    route: agent.route,
    model_provider: agent.model_provider,
    model_id: agent.model_id,
    enabled: agent.enabled,
  };
}

/** The first thing wrong with a name, as a sentence, or null. The brain checks the same limits. */
export function nameProblem(name: string): string | null {
  const trimmed = name.trim();
  if (trimmed === "") return "Give the agent a name.";
  if ([...trimmed].length > nameMax) return `The name must be 1 to ${nameMax} characters.`;
  return null;
}

export function descriptionProblem(description: string): string | null {
  return [...description.trim()].length > descriptionMax
    ? `The description must be at most ${descriptionMax} characters.`
    : null;
}

export function overlayProblem(overlay: string): string | null {
  return [...overlay.trim()].length > overlayMax ? `The instructions must be at most ${overlayMax} characters.` : null;
}

export function routeProblem(route: string): string | null {
  const trimmed = route.trim();
  return trimmed === "" || routeName.test(trimmed) ? null : "The route is not a valid route name.";
}

/** The first problem with a draft, or null when it can be saved. */
export function draftProblem(draft: AgentDraft): string | null {
  return (
    nameProblem(draft.name) ??
    descriptionProblem(draft.description) ??
    overlayProblem(draft.prompt_overlay) ??
    routeProblem(draft.route)
  );
}

/** The draft as the brain takes it: every text trimmed. */
export function trimmedDraft(draft: AgentDraft): AgentDraft {
  return {
    ...draft,
    name: draft.name.trim(),
    description: draft.description.trim(),
    prompt_overlay: draft.prompt_overlay.trim(),
    route: draft.route.trim(),
  };
}

/** The fields of a draft that differ from the agent it was made from. */
export function changesOf(agent: AgentProfile, draft: AgentDraft): Partial<AgentInput> {
  const next = trimmedDraft(draft);
  const before = draftOf(agent);
  const out: Partial<AgentInput> = {};
  for (const key of Object.keys(before) as (keyof AgentInput)[]) {
    if (before[key] !== next[key]) Object.assign(out, { [key]: next[key] });
  }
  return out;
}

/** One model a person can give an agent: a provider name and a model id. */
export type ModelChoice = { provider: string; model: string };

/** The usable models of accounts the person may use, each provider and model once, in the order the accounts come. */
export function modelChoices(accounts: readonly ProviderAccount[]): ModelChoice[] {
  const seen = new Set<string>();
  const out: ModelChoice[] = [];
  for (const account of accounts) {
    if (!account.can_use) continue;
    for (const model of account.usable_models) {
      const key = choiceValue({ provider: account.provider, model });
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({ provider: account.provider, model });
    }
  }
  return out;
}

/** The value a picker holds for a choice. A JSON pair cannot be taken for another, whatever a model id holds. */
export function choiceValue(choice: ModelChoice): string {
  return JSON.stringify([choice.provider, choice.model]);
}

export function choiceOf(value: string): ModelChoice | null {
  try {
    const parsed: unknown = JSON.parse(value);
    if (Array.isArray(parsed) && parsed.length === 2 && typeof parsed[0] === "string" && typeof parsed[1] === "string") {
      return { provider: parsed[0], model: parsed[1] };
    }
  } catch {
    // A value the picker did not make is no choice.
  }
  return null;
}

const unknownSentence = "Something went wrong. Try again.";

// The refusals whose sentence the brain writes for the person and never fills
// with what they typed: not found, workspace agent, duplicate, invalid.
const plainStatuses: ReadonlySet<number> = new Set([403, 404, 409, 422]);

/**
 * The sentence to show for a failed agent request. Only a sentence the brain
 * wrote is passed on; anything else reads as one fixed sentence.
 */
export function agentProblem(error: unknown): string {
  if (error instanceof BrainError && plainStatuses.has(error.status) && error.error) return error.error;
  return unknownSentence;
}
