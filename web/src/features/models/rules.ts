import type { DecisionGate } from "./types";

export const needsSettingsAccessTitle = "Needs settings access";

export const needsSettingsAccessBody =
  "Models and routes are shown to people who can manage settings. Ask an owner or an admin for access.";

/** Owners and admins hold settings:manage. Members and viewers do not. */
export function canManageSettings(role: string): boolean {
  return role === "owner" || role === "admin";
}

/** Whole percent of the labelled cases the route got right, rounded down. */
export function gatePercent(gate: Pick<DecisionGate, "hit" | "total">): number {
  if (gate.total <= 0) return 0;
  return Math.floor((gate.hit * 100) / gate.total);
}

/** Compares whole numbers, so 89.5 against a bar of 90 is below it. */
export function meetsBar(gate: Pick<DecisionGate, "hit" | "total" | "bar">): boolean {
  return gate.total > 0 && gate.hit * 100 >= gate.bar * gate.total;
}

export function shortFingerprint(fingerprint: string): string {
  return fingerprint.slice(0, 12);
}

/** Day, month, year and minute in UTC, or an empty string for a bad stamp. */
export function formatUpdated(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const day = new Intl.DateTimeFormat("en-GB", {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
    timeZone: "UTC",
  }).format(date);
  return `${day} UTC`;
}
