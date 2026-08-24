import { icons } from "@upmo-ai/icons";
import { describe, expect, it } from "vitest";

import { shellConcepts } from "./icons";

const expected: Record<(typeof shellConcepts)[number], string> = {
  "nav.home": "home",
  "nav.chats": "message",
  "nav.tasks": "list-check",
  "nav.automations": "clock-play",
  "nav.agents": "users",
  "nav.memory": "brain",
  "nav.knowledge": "books",
  "nav.usage": "chart-bar",
  "nav.settings": "settings",
  "nav.sidebar": "layout-sidebar",
  "nav.notifications": "bell",
};

describe("shell icons", () => {
  it("matches the design-system map for every concept the module uses", () => {
    expect(shellConcepts).toHaveLength(Object.keys(expected).length);
    for (const concept of shellConcepts) {
      expect(icons[concept]).toBe(expected[concept]);
    }
  });
});
