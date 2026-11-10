import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient, type Chat, type ChatSummary } from "../../app/api";
import { AppProviders } from "../../theme";

// A reply that lands at once, so most tests do not wait for the stub's delay.
const quick = () => createStubApi({ replyDelayMs: 0 });

function renderChat(path = "/chat", client: ApiClient = quick()) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
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

function messageLog() {
  return screen.getByRole("log", { name: "Messages" });
}

// The page loads with its route, so wait for it.
function findChatList() {
  return screen.findByRole("navigation", { name: "Chat list" });
}

async function type(text: string) {
  const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;
  fireEvent.change(box, { target: { value: text } });
  return box;
}

const replyTimeout = { timeout: 4000 };

describe("Chat page", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders the chat list, current chat and composer", async () => {
    renderChat();

    expect(await screen.findByRole("heading", { level: 1, name: "Chat" })).toBeTruthy();
    const list = await findChatList();
    expect(within(list).getByRole("textbox", { name: "Search chats" })).toBeTruthy();
    expect(within(list).getByRole("button", { name: "New chat" })).toBeTruthy();

    const current = await within(list).findByRole("button", { name: /Roadmap priorities/ });
    expect(current.getAttribute("aria-current")).toBe("page");
    expect(await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" })).toBeTruthy();

    const composer = screen.getByRole("form", { name: "Chat composer" });
    expect(within(composer).getByRole("textbox", { name: "Message" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: /Agent, route and model: Auto · Default · Gpt-5\.6-Luna/ })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Attach file" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Dictate" })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: /Tools mode: Auto/ })).toBeTruthy();
    expect(within(composer).getByRole("button", { name: "Send" })).toBeTruthy();
    expect(await screen.findByRole("button", { name: "Copy reply" })).toBeTruthy();
    expect(screen.getByText(/to send/)).toBeTruthy();
  });

  it("UPM-201 sends a line, shows thinking, then shows the reply with its model, tokens and cost", async () => {
    renderChat("/chat", createStubApi({ replyDelayMs: 250 }));
    const box = await type("Draft the release note");
    await screen.findByRole("button", { name: "Copy reply" });

    fireEvent.keyDown(box, { key: "Enter" });

    expect(await within(messageLog()).findByText("Draft the release note")).toBeTruthy();
    expect(box.value).toBe("");
    // The reply is awaited: a visible state, and Send is blocked meanwhile.
    expect(await within(messageLog()).findByText("Thinking")).toBeTruthy();
    expect(messageLog().getAttribute("aria-busy")).toBe("true");
    fireEvent.change(box, { target: { value: "Another" } });
    expect(screen.getByRole("button", { name: "Send" }).getAttribute("aria-disabled")).toBe("true");

    expect(await within(messageLog()).findByText(/A stubbed reply to: "Draft the release note"/, {}, replyTimeout)).toBeTruthy();
    expect(within(messageLog()).queryByText("Thinking")).toBeNull();
    expect(messageLog().getAttribute("aria-busy")).toBe("false");
    expect(screen.getAllByRole("button", { name: "Copy reply" })).toHaveLength(2);
    // The new reply's meta line: the model that wrote it, tokens and a known cost.
    const meta = within(messageLog()).getAllByText(/claude-3-7-sonnet · \d+ tokens · /);
    expect(meta.at(-1)?.textContent).toMatch(/claude-3-7-sonnet · \d+ tokens · US\$0\.\d+/);
    // Send is open again for the next line.
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Send" }).getAttribute("aria-disabled")).toBeNull();
    });
  });

  it("sends on clicking Send and clears the draft", async () => {
    renderChat();
    const box = await type("Click to send");

    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(await within(messageLog()).findByText("Click to send")).toBeTruthy();
    expect(box.value).toBe("");
    expect(await within(messageLog()).findByText(/A stubbed reply to: "Click to send"/, {}, replyTimeout)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send" }).getAttribute("aria-disabled")).toBe("true");
  });

  it("uses the label of a chosen Agent, Route and Model, not its value", async () => {
    renderChat();
    await screen.findByRole("textbox", { name: "Message" });

    async function choose(group: string, option: string) {
      const pill = screen.getByRole("button", { name: /Agent, route and model:/ });
      if (pill.getAttribute("aria-expanded") !== "true") fireEvent.click(pill);
      const groupItem = await screen.findByRole("menuitem", { name: group, hidden: true });
      fireEvent.mouseEnter(groupItem);
      fireEvent.click(await screen.findByRole("menuitemradio", { name: option, hidden: true }));
    }

    await choose("Agent", "Code Specialist");
    await choose("Route", "chat-stream");
    await choose("Model", "Claude 3.7 Sonnet");

    const pill = screen.getByRole("button", { name: /Agent, route and model:/ });
    expect(pill.getAttribute("aria-label")).toBe(
      "Agent, route and model: Code Specialist · chat-stream · Claude 3.7 Sonnet",
    );
    expect(pill.textContent).toContain("Code Specialist · chat-stream · Claude 3.7 Sonnet");
    expect(pill.textContent).not.toContain("claude-3.7-sonnet");
  });

  it("keeps the draft on Shift+Enter", async () => {
    renderChat();
    const box = await type("First line");
    await screen.findByRole("button", { name: "Copy reply" });

    fireEvent.keyDown(box, { key: "Enter", shiftKey: true });

    expect(box.value).toBe("First line");
    expect(screen.getAllByRole("button", { name: "Copy reply" })).toHaveLength(1);
  });

  it("disables Send while the draft is empty", async () => {
    renderChat();
    const box = await screen.findByRole("textbox", { name: "Message" });
    await screen.findByRole("button", { name: "Copy reply" });
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

    expect(await within(log).findAllByRole("button", { name: "Copy reply" })).toHaveLength(1);
    expect(within(log).getByText(/Summarise the launch notes/)).toBeTruthy();
    // The composer sits outside the log, so only the thread scrolls.
    expect(within(log).queryByRole("form", { name: "Chat composer" })).toBeNull();
  });

  it("UPM-196 returns to the newest message when a message is sent from further up", async () => {
    renderChat();
    const log = await screen.findByRole("log", { name: "Messages" });
    await within(log).findByText(/Summarise the launch notes/);
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
    expect(await within(log).findByText("Where did we get to?")).toBeTruthy();
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
    const reply = 'A stubbed reply to: "Same question". The real answer arrives here once the brain serves chats.';
    // The log stays silent; the status line carries the reply instead.
    expect(log.getAttribute("aria-live")).toBe("off");
    expect(status().textContent).toBe("");

    const box = await type("Same question");
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => {
      expect(status().textContent).toBe(reply);
    }, replyTimeout);

    // Starting the next message resets the line, so an identical reply is
    // announced again rather than treated as no change.
    fireEvent.change(box, { target: { value: "Same question" } });
    await waitFor(() => {
      expect(status().textContent).toBe("");
    });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => {
      expect(status().textContent).toBe(reply);
    }, replyTimeout);
  });
});

