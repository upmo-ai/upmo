import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createBrainApi } from "../../app/brain-api";
import { AppProviders } from "../../theme";

const closeId = "01990000-0000-7000-8000-0000000000c1";
const pricesId = "01990000-0000-7000-8000-0000000000c2";
const emptyId = "01990000-0000-7000-8000-0000000000c3";

type StoredLine = {
  id: string;
  role: "user" | "assistant";
  body: string;
  model: string | null;
  tokens: number;
  cost_usd: number | null;
  at: string;
};
type StoredChat = { id: string; createdAt: string; lines: StoredLine[] };
type Call = { method: string; path: string; body: unknown };

function minutesAgo(minutes: number) {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

function line(id: string, role: StoredLine["role"], body: string, extra: Partial<StoredLine> = {}): StoredLine {
  return {
    id,
    role,
    body,
    model: role === "assistant" ? "claude-opus-5-5" : null,
    tokens: 0,
    cost_usd: null,
    at: minutesAgo(10),
    ...extra,
  };
}

/**
 * A brain that keeps Ada's chats in memory and answers the chat routes the way
 * the real ones do: the list names a chat by its first user line and previews
 * its newest line, a send stores the user line and answers 202 with no body,
 * and no assistant line is ever written.
 */
function brain(options: { readOnlyChat?: string; tasks?: () => Response } = {}) {
  const chats: StoredChat[] = [
    {
      id: closeId,
      createdAt: minutesAgo(12),
      lines: [
        line("l101", "user", "What should the October close check first?", { at: minutesAgo(12) }),
        line("l102", "assistant", "Start with the supplier statements, then reconcile the ledger.", {
          model: "claude-opus-5-5",
          tokens: 412,
          cost_usd: 0.0031,
          at: minutesAgo(11),
        }),
      ],
    },
    {
      id: pricesId,
      createdAt: minutesAgo(18),
      lines: [
        line("l201", "user", "Summarize the supplier prices", { at: minutesAgo(18) }),
        line("l202", "assistant", "Prices are stable to the year end; lead times are the open question.", {
          model: "llama3.3:70b",
          tokens: 58,
          cost_usd: null,
          at: minutesAgo(17),
        }),
      ],
    },
    { id: emptyId, createdAt: minutesAgo(120), lines: [] },
  ];
  const calls: Call[] = [];
  let serial = 100;
  let nowMs = Date.parse(minutesAgo(5));

  // The titles and previews the brain derives, the way its own list does.
  const summary = (chat: StoredChat) => {
    const firstUser = chat.lines.find((item) => item.role === "user")?.body ?? "";
    const newest = chat.lines.at(-1);
    return {
      id: chat.id,
      title: firstUser.trim() === "" ? "New chat" : firstUser,
      preview: newest?.body ?? "",
      last_activity: newest?.at ?? chat.createdAt,
    };
  };
  const sorted = () => [...chats].sort((a, b) => Date.parse(summary(b).last_activity) - Date.parse(summary(a).last_activity));
  const find = (id: string) => chats.find((chat) => chat.id === id);

  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    expect(init?.credentials).toBe("include");

    // The page and the bell read these too; the brain has no approvals or notices here.
    if (method === "GET" && (path === "/api/approvals" || path.startsWith("/api/notifications"))) {
      return Response.json([]);
    }
    if (path === "/api/tasks" && method === "GET") return options.tasks?.() ?? Response.json([]);

    if (path === "/api/chats" && method === "GET") return Response.json(sorted().map(summary));
    if (path === "/api/chats" && method === "POST") {
      serial += 1;
      const chat: StoredChat = { id: `chat-${serial}`, createdAt: new Date(nowMs).toISOString(), lines: [] };
      chats.push(chat);
      return Response.json(summary(chat), { status: 201 });
    }
    const match = /^\/api\/chats\/([^/]+)$/.exec(path);
    const chat = match ? find(decodeURIComponent(match[1] ?? "")) : undefined;
    if (match && !chat) return Response.json({ error: "no such chat" }, { status: 404 });
    if (chat && method === "GET") return Response.json({ id: chat.id, lines: chat.lines });
    if (chat && method === "POST") {
      if (options.readOnlyChat === chat.id) {
        return Response.json({ error: "needs a chat you can edit" }, { status: 403 });
      }
      const text = (body as { body?: string }).body;
      if (typeof text !== "string" || text.trim() === "") {
        return Response.json({ error: "a chat needs a line" }, { status: 400 });
      }
      nowMs += 60_000;
      chat.lines.push({
        id: `line-${serial}`,
        role: "user",
        body: text,
        model: null,
        tokens: 0,
        cost_usd: null,
        at: new Date(nowMs).toISOString(),
      });
      // Accepted: the line is stored, and the answer carries no body to read.
      return new Response(null, { status: 202 });
    }
    return new Response(null, { status: 404 });
  });
  return { fetcher, calls, chats };
}

