import type { CatalogModel, PriceSource, SeenModel } from "../../app/api";

// Prices are kept in whole micro-USD per million tokens, as the brain keeps
// them, so a typed price is never rounded through a float on its way there.
const MICRO_PLACES = 6;

/**
 * The decimal text moved `places` to the left as a whole number, or null when
 * it is not a plain non-negative decimal, has more digits than fit, or is too
 * large to hold exactly.
 */
function shiftDecimal(text: string, places: number): number | null {
  const match = /^(\d*)(?:\.(\d*))?$/.exec(text.trim());
  if (!match) return null;
  const whole = match[1] ?? "";
  const fraction = match[2] ?? "";
  if (whole === "" && fraction === "") return null;
  if (fraction.length > places && /[1-9]/.test(fraction.slice(places))) return null;
  const digits = `${whole}${fraction.slice(0, places).padEnd(places, "0")}`.replace(/^0+(?=\d)/, "");
  const value = Number(digits);
  return Number.isSafeInteger(value) ? value : null;
}

/** A typed USD amount as micro-USD, or null when it is not a price. At most six decimals. */
export function usdToMicro(text: string): number | null {
  return shiftDecimal(text, MICRO_PLACES);
}

/** Micro-USD as plain USD text with no trailing zeros, for a form field. */
export function microToUsdText(micro: number): string {
  const whole = Math.floor(micro / 1_000_000);
  const fraction = String(micro % 1_000_000).padStart(MICRO_PLACES, "0").replace(/0+$/, "");
  return fraction ? `${whole}.${fraction}` : String(whole);
}

/** Micro-USD per million tokens as a price label, with at least cents shown: $2.50, $0.075. */
export function formatMicro(micro: number): string {
  const [whole, fraction = ""] = microToUsdText(micro).split(".");
  return `$${whole}.${fraction.padEnd(2, "0")}`;
}

/** A catalogue rate (USD per million tokens) as micro-USD, or null when it is not set. */
export function catalogMicro(usd: number | null | undefined): number | null {
  if (usd == null || !Number.isFinite(usd) || usd < 0) return null;
  return Math.round(usd * 1_000_000);
}

/**
 * A provider list rate, given in USD per token as text (OpenRouter's
 * "0.000003"), as micro-USD per million tokens. A negative or malformed rate,
 * such as OpenRouter's "-1", is no rate.
 */
export function seenRateMicro(pricing: Record<string, string> | undefined, key: string): number | null {
  const raw = pricing?.[key];
  return raw === undefined ? null : shiftDecimal(raw, MICRO_PLACES + 6);
}

/** The input and output rates as one line, per million tokens. */
export function formatRates(input: number | null, output: number | null): string {
  if (input === null && output === null) return "Unpriced";
  if (input === 0 && output === 0) return "Free";
  const text = (micro: number | null) => (micro === null ? "N/A" : formatMicro(micro));
  return `in ${text(input)} · out ${text(output)} / MTok`;
}

const sourceLabels: Readonly<Record<PriceSource, string>> = {
  override: "Your price",
  provider: "Provider",
  models_dev: "models.dev",
  other_provider: "Estimate",
  none: "Unknown",
};

/** Where a price came from, as the screen names it. */
export function sourceLabel(source: PriceSource): string {
  return sourceLabels[source];
}

/** A discovered model's price on the form: the provider's own list first, then models.dev. */
export function listedPrice(
  seen: SeenModel,
  catalog: readonly CatalogModel[],
): { input: number | null; output: number | null; source: PriceSource } {
  const input = seenRateMicro(seen.pricing, "prompt");
  const output = seenRateMicro(seen.pricing, "completion");
  if (input !== null || output !== null) return { input, output, source: "provider" };
  const row = catalog.find((item) => item.model === seen.id);
  const catIn = catalogMicro(row?.input_usd_per_mtok);
  const catOut = catalogMicro(row?.output_usd_per_mtok);
  if (catIn !== null || catOut !== null) return { input: catIn, output: catOut, source: "models_dev" };
  return { input: null, output: null, source: "none" };
}
