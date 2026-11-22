import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createBrainApi } from "../../app/brain-api";
import { AppProviders } from "../../theme";
import { sampleRoutes } from "../models/sample";
import {
  STREAM_RETRY_MS,
  STREAM_SILENCE_MS,
  attemptsText,
  keepPending,
  newestEventId,
  noticeText,
  parseDelta,
  parseLine,
  parseNotice,
  resumeAfter,
  streamUrl,
  withDelta,
  withLine,
  withNotice,
} from "./stream";
import type { Chat, ChatLine, ChatNotice } from "./types";

const askedId = "01990000-0000-7000-8000-0000000000d1";
const failedChatId = "01990000-0000-7000-8000-0000000000d2";

function stored(id: string, role: ChatLine["role"], body: string, extra: Partial<ChatLine> = {}): ChatLine {
  return {
    id,
    role,
    body,
    model: role === "assistant" ? "claude-y" : null,
    tokens: 0,
    cost_usd: null,
    at: "2026-11-09T09:00:00Z",
    ...extra,
  };
}

const user = stored("u1", "user", "Plan the offsite");
const pending: ChatLine = {
  id: "r1",
  role: "assistant",
  body: "Hel",
  model: null,
  tokens: 0,
  cost_usd: null,
  at: "",
  status: "pending",
  seq: 1,
};
function storedReply(id: string) {
  return stored(id, "assistant", "Hello", { status: "done", reply_to: "u1" });
}
const notice: ChatNotice = { line_id: "r1", failed_model: "gpt-x", reason: "rate limited", next_model: "claude-y" };