describe("UPM-201 chats from the API", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("UPM-201 lists the chats the API returns, not a fixed sample", async () => {
    const base = quick();
    const only: ChatSummary[] = [
      { id: "q3", title: "Quarterly numbers", preview: "Revenue is up.", last_activity: new Date().toISOString() },
    ];
    const client: ApiClient = {
      ...base,
      chats: {
        ...base.chats,
        list: async () => only,
        get: async (id): Promise<Chat> => ({
          id,
          lines: [
            { id: "l1", role: "user", body: "How did Q3 go?", model: null, tokens: 5, cost_usd: 0.00005, at: only[0]!.last_activity },
          ],
        }),
      },
    };
    renderChat("/chat", client);

    const list = await findChatList();
    expect(await within(list).findByRole("button", { name: /Quarterly numbers/ })).toBeTruthy();
    expect(within(list).queryByRole("button", { name: /Roadmap priorities/ })).toBeNull();
    expect(await screen.findByRole("heading", { level: 2, name: "Quarterly numbers" })).toBeTruthy();
    expect(await within(messageLog()).findByText("How did Q3 go?")).toBeTruthy();
  });

  it("UPM-201 opens another chat and shows its own lines", async () => {
    renderChat();
    const list = await findChatList();
    await within(list).findByRole("button", { name: /Roadmap priorities/ });

    fireEvent.click(within(list).getByRole("button", { name: /Research summary/ }));

    expect(await screen.findByRole("heading", { level: 2, name: "Research summary" })).toBeTruthy();
    expect(await within(messageLog()).findByText(/What did the interviews tell us/)).toBeTruthy();
    expect(within(messageLog()).queryByText(/Summarise the launch notes/)).toBeNull();
    expect(within(list).getByRole("button", { name: /Research summary/ }).getAttribute("aria-current")).toBe("page");
    expect(within(list).getByRole("button", { name: /Roadmap priorities/ }).getAttribute("aria-current")).toBeNull();
  });

  it("UPM-201 starts a new chat, names it from the first line and moves it to the top", async () => {
    renderChat();
    const list = await findChatList();
    await within(list).findByRole("button", { name: /Roadmap priorities/ });

    fireEvent.click(within(list).getByRole("button", { name: "New chat" }));

    expect(await screen.findByRole("heading", { level: 2, name: "New chat" })).toBeTruthy();
    expect(await screen.findByText("Write a message to start this chat.")).toBeTruthy();
    expect(within(messageLog()).queryByRole("button", { name: "Copy reply" })).toBeNull();
    // A blank chat is not started twice.
    fireEvent.click(within(list).getByRole("button", { name: "New chat" }));
    expect(within(list).getAllByRole("button", { name: /^New chat/ })).toHaveLength(2);

    const box = await type("Plan the offsite");
    fireEvent.keyDown(box, { key: "Enter" });

    expect(await screen.findByRole("heading", { level: 2, name: "Plan the offsite" })).toBeTruthy();
    expect(await within(messageLog()).findByText(/A stubbed reply to: "Plan the offsite"/, {}, replyTimeout)).toBeTruthy();
    const items = within(list).getAllByRole("button", { name: /\d\d:\d\d$|\d+ \w{3}$/ });
    expect(items[0]?.textContent).toContain("Plan the offsite");
    expect(items[0]?.getAttribute("aria-current")).toBe("page");
  });

  it("UPM-201 shows an unknown cost as Unknown, never as zero", async () => {
    renderChat();
    const list = await findChatList();
    fireEvent.click(await within(list).findByRole("button", { name: /Supplier review/ }));

    const reply = await within(messageLog()).findByText(/Start with price stability/);
    const body = reply.closest(".upmo-chat-msg");
    expect(body?.textContent).toContain("llama3.3:70b · 61 tokens · Unknown");
    expect(messageLog().textContent).not.toMatch(/\$0\.00(?!\d)/);
    // The user line in the same chat has an unknown cost too.
    expect(within(messageLog()).getByText("16 tokens · Unknown")).toBeTruthy();
  });

  it("UPM-201 keeps a known cost under a cent visible", async () => {
    renderChat();
    expect(await within(await screen.findByRole("log", { name: "Messages" })).findByText("18 tokens · US$0.0002")).toBeTruthy();
  });

  it("UPM-201 gives the words back and says why when a line cannot be sent", async () => {
    const base = quick();
    const client: ApiClient = {
      ...base,
      chats: {
        ...base.chats,
        send: async () => {
          throw new Error("The brain is offline.");
        },
      },
    };
    renderChat("/chat", client);
    const box = await type("This will not send");
    await screen.findByRole("button", { name: "Copy reply" });

    fireEvent.keyDown(box, { key: "Enter" });

    expect(await screen.findByText("The brain is offline.")).toBeTruthy();
    await waitFor(() => {
      expect(box.value).toBe("This will not send");
    });
    expect(within(messageLog()).queryByText("This will not send")).toBeNull();
  });

  it("UPM-201 sends nothing more while a reply is awaited", async () => {
    const base = createStubApi({ replyDelayMs: 60_000 });
    const send = vi.fn<ApiClient["chats"]["send"]>(base.chats.send);
    const client: ApiClient = { ...base, chats: { ...base.chats, send } };
    renderChat("/chat", client);
    const box = await type("First");
    await screen.findByRole("button", { name: "Copy reply" });
    fireEvent.keyDown(box, { key: "Enter" });
    await within(messageLog()).findByText("Thinking");

    fireEvent.change(box, { target: { value: "Second" } });
    fireEvent.keyDown(box, { key: "Enter" });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(send).toHaveBeenCalledTimes(1);
    expect(box.value).toBe("Second");
  });

  it("UPM-201 returns to a chat that is still awaiting its reply and finds the reply", async () => {
    renderChat("/chat", createStubApi({ replyDelayMs: 700 }));
    const list = await findChatList();
    const box = await type("Slow question");
    await within(list).findByRole("button", { name: /Roadmap priorities/ });
    fireEvent.keyDown(box, { key: "Enter" });
    await within(messageLog()).findByText("Thinking");

    fireEvent.click(within(list).getByRole("button", { name: /Research summary/ }));
    await within(messageLog()).findByText(/What did the interviews tell us/);
    fireEvent.click(within(list).getByRole("button", { name: /Roadmap priorities/ }));

    expect(await within(messageLog()).findByText(/A stubbed reply to: "Slow question"/, {}, replyTimeout)).toBeTruthy();
    expect(within(messageLog()).queryByText("Thinking")).toBeNull();
  });

  it("UPM-201 reads out the reply in the chat it was sent in after a line is sent in another chat", async () => {
    renderChat("/chat", createStubApi({ replyDelayMs: 500 }));
    const list = await findChatList();
    const log = await screen.findByRole("log", { name: "Messages" });
    const thread = log.closest<HTMLElement>(".upmo-chat-thread");
    if (!thread) throw new Error("the log is not inside a chat thread");
    const box = await type("Slow question");
    await within(list).findByRole("button", { name: /Roadmap priorities/ });
    fireEvent.keyDown(box, { key: "Enter" });
    await within(log).findByText("Thinking");

    // A line is sent in a second chat while the first is still awaited.
    fireEvent.click(within(list).getByRole("button", { name: /Research summary/ }));
    await within(log).findByText(/What did the interviews tell us/);
    fireEvent.change(box, { target: { value: "Other question" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await within(log).findByText("Thinking");

    fireEvent.click(within(list).getByRole("button", { name: /Roadmap priorities/ }));

    await waitFor(() => {
      expect(within(thread).getByRole("status").textContent).toContain('A stubbed reply to: "Slow question"');
    }, replyTimeout);
  });

  it("UPM-201 leaves another chat's scroll alone when a line is sent in this one", async () => {
    const base = quick();
    let release: () => void = () => {};
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const list = vi.fn<ApiClient["chats"]["list"]>(base.chats.list);
    const client: ApiClient = {
      ...base,
      chats: {
        ...base.chats,
        list,
        send: async (id, body) => {
          await held;
          return base.chats.send(id, body);
        },
      },
    };
    renderChat("/chat", client);
    const nav = await findChatList();
    const log = await screen.findByRole("log", { name: "Messages" });
    const scroll = scrollable(log);
    const box = await type("Sent from the first chat");
    await within(nav).findByRole("button", { name: /Roadmap priorities/ });
    fireEvent.keyDown(box, { key: "Enter" });

    // The reader moves to another chat and scrolls up in it before the first send lands.
    fireEvent.click(within(nav).getByRole("button", { name: /Research summary/ }));
    await within(log).findByText(/What did the interviews tell us/);
    scroll.top = 300;
    fireEvent.scroll(log);
    const reads = list.mock.calls.length;
    release();
    await waitFor(() => {
      expect(list.mock.calls.length).toBeGreaterThan(reads);
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 100));
    });

    expect(scroll.top).toBe(300);
  });

  it("UPM-201 keeps the open chat when the list changes order", async () => {
    const base = quick();
    let reversed = false;
    const client: ApiClient = {
      ...base,
      chats: {
        ...base.chats,
        list: async () => {
          const items = await base.chats.list();
          return reversed ? items.reverse() : items;
        },
      },
    };
    renderChat("/chat", client);
    const nav = await findChatList();
    expect(await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" })).toBeTruthy();
    await within(nav).findByRole("button", { name: /Supplier review/ });

    // Sending reads the list again, and now it comes back the other way up.
    reversed = true;
    const box = await type("Stay on this chat");
    fireEvent.keyDown(box, { key: "Enter" });
    expect(await within(messageLog()).findByText("Stay on this chat")).toBeTruthy();
    await within(messageLog()).findByText(/A stubbed reply to: "Stay on this chat"/, {}, replyTimeout);

    expect(screen.getByRole("heading", { level: 2, name: "Roadmap priorities" })).toBeTruthy();
    expect(within(nav).getByRole("button", { name: /Roadmap priorities/ }).getAttribute("aria-current")).toBe("page");
    expect(within(messageLog()).getByText("Stay on this chat")).toBeTruthy();
  });

  it("UPM-201 filters the list by title and says when nothing matches", async () => {
    renderChat();
    const list = await findChatList();
    await within(list).findByRole("button", { name: /Roadmap priorities/ });

    fireEvent.change(within(list).getByRole("textbox", { name: "Search chats" }), { target: { value: "supplier" } });
    expect(within(list).getByRole("button", { name: /Supplier review/ })).toBeTruthy();
    expect(within(list).queryByRole("button", { name: /Roadmap priorities/ })).toBeNull();

    fireEvent.change(within(list).getByRole("textbox", { name: "Search chats" }), { target: { value: "zzz" } });
    expect(within(list).getByText("No chats match.")).toBeTruthy();
  });
});

describe("UPM-201 chat list at phone width", () => {
  const width = window.innerWidth;

  afterEach(() => {
    cleanup();
    window.innerWidth = width;
  });

  it("UPM-201 starts with the list shut, opens it on request and shuts it after a chat is picked", async () => {
    window.innerWidth = 390;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });

    // Shut: the drawer is out of the tab order and the page offers a way back in.
    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("false");
    expect(document.querySelector("#chat-list")?.hasAttribute("inert")).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Show chats" }));
    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("true");
    expect(document.querySelector("#chat-list")?.hasAttribute("inert")).toBe(false);

    fireEvent.click(within(await findChatList()).getByRole("button", { name: /Supplier review/ }));
    expect(await screen.findByRole("heading", { level: 2, name: "Supplier review" })).toBeTruthy();
    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("false");
    expect(screen.getByRole("button", { name: "Show chats" })).toBeTruthy();
  });

  it("UPM-201 starts with the list open on a wide screen", async () => {
    window.innerWidth = 1280;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });

    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("true");
    expect(screen.queryByRole("button", { name: "Show chats" })).toBeNull();
  });

  it("UPM-201 moves focus to Show chats when the sidebar is collapsed and into the list when it is shown", async () => {
    window.innerWidth = 1280;
    renderChat();
    const list = await findChatList();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    const collapse = within(list).getByRole("button", { name: "Collapse sidebar" });
    collapse.focus();

    fireEvent.click(collapse);

    const show = screen.getByRole("button", { name: "Show chats" });
    expect(document.activeElement).toBe(show);
    expect(document.querySelector("#chat-list")?.hasAttribute("inert")).toBe(true);

    fireEvent.click(show);

    expect(document.activeElement).toBe(within(list).getByRole("textbox", { name: "Search chats" }));
    expect(screen.queryByRole("button", { name: "Show chats" })).toBeNull();
  });

  it("UPM-201 keeps the Show chats control in the page while it is hidden", async () => {
    window.innerWidth = 1280;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });

    // Hidden from the page's tab order and reading, but not removed, so focus
    // can be handed over before it goes.
    const control = document.querySelector<HTMLElement>(".chat-show");
    expect(control?.hidden).toBe(true);
    expect(control?.querySelector("button")).toBeTruthy();
  });

  it("UPM-201 shuts the drawer on Escape and returns focus to Show chats", async () => {
    window.innerWidth = 390;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    fireEvent.click(screen.getByRole("button", { name: "Show chats" }));
    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("true");
    expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Search chats" }));

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });

    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("false");
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Show chats" }));
  });

  it("UPM-201 ignores Escape while the drawer is shut", async () => {
    window.innerWidth = 390;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    const show = screen.getByRole("button", { name: "Show chats" });
    show.focus();

    fireEvent.keyDown(document.body, { key: "Escape" });

    expect(document.activeElement).toBe(show);
    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("false");
  });

  it("UPM-201 shuts the drawer on a click outside it, and keeps the thread inert while it is open", async () => {
    window.innerWidth = 390;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    const thread = () => document.querySelector(".chat-thread");
    expect(thread()?.hasAttribute("inert")).toBe(false);
    expect(document.querySelector(".chat-backdrop")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Show chats" }));
    expect(thread()?.hasAttribute("inert")).toBe(true);
    const backdrop = document.querySelector(".chat-backdrop");
    expect(backdrop).toBeTruthy();

    fireEvent.click(backdrop as Element);

    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("false");
    expect(thread()?.hasAttribute("inert")).toBe(false);
    expect(document.querySelector(".chat-backdrop")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Show chats" }));
  });

  it("UPM-201 shuts the drawer after New chat", async () => {
    window.innerWidth = 390;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    fireEvent.click(screen.getByRole("button", { name: "Show chats" }));

    fireEvent.click(within(await findChatList()).getByRole("button", { name: "New chat" }));

    expect(await screen.findByRole("heading", { level: 2, name: "New chat" })).toBeTruthy();
    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("false");
    expect(document.querySelector(".chat-thread")?.hasAttribute("inert")).toBe(false);
  });

  it("UPM-201 shuts the drawer after New chat on a chat that is already blank", async () => {
    window.innerWidth = 390;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    fireEvent.click(screen.getByRole("button", { name: "Show chats" }));
    fireEvent.click(within(await findChatList()).getByRole("button", { name: "New chat" }));
    await screen.findByRole("heading", { level: 2, name: "New chat" });
    await screen.findByText("Write a message to start this chat.");
    fireEvent.click(screen.getByRole("button", { name: "Show chats" }));

    fireEvent.click(within(await findChatList()).getByRole("button", { name: "New chat" }));

    expect(document.querySelector("#chat-list")?.getAttribute("data-open")).toBe("false");
  });

  it("UPM-201 follows the screen across the phone breakpoint", async () => {
    window.innerWidth = 1280;
    renderChat();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    const nav = () => document.querySelector("#chat-list");
    expect(nav()?.getAttribute("data-open")).toBe("true");

    // Rotated or narrowed to a phone: the drawer shuts and the thread is first.
    window.innerWidth = 390;
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(nav()?.getAttribute("data-open")).toBe("false");
    expect(nav()?.hasAttribute("inert")).toBe(true);
    expect(screen.getByRole("button", { name: "Show chats" })).toBeTruthy();

    // Back to a wide screen: the list returns as a sidebar.
    window.innerWidth = 1280;
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(nav()?.getAttribute("data-open")).toBe("true");
    expect(nav()?.hasAttribute("inert")).toBe(false);
    expect(screen.queryByRole("button", { name: "Show chats" })).toBeNull();
  });

  it("UPM-201 hands focus to Show chats when a resize shuts the list around it", async () => {
    window.innerWidth = 1280;
    renderChat();
    const list = await findChatList();
    await screen.findByRole("heading", { level: 2, name: "Roadmap priorities" });
    within(list).getByRole("textbox", { name: "Search chats" }).focus();

    window.innerWidth = 390;
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });

    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Show chats" }));
  });
});
