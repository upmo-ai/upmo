import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";

function renderChat(path = "/chat") {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
  return history;
}

// jsdom does no layout, so give the scrolling log a height and a scroll
// position the thread can read and move.
function scrollable(log: HTMLElement) {
  const state = { top: 0 };
  Object.defineProperty(log, "scrollHeight", { configurable: true, get: () => 1000 });
  Object.defineProperty(log, "clientHeight", { configurable: true, get: () => 200 });
  Object.defineProperty(log, "scrollTop", {
    configurable: true,
    get: () => state.top,
    set: (value: number) => {
      state.top = value;
    },
  });
  return state;
}

// The reply is also spoken through the thread's status line, so look for it
// in the message log.
function inLog(text: string) {
  return within(screen.getByRole("log", { name: "Messages" })).getByText(text);
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
    expect(inLog("I have processed your request with Gpt-5.6-Luna via the Default route.")).toBeTruthy();
    expect(box.value).toBe("");
    expect(screen.getAllByRole("button", { name: "Copy reply" })).toHaveLength(2);
  });

  it("sends on clicking Send and clears the draft", async () => {
    renderChat();
    const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;

    fireEvent.change(box, { target: { value: "Click to send" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(screen.getByText("Click to send")).toBeTruthy();
    expect(inLog("I have processed your request with Gpt-5.6-Luna via the Default route.")).toBeTruthy();
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
      inLog("I have processed your request with Claude 3.7 Sonnet via the Cost Optimized route."),
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

  it("UPM-196 lists the messages in a named log, apart from the composer", async () => {
    renderChat();
    const log = await screen.findByRole("log", { name: "Messages" });

    expect(within(log).getAllByRole("button", { name: "Copy reply" })).toHaveLength(1);
    expect(within(log).getByText(/Summarise the launch notes/)).toBeTruthy();
    // The composer sits outside the log, so only the thread scrolls.
    expect(within(log).queryByRole("form", { name: "Chat composer" })).toBeNull();
  });

  it("UPM-196 returns to the newest message when a message is sent from further up", async () => {
    renderChat();
    const log = await screen.findByRole("log", { name: "Messages" });
    const scroll = scrollable(log);

    // The reader scrolls up: the thread lets go of the newest message.
    scroll.top = 300;
    fireEvent.scroll(log);
    expect(screen.getByRole("button", { name: "Jump to newest" })).toBeTruthy();

    const box = screen.getByRole("textbox", { name: "Message" });
    fireEvent.change(box, { target: { value: "Where did we get to?" } });
    fireEvent.keyDown(box, { key: "Enter" });

    await waitFor(() => {
      expect(scroll.top).toBe(1000);
    });
    expect(within(log).getByText("Where did we get to?")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Jump to newest" })).toBeNull();
  });

  it("UPM-196 fills the main region on the chat page only", async () => {
    const history = renderChat();
    await screen.findByRole("heading", { level: 1, name: "Chat" });
    const main = screen.getByRole("main");
    expect(main.getAttribute("data-fill")).toBe("true");
    // The chat page keeps the inset the other routes have, through this class.
    expect(main.classList.contains("shell-main-fill")).toBe(true);

    act(() => {
      history.push("/tasks");
    });
    await screen.findByRole("heading", { level: 1, name: "Tasks" });
    expect(screen.getByRole("main").hasAttribute("data-fill")).toBe(false);
    expect(screen.getByRole("main").classList.contains("shell-main-fill")).toBe(false);
  });

  it("fills the main region at /chat/ with a trailing slash", async () => {
    renderChat("/chat/");
    await screen.findByRole("heading", { level: 1, name: "Chat" });
    expect(screen.getByRole("main").getAttribute("data-fill")).toBe("true");
    expect(screen.getByRole("main").classList.contains("shell-main-fill")).toBe(true);
  });

  it("announces the reply once it is added, and again when the same reply comes back", async () => {
    renderChat();
    const log = await screen.findByRole("log", { name: "Messages" });
    const thread = log.closest<HTMLElement>(".upmo-chat-thread");
    if (!thread) throw new Error("the log is not inside a chat thread");
    const status = () => within(thread).getByRole("status");
    const reply = "I have processed your request with Gpt-5.6-Luna via the Default route.";
    // The log stays silent; the status line carries the reply instead.
    expect(log.getAttribute("aria-live")).toBe("off");
    expect(status().textContent).toBe("");

    const box = screen.getByRole("textbox", { name: "Message" });
    fireEvent.change(box, { target: { value: "First question" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => {
      expect(status().textContent).toBe(reply);
    });

    // Starting the next message resets the line, so an identical reply is
    // announced again rather than treated as no change.
    fireEvent.change(box, { target: { value: "Second question" } });
    await waitFor(() => {
      expect(status().textContent).toBe("");
    });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => {
      expect(status().textContent).toBe(reply);
    });
  });
});