describe("chat stream rules", () => {
  it("parses a delta, a notice and a line, and refuses bad data", () => {
    expect(parseDelta('{"line_id":"r1","seq":2,"body":"lo"}')).toEqual({ lineId: "r1", seq: 2, body: "lo" });
    expect(parseDelta('{"line_id":"r1","seq":0,"body":"lo"}')).toBeNull();
    expect(parseDelta('{"line_id":"","seq":1,"body":"lo"}')).toBeNull();
    expect(parseDelta("[]")).toBeNull();
    expect(parseDelta("nope")).toBeNull();
    expect(parseNotice(JSON.stringify(notice))).toEqual(notice);
    expect(parseNotice('{"line_id":"r1","failed_model":"a"}')).toBeNull();
    expect(parseLine(JSON.stringify(user))).toEqual(user);
    expect(parseLine('{"id":"x"}')).toBeNull();
    expect(parseLine("nope")).toBeNull();
  });

  it("grows the pending reply by each delta, starting it with the first", () => {
    const chat: Chat = { id: "c", lines: [user] };
    const first = withDelta(chat, { lineId: "r1", seq: 1, body: "Hel" });
    expect(first?.lines.at(-1)).toMatchObject({ id: "r1", role: "assistant", body: "Hel", status: "pending", seq: 1 });
    const second = withDelta(first, { lineId: "r1", seq: 2, body: "lo" });
    expect(second?.lines.at(-1)?.body).toBe("Hello");
    expect(second?.lines).toHaveLength(2);
    expect(withDelta(undefined, { lineId: "r1", seq: 1, body: "x" })).toBeUndefined();
  });

  it("ignores a replayed delta and one for a reply that is already stored", () => {
    const chat: Chat = { id: "c", lines: [user, pending] };
    expect(withDelta(chat, { lineId: "r1", seq: 1, body: "Hel" })).toBe(chat);
    const done: Chat = { id: "c", lines: [user, stored("r1", "assistant", "Hello", { status: "done" })] };
    expect(withDelta(done, { lineId: "r1", seq: 5, body: "!" })).toBe(done);
  });

  it("replaces the pending reply with the stored line, in place and once", () => {
    const chat: Chat = { id: "c", lines: [user, pending], notices: [notice] };
    const final = stored("r1", "assistant", "Hello there", { status: "done", reply_to: "u1" });
    const next = withLine(chat, final);
    expect(next?.lines).toEqual([user, final]);
    expect(next?.notices).toEqual([]);
    expect(withLine(next, final)).toBe(next);
  });

  it("adds a user line once, and a reply takes the place of a failed one to the same line", () => {
    const base: Chat = { id: "c", lines: [] };
    const added = withLine(base, user);
    expect(added?.lines).toEqual([user]);
    expect(withLine(added, user)).toBe(added);

    const failed = stored("f1", "assistant", "No model could answer.", { status: "failed", reply_to: "u1" });
    const retried = withLine({ id: "c", lines: [user, failed] }, stored("f2", "assistant", "Still no.", { status: "failed", reply_to: "u1" }));
    expect(retried?.lines.map((line) => line.id)).toEqual(["u1", "f2"]);
    const answered = withLine(retried, stored("r2", "assistant", "Here.", { status: "done", reply_to: "u1" }));
    expect(answered?.lines.map((line) => line.id)).toEqual(["u1", "r2"]);
    expect(withLine(undefined, user)).toBeUndefined();
  });

  it("adds a switch notice once", () => {
    const chat: Chat = { id: "c", lines: [user] };
    const once = withNotice(chat, notice);
    expect(once?.notices).toEqual([notice]);
    expect(withNotice(once, notice)).toBe(once);
    expect(withNotice(once, { ...notice, failed_model: "claude-y", next_model: "llama" })?.notices).toHaveLength(2);
    expect(withNotice(undefined, notice)).toBeUndefined();
  });

  it("keeps the newest event id and resumes only a reply the page holds half of", () => {
    expect(newestEventId("a", "b")).toBe("b");
    expect(newestEventId("a", "")).toBe("a");
    expect(newestEventId(undefined, "")).toBeUndefined();
    expect(resumeAfter({ id: "c", lines: [user, pending] }, "e9")).toBe("e9");
    expect(resumeAfter({ id: "c", lines: [user] }, "e9")).toBeUndefined();
    expect(resumeAfter(undefined, "e9")).toBeUndefined();
    expect(streamUrl("c", undefined)).toBe("/api/chats/c/stream");
    expect(streamUrl("a b", "x/y")).toBe("/api/chats/a%20b/stream?after=x%2Fy");
  });

  it("keeps a reply the page holds half of across a read, unless the read has it", () => {
    const held: Chat = { id: "c", lines: [user, pending] };
    expect(keepPending({ id: "c", lines: [user] }, held).lines).toEqual([user, pending]);
    const final = storedReply("r1");
    expect(keepPending({ id: "c", lines: [user, final] }, held).lines).toEqual([user, final]);
    const fresh: Chat = { id: "c", lines: [user] };
    expect(keepPending(fresh, undefined)).toBe(fresh);
    expect(keepPending(fresh, { id: "c", lines: [user] })).toBe(fresh);
  });

  it("says in plain words which model did not answer and which is tried next", () => {
    expect(noticeText(notice)).toBe("gpt-x didn't answer (rate limited), trying claude-y");
    expect(noticeText({ ...notice, next_model: "" })).toBe("gpt-x didn't answer (rate limited)");
    expect(
      attemptsText([
        { model: "gpt-x", reason: "rate limited" },
        { model: "claude-y", reason: "timed out" },
      ]),
    ).toBe("Tried gpt-x (rate limited), then claude-y (timed out).");
    expect(attemptsText([])).toBe("");
    expect(attemptsText(undefined)).toBe("");
  });
});

type Call = { method: string; path: string; body: unknown };

/**
 * A brain that keeps two chats in memory. The first has only the person's line,
 * its reply is written on the stream. The second ends in a failed reply.
 */
