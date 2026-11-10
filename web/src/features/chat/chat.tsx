import { Text, Title, useMantineTheme } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { IconLayoutSidebar, IconSearch } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button, ChatMessage, ChatThread, Composer, IconButton, Input, StatusAlert } from "@upmo-ai/components";
import type { ComposerOption } from "@upmo-ai/components";

import {
  agentsQueryKey,
  chatQueryKey,
  chatsQueryKey,
  gatewayRoutesQueryKey,
  providersQueryKey,
  useApi,
  useWrite,
  type AgentProfile,
  type Chat,
  type ChatLine,
  type ChatPicks,
  type ChatSummary,
  type ProviderAccount,
} from "../../app/api";
import type { GatewayRoute } from "../models/types";
import { ShellIcon } from "../../ui/icons";
import { awaitingReply, formatCost, groupByDay, sendRefusal } from "./format";
import {
  AUTO,
  chatPicks,
  chatRoutes,
  loadPicks,
  modelChoices,
  modelLabel,
  pickableAgents,
  savePicks,
  validPicks,
  type Picks,
} from "./picks";
import { attemptsText, keepPending, noticeText, useChatStream } from "./stream";

import "./chat.css";

// How often the open chat is read again while a reply is awaited.
const REPLY_POLL_MS = 300;

