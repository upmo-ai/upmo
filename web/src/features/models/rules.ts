import type { DecisionBinding, DecisionGate, Scope } from "./types";

/** Whole percent of the labeled cases the route got right, rounded down. */
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

/** One row of the decision view: a decision, its binding and its gate. */
export interface DecisionRow {
  key: string;
  decision: string;
  scope: Scope;
  /** Null when the server sent a gate for a route no decision is bound to. */
  binding: DecisionBinding | null;
  /** The accepted gate of the bound route, or null when it has none. */
  gate: DecisionGate | null;
}

/**
 * Joins each decision to the gate of the route it is bound to. A decision with
 * no route takes the gate of the route that shares its name, so one name never
 * gets two rows. A gate that no decision claims keeps a row of its own, so no
 * gate drops out of the view.
 */
export function decisionRows(decisions: DecisionBinding[], gates: DecisionGate[]): DecisionRow[] {
  const byRoute = new Map(gates.map((gate) => [gate.route, gate]));
  const claimed = new Set<string>();
  const rows: DecisionRow[] = decisions.map((binding) => {
    const route = binding.route ?? binding.decision;
    const gate = byRoute.get(route) ?? null;
    if (gate) claimed.add(gate.route);
    return { key: `decision:${binding.decision}`, decision: binding.decision, scope: binding.scope, binding, gate };
  });
  for (const gate of gates) {
    if (claimed.has(gate.route)) continue;
    rows.push({ key: `gate:${gate.route}`, decision: gate.route, scope: gate.scope, binding: null, gate });
  }
  return rows;
}