function brain(
  options: { pendingMarker?: boolean; routesRefused?: boolean; listsRefused?: boolean; listsFail?: number; agents?: unknown[]; accounts?: unknown[] } = {},
) {
  const failedAt = Date.now() - 60_000;
  const chats = [
    { id: askedId, at: Date.now() - 10 * 60_000, lines: [stored("u1", "user", "Plan the offsite", options.pendingMarker ? { pending: true } : {})] },
    {
      id: failedChatId,
      at: failedAt,
      lines: [
        stored("u2", "user", "Summarize the prices"),
        stored("f1", "assistant", "No model could answer.", {
          status: "failed",
          reply_to: "u2",
          model: null,
          attempts: [
            { model: "gpt-x", reason: "rate limited" },
            { model: "claude-y", reason: "timed out" },
          ],
        }),
      ],
    },
  ];
  const calls: Call[] = [];
  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    if (method === "GET" && (path === "/api/approvals" || path.startsWith("/api/notifications"))) {
      return Response.json([]);
    }
    if (path === "/api/tasks" && method === "GET") return Response.json([]);
    if (options.listsFail && method === "GET" && ["/api/gateway/routes", "/api/agents", "/api/providers"].includes(path)) {
      return Response.json({ error: "down" }, { status: options.listsFail });
    }
    if (path === "/api/gateway/routes" && method === "GET") {
      if (options.routesRefused) return Response.json({ error: "unauthorized" }, { status: 401 });
      return Response.json(sampleRoutes.map(({ scope: _scope, ...route }) => route));
    }
    if ((path === "/api/agents" || path === "/api/providers") && method === "GET") {
      if (options.listsRefused) return Response.json({ error: "unauthorized" }, { status: 401 });
      return Response.json(path === "/api/agents" ? (options.agents ?? []) : (options.accounts ?? []));
    }
    if (path === "/api/chats" && method === "GET") {
      return Response.json(
        chats.map((chat) => ({
          id: chat.id,
          title: chat.lines[0]?.body ?? "New chat",
          preview: chat.lines.at(-1)?.body ?? "",
          last_activity: new Date(chat.at).toISOString(),
        })),
      );
    }
    const match = /^\/api\/chats\/([^/]+)$/.exec(path);
    const chat = match ? chats.find((item) => item.id === match[1]) : undefined;
    if (chat && method === "GET") return Response.json({ id: chat.id, lines: chat.lines });
    if (chat && method === "POST") return new Response(null, { status: 202 });
    if (/^\/api\/chats\/[^/]+\/lines\/[^/]+\/retry$/.test(path) && method === "POST") {
      return new Response(null, { status: 202 });
    }
    return new Response(null, { status: 404 });
  });
  return { fetcher, calls };
}

/** The EventSource the chat stream opens, with the events a test sends. */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly url: string;
  withCredentials: boolean;
  readyState = FakeEventSource.CONNECTING;
  closed = 0;
  private listeners = new Map<string, Array<(event: MessageEvent) => void>>();

  constructor(url: string, init?: { withCredentials?: boolean }) {
    this.url = url;
    this.withCredentials = init?.withCredentials ?? false;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: (event: MessageEvent) => void) {
    const current = this.listeners.get(type) ?? [];
    current.push(listener);
    this.listeners.set(type, current);
  }

  close() {
    this.closed += 1;
    this.readyState = FakeEventSource.CLOSED;
  }

  emit(type: string, data: unknown, id = "") {
    if (type === "open") this.readyState = FakeEventSource.OPEN;
    const text = typeof data === "string" ? data : JSON.stringify(data);
    for (const listener of this.listeners.get(type) ?? []) {
      listener(new MessageEvent(type, { data: text, lastEventId: id }));
    }
  }
}

function renderChat() {
  const history = createMemoryHistory({ initialEntries: ["/chat"] });
  render(
    <AppProviders>
      <App history={history} client={createBrainApi()} />
    </AppProviders>,
  );
}

function log() {
  return screen.getByRole("log", { name: "Messages" });
}

const threadReads = (calls: Call[], id: string) =>
  calls.filter((call) => call.method === "GET" && call.path === `/api/chats/${id}`).length;

// Real time: a few polling intervals (300 ms each) go by.
const pause = (ms: number) => act(async () => new Promise<void>((resolve) => setTimeout(resolve, ms)));

// The stream opens once the thread has loaded.
async function openStream() {
  await within(await screen.findByRole("log", { name: "Messages" })).findByText(/Plan the offsite|Summarize the prices/);
  await waitFor(() => {
    expect(FakeEventSource.instances.length).toBeGreaterThan(0);
  });
  const source = FakeEventSource.instances[0];
  act(() => {
    source.emit("open", "");
  });
  return source;
}

