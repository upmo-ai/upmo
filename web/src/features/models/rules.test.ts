import { describe, expect, it } from "vitest";

import { decisionRows, formatUpdated, gatePercent, meetsBar, shortFingerprint } from "./rules";
import { sampleDecisions, sampleGates } from "./sample";

describe("UPM-202 models rules", () => {
  it("UPM-202 rounds the score down so it never reads above the bar it missed", () => {
    expect(gatePercent({ hit: 179, total: 200 })).toBe(89);
    expect(gatePercent({ hit: 46, total: 50 })).toBe(92);
    expect(gatePercent({ hit: 0, total: 0 })).toBe(0);
  });

  it("UPM-202 meets the bar exactly on the bar and not a case under it", () => {
    expect(meetsBar({ hit: 38, total: 40, bar: 95 })).toBe(true);
    expect(meetsBar({ hit: 179, total: 200, bar: 90 })).toBe(false);
    expect(meetsBar({ hit: 180, total: 200, bar: 90 })).toBe(true);
    expect(meetsBar({ hit: 0, total: 0, bar: 1 })).toBe(false);
  });

  it("UPM-202 shortens a fingerprint and formats a stamp in UTC", () => {
    expect(shortFingerprint("c28d8340a482168634304d42920fb36f")).toBe("c28d8340a482");
    expect(formatUpdated("2026-11-04T09:12:00Z")).toBe("4 Nov 2026, 09:12 UTC");
    expect(formatUpdated("not a date")).toBe("");
  });
});

describe("UPM-51 decision rows", () => {
  it("UPM-51 joins a decision to the gate of its bound route and leaves unbound ones without", () => {
    const rows = decisionRows(sampleDecisions, sampleGates);
    expect(rows).toHaveLength(sampleDecisions.length);
    expect(rows.find((row) => row.decision === "agent-dispatch")?.gate?.route).toBe("agent-dispatch");
    expect(rows.find((row) => row.decision === "memory-check")?.gate).toBeNull();
    expect(rows.find((row) => row.decision === "tool-approval")?.gate).toBeNull();
  });

  it("UPM-51 keeps a gate that no decision claims", () => {
    const rows = decisionRows([], sampleGates);
    expect(rows.map((row) => row.decision)).toEqual(sampleGates.map((gate) => gate.route));
    expect(rows.every((row) => row.binding === null && row.gate !== null)).toBe(true);
  });

  it("UPM-51 gives an unbound decision the gate of the route that shares its name, in one row", () => {
    const gate = sampleGates[0]!;
    const unbound = { decision: gate.route, scope: "global" as const, model: null, route: null, last_eval: null };
    const rows = decisionRows([unbound], [gate]);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.binding).toBe(unbound);
    expect(rows[0]?.gate).toBe(gate);
  });

  it("UPM-51 carries the scope of the decision, or of the gate when no decision claims it", () => {
    const own = { ...sampleDecisions[0]!, decision: "mine", scope: "own" as const, route: null };
    const stray = { ...sampleGates[1]!, scope: "own" as const };
    const rows = decisionRows([own], [stray]);
    expect(rows.map((row) => [row.decision, row.scope])).toEqual([
      ["mine", "own"],
      [stray.route, "own"],
    ]);
  });
});
