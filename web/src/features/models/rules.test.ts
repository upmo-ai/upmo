import { describe, expect, it } from "vitest";

import { canManageSettings, formatUpdated, gatePercent, meetsBar, shortFingerprint } from "./rules";

describe("UPM-202 models rules", () => {
  it("UPM-202 gives settings access to owners and admins only", () => {
    expect(canManageSettings("owner")).toBe(true);
    expect(canManageSettings("admin")).toBe(true);
    expect(canManageSettings("member")).toBe(false);
    expect(canManageSettings("viewer")).toBe(false);
    expect(canManageSettings("")).toBe(false);
  });

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
