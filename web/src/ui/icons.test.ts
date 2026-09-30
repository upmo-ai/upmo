import { icons } from "@upmo-ai/icons";
import { describe, expect, it } from "vitest";

import { shellConcepts } from "./icons";

const expected: Record<(typeof shellConcepts)[number], string> = {
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
  "action.send": "caret",
  "object.folder": "folder",
  "object.source": "file-search",
  "object.automation": "clock-play",
  "object.agent": "user-hexagon",
};

describe("shell icons", () => {
  it("matches the design-system map for every concept the module uses", () => {
    expect(shellConcepts).toHaveLength(Object.keys(expected).length);
    for (const concept of shellConcepts) {
      expect(icons[concept]).toBe(expected[concept]);
    }
  });
});