describe("chat stream page", () => {
  beforeEach(() => {
    localStorage.clear();
    FakeEventSource.instances.length = 0;
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("UPM-251 shows Thinking from the pending marker right after a reload", async () => {
    const { fetcher } = brain({ pendingMarker: true });
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    expect(await within(log()).findByText("Thinking")).toBeTruthy();
    expect(log().getAttribute("aria-busy")).toBe("true");
  });

  it("UPM-251 shows no Thinking for a stored user line without the marker", async () => {
    const { fetcher } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    expect(await within(log()).findByText("Plan the offsite")).toBeTruthy();
    expect(within(log()).queryByText("Thinking")).toBeNull();
  });

  it("grows the reply as deltas arrive, then shows the stored line in its place", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    const source = await openStream();
    expect(source.url).toBe(`/api/chats/${askedId}/stream`);
    expect(source.withCredentials).toBe(true);

    act(() => {
      source.emit("delta", { line_id: "r1", seq: 1, body: "Hel" }, "e1");
    });
    expect(await within(log()).findByText("Hel")).toBeTruthy();
    act(() => {
      source.emit("delta", { line_id: "r1", seq: 2, body: "lo there" }, "e2");
    });
    expect(await within(log()).findByText("Hello there")).toBeTruthy();
    expect(log().querySelectorAll(".upmo-chat-msg--assistant")).toHaveLength(1);
    expect(log().getAttribute("aria-busy")).toBe("true");
    // While the stream is open a pending reply is not polled for.
    await pause(200);
    const quiet = threadReads(calls, askedId);
    await pause(1_000);
    expect(threadReads(calls, askedId)).toBe(quiet);

    // A replayed piece changes nothing.
    act(() => {
      source.emit("delta", { line_id: "r1", seq: 2, body: "lo there" }, "e2");
    });
    await act(async () => {});
    expect(within(log()).getByText("Hello there")).toBeTruthy();

    act(() => {
      source.emit("line", stored("r1", "assistant", "Hello there, friend.", { status: "done", reply_to: "u1", tokens: 12 }), "e3");
    });
    expect(await within(log()).findByText("Hello there, friend.")).toBeTruthy();
    expect(within(log()).queryByText("Hello there")).toBeNull();
    expect(within(log()).getByText(/claude-y · 12 tokens/)).toBeTruthy();
    expect(log().querySelectorAll(".upmo-chat-msg--assistant")).toHaveLength(1);
    await waitFor(() => {
      expect(log().getAttribute("aria-busy")).toBe("false");
    });
    // The stream carried it: no poll of the thread beyond the first read and the open's read.
    expect(calls.filter((call) => call.path === `/api/chats/${askedId}` && call.method === "GET").length).toBeLessThanOrEqual(2);
  });

  it("says which model did not answer and which is tried next, until the reply is stored", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderChat();
    const source = await openStream();

    act(() => {
      source.emit("status", notice, "e1");
    });
    expect(await within(log()).findByText("gpt-x didn't answer (rate limited), trying claude-y")).toBeTruthy();

    act(() => {
      source.emit("line", stored("r1", "assistant", "Done.", { status: "done", reply_to: "u1" }), "e2");
    });
    await within(log()).findByText("Done.");
    expect(within(log()).queryByText(/didn't answer/)).toBeNull();
  });

  it("sends the composer's route with a line and waits for the reply on the stream", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    const source = await openStream();
    await choose("Route", "chat-stream");

    const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;
    fireEvent.change(box, { target: { value: "And the venue?" } });
    fireEvent.keyDown(box, { key: "Enter" });

    await waitFor(() => {
      expect(calls).toContainEqual({ method: "POST", path: `/api/chats/${askedId}`, body: { body: "And the venue?", route: "chat-stream" } });
    });
    // The brain stored the line; the page shows the wait, and polls nothing while the stream is open.
    act(() => {
      source.emit("line", stored("u3", "user", "And the venue?"), "e1");
    });
    expect(await within(log()).findByText("Thinking")).toBeTruthy();
    await pause(400);
    const quiet = threadReads(calls, askedId);
    await pause(1_000);
    expect(threadReads(calls, askedId)).toBe(quiet);
    expect(screen.getByRole("button", { name: /Agent, route and model/ }).getAttribute("aria-label")).toContain("chat-stream");
  });

  it("offers Auto and the gateway's chat routes, and sends the chosen one", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderChat();
    await openStream();
    const pill = screen.getByRole("button", { name: /Agent, route and model:/ });
    fireEvent.click(pill);
    fireEvent.mouseEnter(await screen.findByRole("menuitem", { name: "Route", hidden: true }));
    const names = (await screen.findAllByRole("menuitemradio", { hidden: true })).map((item) => item.getAttribute("aria-label"));
    const routeNames = names.filter((name) => ["Auto", ...sampleRoutes.map((item) => item.name)].includes(name ?? ""));
    // Chat and stream routes only: the sample's embed and decide routes cannot answer.
    expect(routeNames).toEqual(["Auto", "chat", "chat-stream"]);
    expect(names).not.toContain("Fastest");
    expect(names).not.toContain("embed");
  });

  it("offers only Auto when the brain refuses the lists, and stays signed in", async () => {
    vi.stubGlobal("fetch", brain({ routesRefused: true, listsRefused: true }).fetcher);
    renderChat();
    await openStream();
    fireEvent.click(screen.getByRole("button", { name: /Agent, route and model:/ }));
    fireEvent.mouseEnter(await screen.findByRole("menuitem", { name: "Route", hidden: true }));
    const names = (await screen.findAllByRole("menuitemradio", { hidden: true })).map((item) => item.getAttribute("aria-label"));
    expect(names).toContain("Auto");
    expect(names).not.toContain("chat");
    expect(names).not.toContain("chat-stream");
  });

  it("shows a failed reply with the models tried, and retries on the route picked now", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    const list = await screen.findByRole("navigation", { name: "Chat list" });
    fireEvent.click(await within(list).findByRole("button", { name: /Plan the offsite/ }));
    await within(log()).findByText("Plan the offsite");
    fireEvent.click(within(list).getByRole("button", { name: /Summarize the prices/ }));

    expect(await within(log()).findByText("No model could answer.")).toBeTruthy();
    expect(within(log()).getByText("Tried gpt-x (rate limited), then claude-y (timed out).")).toBeTruthy();
    // A reason in plain words is all it shows.
    expect(log().textContent).not.toMatch(/error|exception|status \d{3}/i);

    await choose("Route", "chat");
    fireEvent.click(within(log()).getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(calls).toContainEqual({
        method: "POST",
        path: `/api/chats/${failedChatId}/lines/f1/retry`,
        body: { route: "chat" },
      });
    });
    // The failed reply gives way to the wait.
    expect(await within(log()).findByText("Thinking")).toBeTruthy();
    expect(within(log()).queryByText("No model could answer.")).toBeNull();

    // The new reply arrives on the stream and the failure is gone.
    const source = FakeEventSource.instances.at(-1);
    expect(source?.url).toBe(`/api/chats/${failedChatId}/stream`);
    act(() => {
      source?.emit("open", "");
      source?.emit("line", stored("r2", "assistant", "Here are the prices.", { status: "done", reply_to: "u2" }), "e5");
    });
    expect(await within(log()).findByText("Here are the prices.")).toBeTruthy();
    expect(within(log()).queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("shows a reply under way when the page is opened again, from the replayed deltas", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderChat();
    const source = await openStream();

    // The stream starts cursorless and replays the reply from its first piece.
    act(() => {
      source.emit("line", user, "e0");
      source.emit("delta", { line_id: "r1", seq: 1, body: "Half" }, "e1");
      source.emit("delta", { line_id: "r1", seq: 2, body: " way" }, "e2");
    });
    expect(await within(log()).findByText("Half way")).toBeTruthy();
    expect(within(log()).getAllByText("Plan the offsite")).toHaveLength(1);
  });

  it("polls for the reply after 30 s of silence and resumes after the newest event heard", async () => {
    vi.useFakeTimers();
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    for (let round = 0; round < 5; round += 1) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
    }
    const source = FakeEventSource.instances[0];
    expect(source).toBeTruthy();
    act(() => {
      source.emit("open", "");
      source.emit("delta", { line_id: "r1", seq: 1, body: "Half" }, "e1");
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const reads = () => calls.filter((call) => call.method === "GET" && call.path === `/api/chats/${askedId}`).length;
    const streaming = reads();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(reads()).toBe(streaming);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(STREAM_SILENCE_MS + 1_000);
    });
    expect(source.closed).toBe(1);
    // The pending reply is still awaited, so the thread is read every poll.
    const polling = reads();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(reads()).toBeGreaterThan(polling);
    // The half reply stays on screen while it is polled for.
    expect(within(log()).getByText("Half")).toBeTruthy();

    // A minute of polling in all: the stream is tried again, after the newest event heard.
    expect(FakeEventSource.instances).toHaveLength(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(STREAM_RETRY_MS);
    });
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.instances[1].url).toBe(`/api/chats/${askedId}/stream?after=e1`);
  });

  it("polls a chat whose stream has not opened yet, though the chat before it was streaming", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    // The first chat's stream is open.
    await openStream();
    const list = await screen.findByRole("navigation", { name: "Chat list" });
    fireEvent.click(within(list).getByRole("button", { name: /Summarize the prices/ }));
    await within(log()).findByText("No model could answer.");
    await waitFor(() => {
      expect(FakeEventSource.instances.length).toBeGreaterThan(1);
    });
    // The second chat's stream never opens; a retry there is awaited by polling.
    fireEvent.click(within(log()).getByRole("button", { name: "Retry" }));
    expect(await within(log()).findByText("Thinking")).toBeTruthy();

    await pause(400);
    const before = threadReads(calls, failedChatId);
    await waitFor(() => {
      expect(threadReads(calls, failedChatId)).toBeGreaterThanOrEqual(before + 3);
    });
  });
});

