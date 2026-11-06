import type { DecisionGate, GatewayRoute } from "./types";

export const sampleRoutes: GatewayRoute[] = [
  {
    name: "chat",
    capability: "chat",
    decision: "",
    hops: [
      { position: 1, adapter: "anthropic", model: "claude-3-7-sonnet", weight: 3, retry: 1 },
      { position: 2, adapter: "openai_compat", model: "gpt-4.1", weight: 1, retry: 1 },
      { position: 3, adapter: "ollama", model: "llama3.3:70b", weight: 1, retry: 0 },
    ],
  },
  {
    name: "chat-stream",
    capability: "stream",
    decision: "",
    hops: [
      { position: 1, adapter: "anthropic", model: "claude-3-7-sonnet", weight: 1, retry: 1 },
      { position: 2, adapter: "openai_compat", model: "gpt-4.1", weight: 1, retry: 1 },
    ],
  },
  {
    name: "embed",
    capability: "embed",
    decision: "",
    hops: [{ position: 1, adapter: "openai_compat", model: "text-embedding-3-small", weight: 1, retry: 2 }],
  },
  {
    name: "agent-dispatch",
    capability: "decide",
    decision: "choice",
    hops: [
      { position: 1, adapter: "anthropic", model: "claude-3-5-haiku", weight: 1, retry: 1 },
      { position: 2, adapter: "openai_compat", model: "gpt-4.1-mini", weight: 1, retry: 1 },
    ],
  },
  {
    name: "route-intent",
    capability: "decide",
    decision: "choice",
    hops: [{ position: 1, adapter: "anthropic", model: "claude-3-5-haiku", weight: 1, retry: 1 }],
  },
  {
    name: "result-confidence",
    capability: "decide",
    decision: "yesno",
    hops: [
      { position: 1, adapter: "openai_compat", model: "gpt-4.1-mini", weight: 2, retry: 1 },
      { position: 2, adapter: "ollama", model: "deepseek-r1:14b", weight: 1, retry: 0 },
    ],
  },
  {
    name: "fact-keep",
    capability: "decide",
    decision: "yesno",
    hops: [{ position: 1, adapter: "anthropic", model: "claude-3-5-haiku", weight: 1, retry: 1 }],
  },
  {
    name: "model-tier",
    capability: "decide",
    decision: "choice",
    hops: [{ position: 1, adapter: "openai_compat", model: "gpt-4.1-mini", weight: 1, retry: 1 }],
  },
  {
    name: "memory-check",
    capability: "decide",
    decision: "yesno",
    hops: [{ position: 1, adapter: "ollama", model: "llama3.3:70b", weight: 1, retry: 0 }],
  },
];

export const sampleGates: DecisionGate[] = [
  {
    route: "agent-dispatch",
    kind: "choice",
    fingerprint: "c28d8340a482168634304d42920fb36fe953ae58b88dc01920020c2625054c1d",
    verdict: "pass",
    hit: 46,
    total: 50,
    bar: 90,
    updated_at: "2026-11-04T09:12:00Z",
  },
  {
    route: "route-intent",
    kind: "choice",
    fingerprint: "2973b3a452eaff9fc809b140f05e0ba0382368184de44c67305b6728353de6a9",
    verdict: "pass",
    hit: 36,
    total: 40,
    bar: 90,
    updated_at: "2026-11-03T16:40:00Z",
  },
  {
    route: "result-confidence",
    kind: "yesno",
    fingerprint: "5c4a19b70e00c0b5c2e7fde907be29aee64f08db46c1e0e4c62abfda4b875c73",
    verdict: "pass",
    hit: 93,
    total: 100,
    bar: 90,
    updated_at: "2026-11-02T11:05:00Z",
  },
  {
    route: "fact-keep",
    kind: "yesno",
    fingerprint: "1f857e8a5a31a60326a5f8585057c48cf00d1e129b2ac10f685f967c7f450ca0",
    verdict: "pass",
    hit: 38,
    total: 40,
    bar: 95,
    updated_at: "2026-11-01T08:30:00Z",
  },
  {
    route: "model-tier",
    kind: "choice",
    fingerprint: "451e035e6513a124671ed4ecfff6c0a1c9f11d23da86ccbaa8cc7bde63208c2f",
    verdict: "fail",
    hit: 27,
    total: 30,
    bar: 95,
    updated_at: "2026-10-30T14:20:00Z",
  },
];
