import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";

function renderChat() {
  const history = createMemoryHistory({ initialEntries: ["/chat"] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

describe("Chat page", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("renders the chat list, current chat and composer", async () => {
    renderChat();

    expect(await screen.findByRole("heading", { level: 1, name: "Chat" })).toBeTruthy();
    const list = screen.getByRole("navigation", { name: "Chat list" });
    expect(within(list).getByRole("textbox", { name: "Search chats" })).toBeTruthy();
    expect(within(list).getByRole("button", { name: "New chat" })).toBeTruthy();

    const current = within(list).getByRole("button", { name: /Roadmap priorities/ });
    expect(current.getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("heading", { level: 2, name: "Roadmap priorities" })).toBeTruthy();

    const composer = screen.getByRole("form", { name: "Chat composer" });
    expect(within(composer).getByRole("textbox", { name: "Message" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Agent, route, and model configuration" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Attach file" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Dictate" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Tools mode" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Send" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy reply" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Activity" })).toBeTruthy();
    expect(screen.getByText(/to send/)).toBeTruthy();
  });
});