// A model is picked inside its provider's section, so one name under two providers is not ambiguous.
async function choose(group: string, option: string, provider?: string) {
  const pill = screen.getByRole("button", { name: /Agent, route and model:/ });
  if (pill.getAttribute("aria-expanded") !== "true") fireEvent.click(pill);
  const groupItem = await screen.findByRole("menuitem", { name: group, hidden: true });
  fireEvent.mouseEnter(groupItem);
  const scope = provider ? within(await screen.findByRole("group", { name: provider, hidden: true })) : screen;
  fireEvent.click(await scope.findByRole("menuitemradio", { name: option, hidden: true }));
}

const account = (id: string, provider: string, models: string[], visibility: "private" | "everyone" = "private") => ({
  id,
  provider,
  name: `${provider} account`,
  adapter: "openai_compat",
  plan_kind: "metered",
  status: "Connected",
  models,
  usable: true,
  usable_models: models,
  visibility,
  team_ids: [] as string[],
  team_names: [] as string[],
  can_manage: visibility === "private",
  can_use: true,
  is_owner: true,
});

const agentRow = (id: string, name: string, extra: Record<string, unknown> = {}) => ({
  id,
  name,
  description: `${name} agent`,
  prompt_overlay: "",
  route: "",
  model_provider: "",
  model_id: "",
  enabled: true,
  usable: true,
  visibility: "private" as const,
  team_ids: [] as string[],
  team_names: [] as string[],
  can_use: true,
  can_manage: true,
  ...extra,
});