const noLines: ChatLine[] = [];
const noChats: ChatSummary[] = [];

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
  // What this browser remembered; a pick that names something gone is Auto again once the lists are read.
  const [remembered, setRemembered] = useState<Picks>(loadPicks);
  const [selectedTools, setSelectedTools] = useState("auto");
  const [sendError, setSendError] = useState<string>();
  // Chats a reply was asked for in this session (a send or a retry went through).
  // The brain does not mark a user line as awaiting a reply, so this is what
  // tells the wait from a chat whose last line is simply the person's.
  const [asked, setAsked] = useState<Record<string, boolean>>({});
  // Failed replies being asked for again: hidden until the new reply or failure lands.
  const [retrying, setRetrying] = useState<ReadonlySet<string>>(new Set());
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

  // The stub brain has no stream and jsdom has no EventSource: the page polls as before.
  const streams = api.chatStream && typeof EventSource !== "undefined";
  const streamState = useChatStream(activeId);
  const repliesExpected = api.chats.repliesLater || (streams && asked[activeId ?? ""] === true);
  const waiting = (data: Chat | undefined, retried: ReadonlySet<string>) => {
    const last = data?.lines.at(-1);
    return (
      last?.status === "pending" ||
      (last?.status === "failed" && retried.has(last.id)) ||
      awaitingReply(data?.lines, repliesExpected)
    );
  };

  const thread = useQuery({
    queryKey: chatQueryKey(activeId ?? ""),
    queryFn: async () =>
      keepPending(
        await api.chats.get(activeId ?? ""),
        queryClient.getQueryData<Chat>(chatQueryKey(activeId ?? "")),
      ),
    enabled: activeId !== undefined,
    // A reply that streams in needs no polling; one that cannot stream is polled for.
    refetchInterval: (current) =>
      waiting(current.state.data, retrying) && !(streams && streamState === "streaming") ? REPLY_POLL_MS : false,
  });
  const lines = thread.data?.lines ?? noLines;
  const thinking = waiting(thread.data, retrying);
  const pending = lines.at(-1)?.status === "pending";
  const notices = (thread.data?.notices ?? []).filter(
    (notice) => !lines.some((line) => line.id === notice.line_id && line.status !== "pending"),
  );
  // A refusal, even a 401, must not read as a lost session here: a list that fails is null, which
  // offers only Auto and sends nothing, but leaves the remembered pick alone.
  const gatewayRoutes = useQuery({
    queryKey: gatewayRoutesQueryKey(),
    queryFn: () => api.gateway.routes().catch((): GatewayRoute[] | null => null),
  });
  const agentList = useQuery({
    queryKey: agentsQueryKey(),
    queryFn: () => api.agents.list().catch((): AgentProfile[] | null => null),
  });
  const accountList = useQuery({
    queryKey: providersQueryKey(),
    queryFn: () => api.providers.list().catch((): ProviderAccount[] | null => null),
  });
  const agents = pickableAgents(agentList.data ?? []);
  const routes = chatRoutes(gatewayRoutes.data ?? []);
  const models = modelChoices(accountList.data ?? []);
  const picks = validPicks(remembered, agents, routes, models);
  // Only a list that loaded can say a pick is gone; for one that did not, the remembered pick stays in storage.
  const agentsKnown = agentList.data != null;
  const routesKnown = gatewayRoutes.data != null;
  const modelsKnown = accountList.data != null;
  useEffect(() => {
    savePicks({
      agent: agentsKnown ? picks.agent : remembered.agent,
      route: routesKnown ? picks.route : remembered.route,
      model: modelsKnown ? picks.model : remembered.model,
    });
  }, [agentsKnown, routesKnown, modelsKnown, picks.agent, picks.route, picks.model, remembered.agent, remembered.route, remembered.model]);
  const agent = agents.find((item) => item.id === picks.agent);
  const pick = (axis: keyof Picks) => (value: string) => setRemembered({ ...remembered, [axis]: value });
  const agentOptions: ComposerOption[] = [
    { value: AUTO, label: "Auto", description: "No agent" },
    ...agents.map((item) => ({ value: item.id, label: item.name, description: item.description })),
  ];
  // The agent's own route and model are the defaults Auto stands for; a pick on the row overrides them.
  const routeOptions: ComposerOption[] = [
    {
      value: AUTO,
      label: agent?.route ? `Auto (${agent.route})` : "Auto",
      description: agent?.route ? "The agent's route" : "The agent's route, else the install's",
    },
    ...routes.map((item) => ({ value: item.name, label: item.name, description: item.capability })),
  ];
  const agentModel = agent?.model_id ? modelLabel(agent.model_provider, agent.model_id) : "";
  const modelOptions: ComposerOption[] = [
    {
      value: AUTO,
      label: agentModel ? `Auto (${agentModel})` : "Auto",
      description: agentModel ? "The agent's model" : "The agent's model, else the route's chain",
    },
    ...models.map((item) => ({
      value: item.value,
      label: modelLabel(item.provider, item.model),
      description: item.scope === "global" ? `${item.account} (workspace)` : item.account,
    })),
  ];
  // Taken when the button is pressed, so a retry asks on the picks made now.
  const currentPicks = (): ChatPicks => chatPicks(picks, models);

  const send = useWrite(
    (input: { id: string; body: string; picks: ChatPicks }) => api.chats.send(input.id, input.body, input.picks),
    [chatsQueryKey(), chatQueryKey(activeId ?? "")],
    {
      onSuccess: (_, input) => {
        // The chat the line went to may no longer be the open one.
        void queryClient.invalidateQueries({ queryKey: chatQueryKey(input.id) });
        setSentCounts((counts) => ({ ...counts, [input.id]: (counts[input.id] ?? 0) + 1 }));
        setAsked((before) => ({ ...before, [input.id]: true }));
      },
      onError: (error, input) => {
        awaited.current.delete(input.id);
        setSendError(sendRefusal(error));
        // Give the words back unless something new is already being typed.
        setDraft((current) => (current === "" ? input.body : current));
      },
    },
  );

  // Asks again with the picks made now, not the ones the failed reply used.
  const retry = useWrite(
    (input: { id: string; lineId: string; picks: ChatPicks }) => api.chats.retry(input.id, input.lineId, input.picks),
    [chatQueryKey(activeId ?? "")],
    {
      onSuccess: (_, input) => {
        setAsked((before) => ({ ...before, [input.id]: true }));
      },
      onError: (error, input) => {
        setRetrying((ids) => new Set([...ids].filter((id) => id !== input.lineId)));
        setSendError(sendRefusal(error));
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
    if (last?.role !== "assistant" || last.status === "pending" || last.id === awaited.current.get(activeId)) return;
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
    send.mutate({ id: activeId, body: text, picks: currentPicks() });
  };

  const handleRetry = (lineId: string) => {
    if (!activeId || thinking || retry.isPending) return;
    setSendError(undefined);
    setRetrying((ids) => new Set(ids).add(lineId));
    retry.mutate({ id: activeId, lineId, picks: currentPicks() });
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
            {lines.map((line) => {
              if (line.status === "failed" && retrying.has(line.id)) return null;
              if (line.status === "pending") {
                return <ChatMessage key={line.id} role="assistant" content={line.body} streaming copyable={false} />;
              }
              if (line.status === "failed") {
                return (
                  <ChatMessage key={line.id} role="assistant" copyable={false}>
                    <div className="chat-failed">
                      <StatusAlert tone="critical">{line.body}</StatusAlert>
                      {line.attempts?.length ? <Text className="chat-attempts">{attemptsText(line.attempts)}</Text> : null}
                      <Button size="xs" variant="default" onClick={() => handleRetry(line.id)} disabled={thinking}>
                        Retry
                      </Button>
                    </div>
                  </ChatMessage>
                );
              }
              return (
                <ChatMessage
                  key={line.id}
                  role={line.role}
                  content={line.body}
                  model={line.model ?? undefined}
                  tokens={line.tokens}
                  cost={formatCost(line.cost_usd)}
                />
              );
            })}
            {thinking && !pending ? (
              <ChatMessage key="thinking" role="assistant" streaming copyable={false}>
                <span className="chat-thinking">Thinking</span>
              </ChatMessage>
            ) : null}
            {notices.length > 0 ? (
              <div className="chat-notices" role="status">
                {notices.map((notice) => (
                  <Text key={`${notice.line_id}:${notice.failed_model}:${notice.next_model}`} className="chat-notice">
                    {noticeText(notice)}
                  </Text>
                ))}
              </div>
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
                  { label: "Agent", value: picks.agent, options: agentOptions, onChange: pick("agent") },
                  { label: "Route", value: picks.route, options: routeOptions, onChange: pick("route") },
                  { label: "Model", value: picks.model, options: modelOptions, onChange: pick("model") },
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
