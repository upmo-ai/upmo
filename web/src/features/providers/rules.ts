import { BrainError, type ProviderAccount, type ProviderPlanKind, type ProviderStatus } from "../../app/api";

/**
 * The brain dispatches to three adapters: `fake`, which has no network,
 * `anthropic`, which speaks the Anthropic Messages API, and `openai_compat`,
 * which every other hosted or local provider in the picker is reached
 * through. The picker keeps its own labels (anthropic, openai, ...) as the
 * vendor name; this is the one place they turn into the adapter key the brain
 * accepts. The brain tells the compatible vendors apart by their address.
 */
export function adapterKey(pickerAdapter: string): string {
  return pickerAdapter === "fake" || pickerAdapter === "anthropic" ? pickerAdapter : "openai_compat";
}

const adapterLabels: Readonly<Record<string, string>> = {
  fake: "Test adapter",
  anthropic: "Anthropic Messages",
  openai_compat: "OpenAI-compatible",
};

/** The text shown under an account's name. A key the table does not know is shown as sent. */
export function adapterLabel(adapter: string): string {
  return Object.hasOwn(adapterLabels, adapter) ? (adapterLabels[adapter] ?? adapter) : adapter;
}

/**
 * Whether the person must type an API key, as the brain's NeedKey decides: a
 * metered or subscription account needs one unless one is already saved; a
 * local account may go without.
 */
export function keyRequired(plan: ProviderPlanKind, hasSavedKey = false): boolean {
  return plan !== "local" && !hasSavedKey;
}

/**
 * Whether the key field is shown. A provider that takes a key always shows it;
 * any provider shows it on a plan that is not local, because the brain demands
 * a key there.
 */
export function showsKeyField(providerTakesKey: boolean, plan: ProviderPlanKind): boolean {
  return providerTakesKey || plan !== "local";
}

/**
 * The address a provider is reached at when the person gives none: its
 * default, when that is a hosted https address. A local runtime has no such
 * address, since only its owner knows where it runs.
 */
export function hostedBaseUrl(defaultBaseUrl?: string): string | undefined {
  return defaultBaseUrl?.startsWith("https://") ? defaultBaseUrl : undefined;
}

/**
 * Whether the base URL must be filled in. The brain refuses a test without one
 * for every adapter except `fake`, so it is required unless the provider has a
 * hosted address to fall back on (see hostedBaseUrl).
 */
export function baseUrlRequired(pickerAdapter: string, defaultBaseUrl?: string): boolean {
  return pickerAdapter !== "fake" && hostedBaseUrl(defaultBaseUrl) === undefined;
}

export const keysOffSentence = "Saving keys is turned off on this server.";
const unknownSentence = "Something went wrong. Try again.";

// The refusals whose sentence the brain writes for the person and never fills
// with what they typed: not found, shared account, duplicate or in use,
// invalid, and too many tests.
const plainStatuses: ReadonlySet<number> = new Set([403, 404, 409, 422, 429]);

/**
 * The sentence to show for a failed provider request. Only a sentence the
 * brain wrote for the person is passed on; anything else, including network
 * failures and server errors, reads as one fixed sentence, so no other text
 * (and never a key) reaches the screen.
 */
export function providerProblem(error: unknown): string {
  if (!(error instanceof BrainError)) return unknownSentence;
  if (error.status === 503) return keysOffSentence;
  if (plainStatuses.has(error.status) && error.error) return error.error;
  return unknownSentence;
}

const workspaceOffStatus = "Turned off by your workspace" satisfies ProviderStatus;

/** Whether the workspace's policy has turned the account off for this person. */
export function workspaceOff(status: ProviderStatus): boolean {
  return status === workspaceOffStatus;
}

/** The status the card shows. One the workspace turned off reads as not connected; a badge says why. */
export function cardStatus(status: ProviderStatus): Exclude<ProviderStatus, typeof workspaceOffStatus> {
  return status === workspaceOffStatus ? "Not connected" : status;
}

/** The enabled models two daily checks found missing from the provider's list. */
export function retiredModels(account: Pick<ProviderAccount, "enabled_models">): string[] {
  return (account.enabled_models ?? []).filter((model) => model.retired).map((model) => model.id);
}

/** The line about the saved key: its last characters, and when it was set. Nothing without a hint. */
export function keyLine(
  account: Pick<ProviderAccount, "key_hint" | "key_changed_at">,
  date: (iso: string) => string,
): string | null {
  if (!account.key_hint) return null;
  const when = account.key_changed_at ? `, set ${date(account.key_changed_at)}` : "";
  return `Key ending ${account.key_hint}${when}`;
}
