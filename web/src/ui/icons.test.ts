import { icons } from "@upmo-ai/icons";
import { describe, expect, it } from "vitest";

import { shellConcepts } from "./icons";

const expected: Record<Exclude<(typeof shellConcepts)[number], "object.source">, string> = {
  "nav.home": "home",
  "nav.chats": "message",
  "nav.tasks": "list-check",
  "nav.automations": "clock-play",
  "nav.agents": "users",
  "nav.knowledge": "books",
  "nav.usage": "chart-bar",
  "nav.settings": "settings",
  "nav.sidebar": "layout-sidebar",
  "nav.notifications": "bell",
  "action.edit": "pencil",
  "action.delete": "trash",
  "action.remove": "x",
  "action.add": "plus",
  "action.copy": "copy",
  "action.more": "dots",
  "action.send": "arrow-up",
  "object.folder": "folder",
  "object.automation": "clock-play",
  "object.agent": "user-hexagon",
};

describe("shell icons", () => {
  it("matches the design-system map for every concept the module uses", () => {
    expect(shellConcepts).toHaveLength(Object.keys(expected).length + 1);
    for (const [concept, name] of Object.entries(expected)) {
      expect(icons[concept as keyof typeof expected]).toBe(name);
    }
  });

  it("keeps object.source local only while the design-system map lacks it", () => {
    expect(shellConcepts).toContain("object.source");
    expect(Object.keys(icons)).not.toContain("object.source");
  });
});
