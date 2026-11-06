import { Text, Title, useMantineTheme } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { IconLayoutSidebar, IconSearch } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ChatMessage, ChatThread, Composer, IconButton, Input, StatusAlert } from "@upmo-ai/components";
import type { ComposerOption } from "@upmo-ai/components";

import { chatQueryKey, chatsQueryKey, useApi, useWrite, type ChatLine, type ChatSummary } from "../../app/api";
import { ShellIcon } from "../../ui/icons";
import { awaitingReply, formatCost, groupByDay } from "./format";

import "./chat.css";

// How often the open chat is read again while a reply is awaited.
const REPLY_POLL_MS = 300;

const noLines: ChatLine[] = [];
const noChats: ChatSummary[] = [];

// The pickers below are not sent to the server yet: the chat API takes only
// the line. They stay as the page's own choices until it takes more.
const AGENTS: ComposerOption[] = [
  { value: "auto", label: "Auto", description: "Intelligently select the best agent" },
  { value: "general", label: "General Assistant", description: "General tasks and questions" },
  { value: "coder", label: "Code Specialist", description: "Code generation and refactoring" },
  { value: "research", label: "Research Agent", description: "Web search and summarization" },
  { value: "planner", label: "Planning Agent", description: "Multi-step plan decomposition" },
];

const ROUTES: ComposerOption[] = [
  { value: "default", label: "Default", description: "Optimized speed and quality" },
  { value: "fastest", label: "Fastest", description: "Lowest latency routing" },
  { value: "cost", label: "Cost Optimized", description: "Most economical model routing" },
  { value: "direct", label: "Direct Provider", description: "Direct upstream connection" },
];

const MODELS: ComposerOption[] = [
  { value: "gpt-5.6-luna", label: "Gpt-5.6-Luna", description: "OpenAI" },
  { value: "claude-3.7-sonnet", label: "Claude 3.7 Sonnet", description: "Anthropic" },
  { value: "claude-3.5-haiku", label: "Claude 3.5 Haiku", description: "Anthropic" },
  { value: "gpt-4o", label: "GPT-4o", description: "OpenAI" },
  { value: "gemini-2.0-flash", label: "Gemini 2.0 Flash", description: "Google" },
  { value: "deepseek-r1", label: "DeepSeek R1", description: "DeepSeek" },
];

const TOOLS_OPTIONS: ComposerOption[] = [
  { value: "auto", label: "Auto", description: "Allow model to call connected tools" },
  { value: "all", label: "All Tools", description: "Connectors and MCPs enabled" },
  { value: "search", label: "Search Only", description: "Only web search allowed" },
  { value: "disabled", label: "Disabled", description: "Pure model generation" },
];

