import { describe, expect, it } from "vitest";

import { fillsMain } from "./layout";

describe("fillsMain", () => {
  it.each(["/chat", "/chat/", "/chat/roadmap-priorities"])("fills main at %s", (pathname) => {
    expect(fillsMain(pathname)).toBe(true);
  });

  it.each(["/", "/tasks", "/chatter", "/tasks/chat", "/admin/chat"])("leaves main alone at %s", (pathname) => {
    expect(fillsMain(pathname)).toBe(false);
  });
});
