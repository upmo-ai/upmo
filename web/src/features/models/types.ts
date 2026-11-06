/**
 * Whose row it is. Global rows are set for the whole workspace and read-only
 * for everyone. An own row belongs to the signed-in person.
 */
export type Scope = "global" | "own";

export const routeCapabilities = ["chat", "stream", "embed", "decide"] as const;

export type RouteCapability = (typeof routeCapabilities)[number];

/** Empty except on decide routes, which answer yes or no, or choose. */
export type RouteDecision = "" | "yesno" | "choice";

/** One link on a route, tried in position order. Never carries a credential. */
export interface RouteHop {
  position: number;
  adapter: string;
  model: string;
  weight: number;
  retry: number;
}

export interface GatewayRoute {
  name: string;
  scope: Scope;
  capability: RouteCapability;
  decision: RouteDecision;
  hops: RouteHop[];
}

export type GateKind = "yesno" | "choice" | "filter" | "rank";

export type GateVerdict = "pass" | "fail";

/** An accepted decision gate: how a decide route scored against its bar. */
export interface DecisionGate {
  route: string;
  scope: Scope;
  kind: GateKind;
  /** 64 hex characters that change when the route's models change. */
  fingerprint: string;
  verdict: GateVerdict;
  hit: number;
  total: number;
  /** Whole percent the score must reach, 1 to 100. */
  bar: number;
  /** RFC 3339, UTC. */
  updated_at: string;
}

/** The last time a decision was run against its gate. */
export interface DecisionEval {
  verdict: GateVerdict;
  /** RFC 3339, UTC. */
  at: string;
  /** Fingerprint of the gate the run was scored against. */
  gate: string;
}

/**
 * One decision and what it is bound to. Bound to nothing, the model and route
 * are null. Never run, last_eval is null.
 */
export interface DecisionBinding {
  decision: string;
  scope: Scope;
  model: string | null;
  route: string | null;
  last_eval: DecisionEval | null;
}
