import { describe, expect, it } from "vitest";

import { awaitingReply, chatDay, formatActivity, formatCost, groupByDay } from "./format";
import type { ChatLine, ChatSummary } from "./types";

// 5 November 2026, 10:30 local time.
const now = new Date(2026, 10, 5, 10, 30);

function at(day: number, hour: number, minute = 0) {
  return new Date(2026, 10, day, hour, minute).toISOString();
}

function chat(id: string, last_activity: string): ChatSummary {
  return { id, title: id, preview: "", last_activity };
}

function line(role: ChatLine["role"]): ChatLine {
  return { id: role, role, body: "", model: null, tokens: 1, cost_usd: null, at: at(5, 9) };
}

describe("UPM-201 chat list days", () => {
  it("UPM-201 puts a chat in Today, Yesterday or Earlier by calendar day", () => {
    expect(chatDay(at(5, 0, 1), now)).toBe("Today");
    expect(chatDay(at(4, 23, 59), now)).toBe("Yesterday");
    expect(chatDay(at(4, 0, 0), now)).toBe("Yesterday");
    expect(chatDay(at(3, 23, 59), now)).toBe("Earlier");
  });

  it("UPM-201 keeps the given order inside a day and drops empty days", () => {
    const groups = groupByDay([chat("b", at(5, 9)), chat("a", at(5, 8)), chat("c", at(1, 8))], now);

    expect(groups.map((group) => group.day)).toEqual(["Today", "Earlier"]);
    expect(groups[0]?.chats.map((item) => item.id)).toEqual(["b", "a"]);
    expect(groups[0]?.chats.map((item) => item.time)).toEqual(["09:00", "08:00"]);
    expect(groups[1]?.chats[0]?.time).toBe("1 Nov");
  });

  it("UPM-201 shows a time for recent chats and a date for older ones", () => {
    expect(formatActivity(at(5, 9, 42), now)).toBe("09:42");
    expect(formatActivity(at(4, 16, 30), now)).toBe("16:30");
    expect(formatActivity(at(1, 11, 5), now)).toBe("1 Nov");
  });
});

describe("UPM-201 chat cost", () => {
  it("UPM-201 shows an unknown cost as Unknown", () => {
    expect(formatCost(null)).toBe("Unknown");
  });

  it("UPM-201 keeps a known sub-cent cost from rounding to nothing", () => {
    expect(formatCost(0.0002)).toBe("US$0.0002");
    expect(formatCost(0.002)).toBe("US$0.002");
    expect(formatCost(1.5)).toBe("US$1.50");
    expect(formatCost(0.00001)).toBe("Under US$0.0001");
  });

  it("UPM-201 shows a genuine zero as zero, distinct from Unknown", () => {
    expect(formatCost(0)).toBe("US$0.00");
    expect(formatCost(0)).not.toBe(formatCost(null));
  });
});

describe("UPM-201 awaiting a reply", () => {
  it("UPM-201 waits only when the newest line is the user's", () => {
    expect(awaitingReply(undefined)).toBe(false);
    expect(awaitingReply([])).toBe(false);
    expect(awaitingReply([line("user")])).toBe(true);
    expect(awaitingReply([line("user"), line("assistant")])).toBe(false);
  });
});