function renderChatAt(path: string, client = createBrainApi()) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
  return history;
}

async function chatList() {
  return screen.findByRole("navigation", { name: "Chat list" });
}

function messageLog() {
  return screen.getByRole("log", { name: "Messages" });
}

async function type(text: string) {
  const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;
  fireEvent.change(box, { target: { value: text } });
  return box;
}

describe("chats from the brain", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists the brain's chats newest first, with their titles and previews", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderChatAt("/chat");

    const list = await chatList();
    const first = await within(list).findByRole("button", { name: /What should the October close check first\?/ });
    const second = within(list).getByRole("button", { name: /Summarize the supplier prices/ });
    // The head's New chat button also begins with those words, so the item is the second match.
    const [, third] = within(list).getAllByRole("button", { name: /^New chat/ });
    expect(first.compareDocumentPosition(second)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    expect(second.compareDocumentPosition(third)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    // The preview rides along as the item's tooltip; an empty chat has none.
    expect(first.getAttribute("title")).toBe("Start with the supplier statements, then reconcile the ledger.");
    expect(second.getAttribute("title")).toBe("Prices are stable to the year end; lead times are the open question.");
    expect(third.getAttribute("title")).toBeNull();
    expect(screen.queryByText("Roadmap priorities")).toBeNull();
  });

  it("opens the newest chat and shows each line's role, model, tokens and cost", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderChatAt("/chat");

    const log = await screen.findByRole("log", { name: "Messages" });
    await within(log).findByText("What should the October close check first?");
    expect(within(log).getByText("Start with the supplier statements, then reconcile the ledger.")).toBeTruthy();
    expect(log.querySelectorAll(".upmo-chat-msg--user")).toHaveLength(1);
    expect(log.querySelectorAll(".upmo-chat-msg--assistant")).toHaveLength(1);
    expect(within(log).getByText("claude-opus-5-5 · 412 tokens · US$0.0031")).toBeTruthy();
    // A stored user line carries no cost: unknown stays unknown, never zero.
    expect(within(log).getByText("0 tokens · Unknown")).toBeTruthy();

    const list = await chatList();
    fireEvent.click(within(list).getByRole("button", { name: /Summarize the supplier prices/ }));
    await within(log).findByText(/Prices are stable to the year end/);
    expect(within(log).getByText("llama3.3:70b · 58 tokens · Unknown")).toBeTruthy();
    expect(log.textContent).not.toMatch(/\$0\.00(?!\d)/);
  });

  it("opens an empty chat as a blank page", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderChatAt("/chat");

    const list = await chatList();
    await within(list).findByRole("button", { name: /What should the October close check first\?/ });
    const [, empty] = within(list).getAllByRole("button", { name: /^New chat/ });
    fireEvent.click(empty);

    expect(await screen.findByRole("heading", { level: 2, name: "New chat" })).toBeTruthy();
    expect(await screen.findByText("Write a message to start this chat.")).toBeTruthy();
  });

  it("creates a chat with a POST and opens it empty", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChatAt("/chat");
    const list = await chatList();
    await within(list).findByRole("button", { name: /What should the October close check first\?/ });

    fireEvent.click(within(list).getByRole("button", { name: "New chat" }));

    expect(await screen.findByRole("heading", { level: 2, name: "New chat" })).toBeTruthy();
    expect(await screen.findByText("Write a message to start this chat.")).toBeTruthy();
    expect(calls).toContainEqual({ method: "POST", path: "/api/chats", body: undefined });
    await waitFor(() => {
      expect(calls).toContainEqual({ method: "GET", path: "/api/chats/chat-101", body: undefined });
    });
  });

  it("sends the body to the brain, shows the stored line and waits for nothing", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChatAt("/chat");
    const list = await chatList();
    await within(list).findByRole("button", { name: /What should the October close check first\?/ });
    fireEvent.click(within(list).getByRole("button", { name: "New chat" }));
    await screen.findByText("Write a message to start this chat.");

    const box = await type("Plan the offsite");
    fireEvent.keyDown(box, { key: "Enter" });

    expect(await within(messageLog()).findByText("Plan the offsite")).toBeTruthy();
    expect(calls).toContainEqual({ method: "POST", path: "/api/chats/chat-101", body: { body: "Plan the offsite" } });
    expect(box.value).toBe("");
    // No assistant is coming: no thinking line, no busy log, and the list names the chat by its line.
    expect(within(messageLog()).queryByText("Thinking")).toBeNull();
    await waitFor(() => {
      expect(messageLog().getAttribute("aria-busy")).toBe("false");
    });
    expect(await screen.findByRole("heading", { level: 2, name: "Plan the offsite" })).toBeTruthy();

    // Nothing is awaited, so the next line goes straight away.
    fireEvent.change(box, { target: { value: "And book the venue" } });
    fireEvent.keyDown(box, { key: "Enter" });
    expect(await within(messageLog()).findByText("And book the venue")).toBeTruthy();
  });

  it("puts a 403 on send as a sentence and keeps the words", async () => {
    vi.stubGlobal("fetch", brain({ readOnlyChat: pricesId }).fetcher);
    renderChatAt("/chat");
    const list = await chatList();
    fireEvent.click(await within(list).findByRole("button", { name: /Summarize the supplier prices/ }));
    const box = await type("One more question");
    await within(messageLog()).findByText(/Prices are stable to the year end/);

    fireEvent.keyDown(box, { key: "Enter" });

    expect(await screen.findByText("You can read this chat but not write in it.")).toBeTruthy();
    expect(box.value).toBe("One more question");
    expect(within(messageLog()).queryByText("One more question")).toBeNull();
  });

  it("does not hide a 401 behind the sample chats", async () => {
    vi.stubGlobal(
      "fetch",
      brain({ tasks: () => Response.json({ error: "unauthorized" }, { status: 401 }) }).fetcher,
    );
    const history = renderChatAt("/chat");

    await waitFor(() => {
      expect(history.location.pathname).toBe("/login");
    });
    expect(screen.queryByText("Roadmap priorities")).toBeNull();
  });

  it("uses the sample chats, wait included, when there is no brain", async () => {
    const { fetcher, calls } = brain({ tasks: () => new Response(null, { status: 404 }) });
    vi.stubGlobal("fetch", fetcher);
    renderChatAt("/chat");
    const list = await chatList();
    await within(list).findByRole("button", { name: /Roadmap priorities/ });
    expect(calls.filter((call) => call.path.startsWith("/api/chats"))).toEqual([]);

    const box = await type("Hello from the stub");
    fireEvent.keyDown(box, { key: "Enter" });

    // The stub still owes the reply, so the wait shows and ends when it lands.
    expect(await within(messageLog()).findByText("Thinking")).toBeTruthy();
    expect(
      await within(messageLog()).findByText(/A stubbed reply to: "Hello from the stub"/, {}, { timeout: 4000 }),
    ).toBeTruthy();
    expect(within(messageLog()).queryByText("Thinking")).toBeNull();
  });
});