const pickLists = {
  agents: [
    agentRow("a1", "Research"),
    agentRow("a2", "Writer", { route: "chat", model_provider: "anthropic", model_id: "claude-y" }),
    agentRow("a3", "Retired", { enabled: false }),
  ],
  accounts: [account("acc-own", "openai", ["gpt-x"]), account("acc-house", "anthropic", ["claude-y"], "everyone")],
};

const pillLabel = () => screen.getByRole("button", { name: /Agent, route and model:/ }).getAttribute("aria-label");

async function groupNames(group: string) {
  const pill = screen.getByRole("button", { name: /Agent, route and model:/ });
  if (pill.getAttribute("aria-expanded") !== "true") fireEvent.click(pill);
  fireEvent.mouseEnter(await screen.findByRole("menuitem", { name: group, hidden: true }));
  return (await screen.findAllByRole("menuitemradio", { hidden: true })).map((item) => item.getAttribute("aria-label"));
}

// The section headings of the open menu and the options under each, "" for options with no heading.
async function sections(group: string) {
  await groupNames(group);
  const menu = screen.getByLabelText(`Filter ${group}`).closest("[role=menu], [data-menu-dropdown]") ?? document.body;
  const out: Record<string, string[]> = { "": [] };
  for (const el of menu.querySelectorAll("[role=group], [role=menuitemradio]")) {
    if (el.getAttribute("role") === "group") {
      const name = el.getAttribute("aria-labelledby") ? document.getElementById(el.getAttribute("aria-labelledby") ?? "")?.textContent ?? "" : "";
      out[name] = [...el.querySelectorAll("[role=menuitemradio]")].map((item) => item.getAttribute("aria-label") ?? "");
    } else if (!el.closest("[role=group]")) {
      out[""].push(el.getAttribute("aria-label") ?? "");
    }
  }
  return out;
}

