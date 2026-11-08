import { BrainError, type ProviderPlanKind } from "../../app/api";

/**
 * The brain dispatches to two adapters: `fake`, which has no network, and
 * `openai_compat`, which every hosted or local provider in the picker is
 * reached through. The picker keeps its own labels (anthropic, openai, ...) as
 * the vendor name; this is the one place they turn into the adapter key the
 * brain accepts.
 */
export function adapterKey(pickerAdapter: string): string {
  return pickerAdapter === "fake" ? "fake" : "openai_compat";
}

const adapterLabels: Readonly<Record<string, string>> = {
  fake: "Test adapter",
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
 * Whether the base URL must be filled in. The brain refuses a test without one
 * for every adapter except `fake`, and hosted vendors have no address of their
 * own that the brain could fall back to.
 */
export function baseUrlRequired(pickerAdapter: string): boolean {
  return pickerAdapter !== "fake";
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