export function ChatPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const page = useRef<HTMLElement>(null);
  // The chat list is a drawer below the md breakpoint (62em, as in chat.css).
  const theme = useMantineTheme();
  const wide = useMediaQuery(`(min-width: ${theme.breakpoints.md})`, true, {
    getInitialValueInEffect: false,
  });
  const phone = !wide;
  // The drawer starts shut on a phone, so the thread is the first thing seen.
  const [sidebarOpen, setSidebarOpen] = useState(() => !phone);
  // The drawer follows the screen across the breakpoint: shut at phone width,
  // open again on a wide one.
  const [wasPhone, setWasPhone] = useState(phone);
  if (wasPhone !== phone) {
    setWasPhone(phone);
    setSidebarOpen(!phone);
  }
  // Where focus goes once the list has opened or shut.
  const focusAfter = useRef<"list" | "toggle">(undefined);
  const [selectedId, setSelectedId] = useState<string>();
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [selectedAgent, setSelectedAgent] = useState("auto");
  const [selectedRoute, setSelectedRoute] = useState("default");
  const [selectedModel, setSelectedModel] = useState("gpt-5.6-luna");
  const [selectedTools, setSelectedTools] = useState("auto");
  const [sendError, setSendError] = useState<string>();
  // Lines sent in each chat, so that chat's thread returns to its newest
  // message and no other thread is moved.
  const [sentCounts, setSentCounts] = useState<Record<string, number>>({});
  // The finished reply, for the thread to read out. It is reset when the next
  // draft starts, so an identical reply is read again.
  const [announcement, setAnnouncement] = useState<string>();
  // The chats a reply is awaited in, each with the newest line it had when the
  // line was sent. Only a reply that comes after that line is read out, in the
  // chat it belongs to.
  const awaited = useRef(new Map<string, string | undefined>());

  const collapse = useCallback(() => {
    focusAfter.current = "toggle";
    setSidebarOpen(false);
  }, []);
  // Picking a chat or starting one leaves the drawer on a phone; the wide
  // sidebar stays where the reader put it.
  const dismissDrawer = () => {
    if (phone) collapse();
  };
  const reveal = () => {
    focusAfter.current = "list";
    setSidebarOpen(true);
  };

  // The control that opened the list is mounted all the time, only hidden while
  // the list is open, so focus can be moved off a control before it goes. Shutting
  // the list hands focus to that control, opening it hands focus to the search
  // box. A list shut by a resize does the same if focus was inside it.
  useLayoutEffect(() => {
    const target = focusAfter.current;
    focusAfter.current = undefined;
    const root = page.current;
    if (!root) return;
    if (sidebarOpen) {
      if (target === "list") {
        root.querySelector<HTMLElement>(".chat-search input")?.focus({ preventScroll: true });
      }
      return;
    }
    const inList = root.querySelector("#chat-list")?.contains(document.activeElement) ?? false;
    if (target === "toggle" || inList) {
      root.querySelector<HTMLElement>(".chat-show button")?.focus({ preventScroll: true });
    }
  }, [sidebarOpen]);

  // Escape shuts the drawer.
  useEffect(() => {
    if (!phone || !sidebarOpen) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      collapse();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [phone, sidebarOpen, collapse]);

  // Grouped as the data is read, so the days follow the clock whenever the list does.
  const chats = useQuery({
    queryKey: chatsQueryKey(),
    queryFn: () => api.chats.list(),
    select: (items) => ({ items, days: groupByDay(items, new Date()) }),
  });
  const list = chats.data?.items ?? noChats;
  // The open chat is pinned by id: the list changes order as chats are used,
  // and the thread must not change with it. The first chat loaded is pinned
  // until another is picked or created.
  const pinned = list.find((chat) => chat.id === selectedId);
  const activeChat = pinned ?? list[0];
  const activeId = activeChat?.id;
  if (!pinned && activeChat) setSelectedId(activeChat.id);

  const thread = useQuery({
    queryKey: chatQueryKey(activeId ?? ""),
    queryFn: () => api.chats.get(activeId ?? ""),
    enabled: activeId !== undefined,
    refetchInterval: (current) => (awaitingReply(current.state.data?.lines) ? REPLY_POLL_MS : false),
  });
  const lines = thread.data?.lines ?? noLines;
  const thinking = awaitingReply(lines);

  const send = useWrite(
    (input: { id: string; body: string }) => api.chats.send(input.id, input.body),
    [chatsQueryKey(), chatQueryKey(activeId ?? "")],
    {
      onSuccess: (_, input) => {
        setSentCounts((counts) => ({ ...counts, [input.id]: (counts[input.id] ?? 0) + 1 }));
      },
      onError: (error, input) => {
        awaited.current.delete(input.id);
        setSendError(error.message);
        // Give the words back unless something new is already being typed.
        setDraft((current) => (current === "" ? input.body : current));
      },
    },
  );

  const create = useWrite(() => api.chats.create(), [chatsQueryKey()], {
    onSuccess: (created) => {
      setSelectedId(created.id);
      dismissDrawer();
    },
  });

  // The reply has landed in the chat it was awaited in: read it out, and bring
  // the list's preview and time up to date.
  useEffect(() => {
    const last = lines.at(-1);
    if (activeId === undefined || !awaited.current.has(activeId)) return;
    if (last?.role !== "assistant" || last.id === awaited.current.get(activeId)) return;
    awaited.current.delete(activeId);
    setAnnouncement(last.body);
    void queryClient.invalidateQueries({ queryKey: chatsQueryKey() });
  }, [lines, activeId, queryClient]);

  const needle = query.trim().toLocaleLowerCase("en-GB");
  const days = (chats.data?.days ?? [])
    .map((group) => ({
      ...group,
      chats: group.chats.filter((chat) => chat.title.toLocaleLowerCase("en-GB").includes(needle)),
    }))
    .filter((group) => group.chats.length > 0);

  const handleSendMessage = () => {
    const text = draft.trim();
    if (!text || !activeId || thinking || send.isPending) return;
    setSendError(undefined);
    setDraft("");
    awaited.current.set(activeId, lines.at(-1)?.id);
    send.mutate({ id: activeId, body: text });
  };

  const handleDraftChange = (value: string) => {
    setDraft(value);
    setAnnouncement(undefined);
  };

  const handleNewChat = () => {
    // An empty chat is already a blank page.
    if (thread.data && thread.data.lines.length === 0) {
      dismissDrawer();
      return;
    }
    create.mutate();
  };

  return (
    <section className="chat-page" ref={page}>
      <header className="chat-page-header">
        <Title order={1} className="chat-sr-title">Chat</Title>
      </header>

      <div className="chat-workspace" data-collapsed={!sidebarOpen}>
        {phone && sidebarOpen ? (
          <div className="chat-backdrop" aria-hidden="true" onClick={collapse} />
        ) : null}
        <nav
          id="chat-list"
          className="chat-list"
          aria-label="Chat list"
          data-open={sidebarOpen}
          inert={!sidebarOpen}
        >
          <div className="chat-list-head">
            <Input
              size="xs"
              className="chat-search"
              aria-label="Search chats"
              placeholder="Search chats"
              leftSection={<IconSearch size={14} stroke={1.75} aria-hidden />}
              value={query}
              onChange={(event) => setQuery(event.currentTarget.value)}
            />
            <IconButton
              aria-label="New chat"
              variant="filled"
              size={28}
              className="chat-head-btn"
              onClick={handleNewChat}
            >
              <ShellIcon concept="action.add" size={16} />
            </IconButton>
            <IconButton
              aria-label="Collapse sidebar"
              variant="subtle"
              size={28}
              className="chat-head-btn chat-sidebar-collapse-btn"
              onClick={collapse}
            >
              <IconLayoutSidebar size={16} stroke={1.75} aria-hidden="true" />
            </IconButton>
          </div>

          <div className="chat-list-scroll">
            {chats.isError ? (
              <StatusAlert tone="critical">Chats could not be loaded.</StatusAlert>
            ) : null}
            {chats.isSuccess && days.length === 0 ? (
              <Text className="chat-list-empty">
                {needle ? "No chats match." : "No chats yet."}
              </Text>
            ) : null}
            {days.map(({ day, chats: dayChats }) => (
              <section className="chat-day" aria-labelledby={`chat-day-${day}`} key={day}>
                <Text id={`chat-day-${day}`} className="chat-day-label">
                  {day}
                </Text>
                <ul>
                  {dayChats.map((chat) => (
                    <li key={chat.id}>
                      <button
                        className="chat-list-item"
                        type="button"
                        aria-current={chat.id === activeId ? "page" : undefined}
                        title={chat.preview || undefined}
                        onClick={() => {
                          setSelectedId(chat.id);
                          setSendError(undefined);
                          dismissDrawer();
                        }}
                      >
                        <span className="chat-list-item-icon">
                          <ShellIcon concept="nav.chats" size={16} />
                        </span>
                        <span className="chat-list-item-title">{chat.title}</span>
                        <span className="chat-list-item-time">{chat.time}</span>
                      </button>
                    </li>
                  ))}
                </ul>
              </section>
            ))}
          </div>
        </nav>

        <article
          className="chat-thread"
          aria-labelledby="current-chat-title"
          inert={phone && sidebarOpen}
        >
          <div className="chat-thread-header">
            <div className="chat-thread-header-left">
              <span className="chat-show" hidden={sidebarOpen}>
                <IconButton aria-label="Show chats" variant="subtle" size={28} onClick={reveal}>
                  <IconLayoutSidebar size={16} stroke={1.75} aria-hidden="true" />
                </IconButton>
              </span>
              <Title order={2} id="current-chat-title" className="chat-thread-title">
                {activeChat?.title ?? "Chat"}
              </Title>
            </div>
          </div>

          <ChatThread
            className="chat-messages"
            scrollKey={`${activeId ?? ""}:${sentCounts[activeId ?? ""] ?? 0}`}
            announcement={announcement}
            busy={thinking}
            empty={<Text className="chat-thread-empty">Write a message to start this chat.</Text>}
          >
            {lines.map((line) => (
              <ChatMessage
                key={line.id}
                role={line.role}
                content={line.body}
                model={line.model ?? undefined}
                tokens={line.tokens}
                cost={formatCost(line.cost_usd)}
              />
            ))}
            {thinking ? (
              <ChatMessage key="thinking" role="assistant" streaming copyable={false}>
                <span className="chat-thinking">Thinking</span>
              </ChatMessage>
            ) : null}
          </ChatThread>

          <div className="chat-composer-wrap">
            {sendError ? (
              <div className="chat-send-error">
                <StatusAlert tone="critical">{sendError}</StatusAlert>
              </div>
            ) : null}
            <Composer
              value={draft}
              onChange={handleDraftChange}
              onSend={handleSendMessage}
              disabled={activeId === undefined}
              streaming={thinking || send.isPending}
              config={{
                label: "Agent, route and model",
                groups: [
                  { label: "Agent", value: selectedAgent, options: AGENTS, onChange: setSelectedAgent },
                  { label: "Route", value: selectedRoute, options: ROUTES, onChange: setSelectedRoute },
                  { label: "Model", value: selectedModel, options: MODELS, onChange: setSelectedModel },
                ],
              }}
              toolsMode={{ value: selectedTools, options: TOOLS_OPTIONS, onChange: setSelectedTools }}
              onAttach={() => {}}
              onDictate={() => {}}
            />
          </div>
        </article>
      </div>
    </section>
  );
}
