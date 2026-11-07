import type { UsageRange } from "../../app/api";

export const defaultUsageRange: UsageRange = 7;

export const usageRanges: { label: string; value: UsageRange }[] = [
  { label: "Today", value: "today" },
  { label: "7 days", value: 7 },
  { label: "30 days", value: 30 },
  { label: "90 days", value: 90 },
];

/** A range from a URL search value, or undefined when it is not one of the four. */
export function parseUsageRange(value: unknown): UsageRange | undefined {
  if (value === "today") {
    return "today";
  }
  const days = typeof value === "string" ? Number(value) : value;
  return days === 7 || days === 30 || days === 90 ? days : undefined;
}

/** The route's search: `range` is kept only when it is valid. */
export function validateUsageSearch(search: Record<string, unknown>): { range?: UsageRange } {
  const range = parseUsageRange(search.range);
  return range === undefined ? {} : { range };
}

export function rangeDescription(range: UsageRange): string {
  return range === "today" ? "Today." : `Last ${range} days.`;
}
