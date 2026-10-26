import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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
    expect(within(composer).getByRole("button", { name: /Agent, route and model: Auto · Default · Gpt-5\.6-Luna/ })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Attach file" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Dictate" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: /Tools mode: Auto/ })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Send" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy reply" })).toBeTruthy();
    expect(screen.getByText(/to send/)).toBeTruthy();
  });

  it("sends on Enter and appends the message with its reply", async () => {
    renderChat();
    const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;

    fireEvent.change(box, { target: { value: "Draft the release note" } });
    fireEvent.keyDown(box, { key: "Enter" });

    expect(screen.getByText("Draft the release note")).toBeTruthy();
    expect(screen.getByText("I have processed your request with Gpt-5.6-Luna via the Default route.")).toBeTruthy();
    expect(box.value).toBe("");
    expect(screen.getAllByRole("button", { name: "Copy reply" })).toHaveLength(2);
  });

  it("sends on clicking Send and clears the draft", async () => {
    renderChat();
    const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;

    fireEvent.change(box, { target: { value: "Click to send" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(screen.getByText("Click to send")).toBeTruthy();
    expect(screen.getByText("I have processed your request with Gpt-5.6-Luna via the Default route.")).toBeTruthy();
    expect(box.value).toBe("");
    expect(screen.getAllByRole("button", { name: "Copy reply" })).toHaveLength(2);
    expect(screen.getByRole("button", { name: "Send" }).getAttribute("aria-disabled")).toBe("true");
  });

  it("uses the label of a chosen Agent, Route and Model, not its value", async () => {
    renderChat();
    const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;

    async function choose(group: string, option: string) {
      const pill = screen.getByRole("button", { name: /Agent, route and model:/ });
      if (pill.getAttribute("aria-expanded") !== "true") fireEvent.click(pill);
      const groupItem = await screen.findByRole("menuitem", { name: group, hidden: true });
      fireEvent.mouseEnter(groupItem);
      fireEvent.click(await screen.findByRole("menuitemradio", { name: option, hidden: true }));
    }

    await choose("Agent", "Code Specialist");
    await choose("Route", "Cost Optimized");
    await choose("Model", "Claude 3.7 Sonnet");

    const pill = screen.getByRole("button", { name: /Agent, route and model:/ });
    expect(pill.getAttribute("aria-label")).toBe(
      "Agent, route and model: Code Specialist · Cost Optimized · Claude 3.7 Sonnet",
    );
    expect(pill.textContent).toContain("Code Specialist · Cost Optimized · Claude 3.7 Sonnet");
    expect(pill.textContent).not.toContain("claude-3.7-sonnet");

    fireEvent.change(box, { target: { value: "Which model answers?" } });
    fireEvent.keyDown(box, { key: "Enter" });

    expect(
      screen.getByText("I have processed your request with Claude 3.7 Sonnet via the Cost Optimized route."),
    ).toBeTruthy();
    // The reply meta line shows the model label.
    expect(screen.getByText(/Claude 3\.7 Sonnet\s*·/)).toBeTruthy();
    expect(screen.queryByText(/claude-3\.7-sonnet/)).toBeNull();
  });

  it("keeps the draft on Shift+Enter", async () => {
    renderChat();
    const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;

    fireEvent.change(box, { target: { value: "First line" } });
    fireEvent.keyDown(box, { key: "Enter", shiftKey: true });

    expect(box.value).toBe("First line");
    expect(screen.getAllByRole("button", { name: "Copy reply" })).toHaveLength(1);
  });

  it("disables Send while the draft is empty", async () => {
    renderChat();
    const box = await screen.findByRole("textbox", { name: "Message" });
    const send = screen.getByRole("button", { name: "Send" });

    expect(send.getAttribute("aria-disabled")).toBe("true");
    fireEvent.change(box, { target: { value: "   " } });
    expect(send.getAttribute("aria-disabled")).toBe("true");
    fireEvent.change(box, { target: { value: "Hello" } });
    expect(send.getAttribute("aria-disabled")).toBeNull();
  });
});