async function sendLine(text: string) {
  const box = (await screen.findByRole("textbox", { name: "Message" })) as HTMLTextAreaElement;
  fireEvent.change(box, { target: { value: text } });
  fireEvent.keyDown(box, { key: "Enter" });
}

const sentTo = (calls: Call[], path: string) => calls.filter((call) => call.method === "POST" && call.path === path).map((call) => call.body);

describe("chat composer picks", () => {
  beforeEach(() => {
    localStorage.clear();
    FakeEventSource.instances.length = 0;
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("sends the picked agent, route and model pin with a line", async () => {
    const { fetcher, calls } = brain(pickLists);
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    await choose("Agent", "Research");
    await choose("Route", "chat-stream");
    await choose("Model", "gpt-x");

    await sendLine("And the venue?");

    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${askedId}`)).toEqual([
        { body: "And the venue?", agent_id: "a1", route: "chat-stream", pin: { account_id: "acc-own", model: "gpt-x" } },
      ]);
    });
  });

  it("sends nothing for Auto", async () => {
    const { fetcher, calls } = brain(pickLists);
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    await choose("Model", "gpt-x");
    await choose("Model", "Auto");

    await sendLine("Just ask");

    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${askedId}`)).toEqual([{ body: "Just ask" }]);
    });
  });

  it("offers enabled agents only, and models with the provider and own accounts first", async () => {
    vi.stubGlobal("fetch", brain(pickLists).fetcher);
    renderChat();
    await openStream();

    await waitFor(async () => {
      expect(await groupNames("Agent")).toEqual(["Auto", "Research", "Writer"]);
    });
    expect(await groupNames("Model")).toEqual(["Auto", "gpt-x", "claude-y"]);
  });

  it("lists models under their provider, with Auto on top and outside any section", async () => {
    vi.stubGlobal("fetch", brain(pickLists).fetcher);
    renderChat();
    await openStream();

    await waitFor(async () => {
      expect(await sections("Model")).toEqual({ "": ["Auto"], openai: ["gpt-x"], anthropic: ["claude-y"] });
    });
  });

  it("keeps a provider's models in one section when its accounts are not listed together", async () => {
    const accounts = [
      account("acc-1", "openai", ["gpt-x"]),
      account("acc-2", "anthropic", ["claude-y"]),
      account("acc-3", "openai", ["gpt-z"], "everyone"),
    ];
    vi.stubGlobal("fetch", brain({ ...pickLists, accounts }).fetcher);
    renderChat();
    await openStream();

    await waitFor(async () => {
      expect(await sections("Model")).toEqual({ "": ["Auto"], openai: ["gpt-x", "gpt-z"], anthropic: ["claude-y"] });
    });
    expect(screen.getAllByRole("group", { name: "openai", hidden: true })).toHaveLength(1);
  });

  it("pins the model of the provider whose section it is picked in when two providers share a name", async () => {
    const accounts = [account("acc-a", "openai", ["shared"]), account("acc-b", "anthropic", ["shared"])];
    const { fetcher, calls } = brain({ ...pickLists, accounts });
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    await choose("Model", "shared", "anthropic");

    await sendLine("Which one?");

    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${askedId}`)).toEqual([{ body: "Which one?", pin: { account_id: "acc-b", model: "shared" } }]);
    });
  });

  it.each([
    ["Agent", "Research"],
    ["Route", "chat-stream"],
    ["Model", "gpt-x"],
  ])("filters %s by what is typed and keeps Auto", async (group, option) => {
    vi.stubGlobal("fetch", brain(pickLists).fetcher);
    renderChat();
    await openStream();
    await waitFor(async () => {
      expect(await groupNames(group)).toContain(option);
    });

    const filter = screen.getByLabelText(`Filter ${group}`);
    fireEvent.change(filter, { target: { value: option } });

    await waitFor(() => {
      expect(screen.queryAllByRole("menuitemradio", { hidden: true }).map((item) => item.getAttribute("aria-label"))).toEqual(["Auto", option]);
    });
  });

  it("shows an agent's route and model as the defaults on the Route and Model rows", async () => {
    const { fetcher, calls } = brain(pickLists);
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    await choose("Agent", "Writer");

    expect(pillLabel()).toBe("Agent, route and model: Writer · Auto (chat) · Auto (anthropic/claude-y)");

    await sendLine("Draft it");
    // The agent's defaults are the brain's to apply: only the agent is named.
    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${askedId}`)).toEqual([{ body: "Draft it", agent_id: "a2" }]);
    });
  });

  it("lets an explicit model and route beat the agent's", async () => {
    const { fetcher, calls } = brain(pickLists);
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    await choose("Agent", "Writer");
    await choose("Model", "gpt-x");
    await choose("Route", "chat-stream");

    expect(pillLabel()).toBe("Agent, route and model: Writer · chat-stream · gpt-x");

    await sendLine("Draft it");
    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${askedId}`)).toEqual([
        { body: "Draft it", agent_id: "a2", route: "chat-stream", pin: { account_id: "acc-own", model: "gpt-x" } },
      ]);
    });
  });

  it("retries on the picks made now, as one object", async () => {
    const { fetcher, calls } = brain(pickLists);
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    const list = await screen.findByRole("navigation", { name: "Chat list" });
    fireEvent.click(await within(list).findByRole("button", { name: /Summarize the prices/ }));
    await within(log()).findByText("No model could answer.");
    await choose("Route", "chat");
    await choose("Model", "gpt-x");
    await choose("Route", "chat-stream");
    await choose("Model", "Auto");

    fireEvent.click(within(log()).getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${failedChatId}/lines/f1/retry`)).toEqual([{ route: "chat-stream" }]);
    });
  });

  it("retries with an empty object when everything is Auto", async () => {
    const { fetcher, calls } = brain(pickLists);
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    const list = await screen.findByRole("navigation", { name: "Chat list" });
    fireEvent.click(await within(list).findByRole("button", { name: /Summarize the prices/ }));
    await within(log()).findByText("No model could answer.");

    fireEvent.click(within(log()).getByRole("button", { name: "Retry" }));

    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${failedChatId}/lines/f1/retry`)).toEqual([{}]);
    });
  });

  it("remembers the picks in this browser", async () => {
    vi.stubGlobal("fetch", brain(pickLists).fetcher);
    renderChat();
    await openStream();
    await choose("Agent", "Research");
    await choose("Model", "gpt-x");
    cleanup();
    FakeEventSource.instances.length = 0;

    renderChat();
    await openStream();

    await waitFor(() => {
      expect(pillLabel()).toBe("Agent, route and model: Research · Auto · gpt-x");
    });
  });

  it("falls back to Auto for a remembered pick that no longer exists", async () => {
    localStorage.setItem(
      "upmo.chat.picks",
      JSON.stringify({ agent: "gone", route: "gone", model: "acc-gone/gpt-x" }),
    );
    const { fetcher, calls } = brain(pickLists);
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    await groupNames("Agent");

    expect(pillLabel()).toBe("Agent, route and model: Auto · Auto · Auto");
    await sendLine("Hello");
    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${askedId}`)).toEqual([{ body: "Hello" }]);
    });
    expect(JSON.parse(localStorage.getItem("upmo.chat.picks") ?? "null")).toEqual({ agent: "auto", route: "auto", model: "auto" });
  });

  it.each([500, 401])("keeps the remembered picks in storage when the lists fail with %i", async (status) => {
    const kept = JSON.stringify({ agent: "a1", route: "chat", model: "acc-own/gpt-x" });
    localStorage.setItem("upmo.chat.picks", kept);
    const { fetcher, calls } = brain({ ...pickLists, listsFail: status });
    vi.stubGlobal("fetch", fetcher);
    renderChat();
    await openStream();
    await waitFor(() => {
      expect(calls.filter((call) => ["/api/gateway/routes", "/api/agents", "/api/providers"].includes(call.path))).toHaveLength(3);
    });
    await pause(300);

    expect(localStorage.getItem("upmo.chat.picks")).toBe(kept);
    // Nothing can be checked, so nothing is sent for them.
    await sendLine("Hello");
    await waitFor(() => {
      expect(sentTo(calls, `/api/chats/${askedId}`)).toEqual([{ body: "Hello" }]);
    });
  });

  it("renders only Auto when there are no agents, routes or models", async () => {
    vi.stubGlobal("fetch", brain({ routesRefused: true }).fetcher);
    renderChat();
    await openStream();

    expect(await groupNames("Agent")).toEqual(["Auto"]);
    expect(await groupNames("Route")).toEqual(["Auto"]);
    expect(await groupNames("Model")).toEqual(["Auto"]);
  });
});
