import { Anchor, Card, Stack, Text, ThemeIcon, Title, useMantineTheme } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { IconLayoutSidebar, IconPencil, IconPlus, IconSearch, IconServer, IconTrash } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState, type ChangeEvent } from "react";
import { Button, ChatMessage, ChatThread, Composer, IconButton, Input, StatusAlert, Wordmark } from "@upmo-ai/components";
import type { ComposerOption } from "@upmo-ai/components";

import {
  agentsQueryKey,
  chatQueryKey,
  chatShareQueryKey,
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
import { ShareDialog } from "../identity/share-dialog";
import type { GatewayRoute } from "../models/types";
import { ShellIcon } from "../../ui/icons";
import { awaitingReply, formatCost, groupByDay, sendRefusal } from "./format";
import {
  AUTO,
  chatPicks,
  chatRoutes,
  loadPicks,
  modelChoices,
  modelDescription,
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
const MAX_ATTACHMENTS = 10;
const MAX_ATTACHMENT_BYTES = 20 * 1024 * 1024;
const ATTACHMENT_ACCEPT = ".txt,.md,.pdf,.docx,.html,.htm";

function attachmentLimitError(files: readonly File[]): string | undefined {
  if (files.length > MAX_ATTACHMENTS) {
    return `You can attach at most ${MAX_ATTACHMENTS} files per message.`;
  }
  const totalBytes = files.reduce((sum, file) => sum + file.size, 0);
  if (totalBytes > MAX_ATTACHMENT_BYTES) {
    return "Attachments must total 20 MB or less.";
  }
  return undefined;
}

const noLines: ChatLine[] = [];
const noChats: ChatSummary[] = [];

// The composer labels only consecutive options of one group, so a provider's models sit together, in the order its first account was listed.
function byProvider<T extends { provider: string }>(items: readonly T[]): T[] {
  const order = [...new Set(items.map((item) => item.provider))];
  return [...items].sort((a, b) => order.indexOf(a.provider) - order.indexOf(b.provider));
}

// The guide a chat shows when nothing in the workspace can answer: a
// provider first, then a route or an agent if one is still needed. The same
// pieces as the providers page's empty state.
function SetupGuide() {
  return (
    <div data-testid="chat-setup-guide">
    <Card withBorder radius="lg" padding="xl" style={{ borderStyle: "dashed" }}>
      <Stack align="center" gap="sm" py={48}>
        <ThemeIcon variant="light" size={48} radius="lg" color="teal">
          <IconServer size={24} aria-hidden="true" />
        </ThemeIcon>
        <Title order={3}>Connect a provider</Title>
        <Text c="dimmed" size="sm" maw={360} ta="center">
          Nothing here can answer yet. Connect a provider with a working model first.
        </Text>
        <Button mt="xs" component={Link} to="/settings/providers/new" leftSection={<IconPlus size={16} aria-hidden="true" />}>
          Add provider
        </Button>
        <Text size="sm" c="dimmed" ta="center">
          Then create a <Anchor component={Link} to="/models">route</Anchor> or an{" "}
          <Anchor component={Link} to="/agents">agent</Anchor> if you need one.
        </Text>
      </Stack>
    </Card>
    </div>
  );
}

function ChatAuthorAvatar({ avatarUrl, name }: { avatarUrl?: string | null; name: string }) {
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  const showImage = Boolean(avatarUrl) && failedUrl !== avatarUrl;
  return (
    <span className="chat-user-avatar">
      {showImage ? (
        <img
          className="chat-user-avatar-img"
          src={avatarUrl!}
          alt={name}
          onError={() => setFailedUrl(avatarUrl ?? null)}
        />
      ) : (
        <Wordmark compact height={14} label={name} />
      )}
    </span>
  );
}

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
  const [sharingId, setSharingId] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  // The rename form is open with its text as the new title; the delete form
  // asks its question while `deleting` is on.
  const [renaming, setRenaming] = useState(false);
  const [renameText, setRenameText] = useState("");
  const [renameError, setRenameError] = useState<string>();
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string>();
  const [draft, setDraft] = useState("");
  const [attachments, setAttachments] = useState<File[]>([]);
  const fileInputRef = useRef<HTMLInputElement>(null);
  // What this browser remembered; a pick that names something gone is Auto again once the lists are read.
  const [remembered, setRemembered] = useState<Picks>(loadPicks);
  const [sendError, setSendError] = useState<string>();
  // Chats a reply was asked for in this session (a send or a retry went through).
  // A reply the page asked for in this session is a wait even before the brain marks the
  // line pending, which tells it from a chat whose last line is simply the person's.
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
  // The query key carries the search text, so a new query fetches a new list.
  const chats = useQuery({
    queryKey: chatsQueryKey(query),
    queryFn: () => api.chats.list(query),
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
  const sharing = sharingId !== null && sharingId === activeId;

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
  // Nothing usable anywhere: no model and no route can answer, so the thread
  // guides the person to a provider first. Only lists that loaded may say
  // so, or a slow brain would flash the guide.
  const nothingUsable = modelsKnown && routesKnown && models.length === 0 && routes.length === 0;
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
    { value: AUTO, label: "Auto", description: "No agent", pinned: true },
    ...agents.map((item) => ({ value: item.id, label: item.name, description: item.description })),
  ];
  // The agent's own route and model are the defaults Auto stands for; a pick on the row overrides them.
  const routeOptions: ComposerOption[] = [
    {
      value: AUTO,
      pinned: true,
      label: agent?.route ? `Auto (${agent.route})` : "Auto",
      description: agent?.route ? "The agent's route" : "The agent's route, else the install's",
    },
    ...routes.map((item) => ({ value: item.name, label: item.name, description: item.capability })),
  ];
  const agentModel = agent?.model_id ? modelLabel(agent.model_provider, agent.model_id) : "";
  const modelOptions: ComposerOption[] = [
    {
      value: AUTO,
      pinned: true,
      label: agentModel ? `Auto (${agentModel})` : "Auto",
      description: agentModel ? "The agent's model" : "The agent's model, else the route's chain",
    },
    ...byProvider(models).map((item) => ({
      value: item.value,
      label: item.model,
      group: item.provider,
      description: modelDescription(item),
    })),
  ];
  // Taken when the button is pressed, so a retry asks on the picks made now.
  const currentPicks = (): ChatPicks => chatPicks(picks, models);

  const send = useWrite(
    (input: { id: string; body: string; picks: ChatPicks }) => api.chats.send(input.id, input.body, input.picks),
    [chatsQueryKey(), chatQueryKey(activeId ?? "")],
    {
      // The composer words a failure itself, so the shell banner does not.
      shown: true,
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
        if (input.picks.files && input.picks.files.length > 0) {
          setAttachments((current) => (current.length === 0 ? input.picks.files ?? [] : current));
        }
      },
    },
  );

  // Asks again with the picks made now, not the ones the failed reply used.
  const retry = useWrite(
    (input: { id: string; lineId: string; picks: ChatPicks }) => api.chats.retry(input.id, input.lineId, input.picks),
    [chatQueryKey(activeId ?? "")],
    {
      shown: true,
      onSuccess: (_, input) => {
        setAsked((before) => ({ ...before, [input.id]: true }));
      },
      onError: (error, input) => {
        setRetrying((ids) => new Set([...ids].filter((id) => id !== input.lineId)));
        setSendError(sendRefusal(error));
      },
    },
  );

  const stop = useWrite(
    (id: string) => api.chats.stop(id),
    [chatQueryKey(activeId ?? "")],
    { shown: true, onError: (error) => setSendError(sendRefusal(error)) },
  );

  const branch = useWrite(
    (input: { id: string; lineId: string; retry: boolean; picks: ChatPicks }) =>
      api.chats.branch(input.id, input.lineId, { retry: input.retry, picks: input.picks }),
    [chatsQueryKey()],
    {
      shown: true,
      onSuccess: (created) => {
        setSelectedId(created.id);
        dismissDrawer();
      },
      onError: (error) => setSendError(sendRefusal(error)),
    },
  );

  const create = useWrite(() => api.chats.create(), [chatsQueryKey()], {
    onSuccess: (created) => {
      setSelectedId(created.id);
      dismissDrawer();
    },
  });

  const rename = useWrite(
    (input: { id: string; title: string }) => api.chats.rename(input.id, input.title),
    [chatsQueryKey(), chatQueryKey(activeId ?? "")],
    {
      onSuccess: () => {
        setRenameError(undefined);
        setRenaming(false);
        setRenameText("");
      },
      onError: (error) => setRenameError(sendRefusal(error)),
    },
  );

  const remove = useWrite(
    (id: string) => api.chats.remove(id),
    [chatsQueryKey()],
    {
      onSuccess: (_, id) => {
        setDeleting(false);
        setDeleteError(undefined);
        if (selectedId === id) setSelectedId(undefined);
      },
      onError: (error) => setDeleteError(sendRefusal(error)),
    },
  );

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

  // The server answers the search: no client-side filtering of the list.
  const needle = query.trim().toLocaleLowerCase("en-GB");
  const days = chats.data?.days ?? [];

  const handleFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const picked = Array.from(event.currentTarget.files ?? []);
    event.currentTarget.value = "";
    if (picked.length === 0) return;
    const next = [...attachments, ...picked];
    setAttachments(next);
    setSendError(attachmentLimitError(next));
  };

  const handleRemoveAttachment = (index: number) => {
    const next = attachments.filter((_, itemIndex) => itemIndex !== index);
    setAttachments(next);
    setSendError(attachmentLimitError(next));
  };

  const handleSendMessage = () => {
    const text = draft.trim();
    if (!text || !activeId || thinking || send.isPending) return;
    const limitErr = attachmentLimitError(attachments);
    if (limitErr) {
      setSendError(limitErr);
      return;
    }
    const filesToSend = attachments;
    setSendError(undefined);
    setDraft("");
    setAttachments([]);
    awaited.current.set(activeId, lines.at(-1)?.id);
    const picksToSend: ChatPicks = filesToSend.length > 0 ? { ...currentPicks(), files: filesToSend } : currentPicks();
    send.mutate({ id: activeId, body: text, picks: picksToSend });
  };

  const handleRetry = (lineId: string) => {
    if (!activeId || thinking || retry.isPending) return;
    setSendError(undefined);
    setRetrying((ids) => new Set(ids).add(lineId));
    retry.mutate({ id: activeId, lineId, picks: currentPicks() });
  };

  const handleStop = () => {
    if (!activeId || stop.isPending) return;
    setSendError(undefined);
    stop.mutate(activeId);
  };

  const handleBranch = (lineId: string, again: boolean) => {
    if (!activeId || thinking || branch.isPending) return;
    setSendError(undefined);
    branch.mutate({ id: activeId, lineId, retry: again, picks: currentPicks() });
  };

  const latestReplyId = [...lines].reverse().find((line) => line.role === "assistant" && line.status !== "pending")?.id;
  const lineActions = (line: ChatLine) => {
    const latest = line.id === latestReplyId;
    return (
      <div className="chat-line-actions">
        <Button size="xs" variant="default" onClick={() => (latest ? handleRetry(line.id) : handleBranch(line.id, true))} disabled={thinking}>
          Retry
        </Button>
        {latest ? null : (
          <Button size="xs" variant="subtle" onClick={() => handleBranch(line.id, false)} disabled={thinking}>
            Branch from here
          </Button>
        )}
      </div>
    );
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

  const openRename = () => {
    setRenameText(activeChat?.title ?? "");
    setRenameError(undefined);
    setRenaming(true);
  };

  const submitRename = () => {
    if (!activeId || !renameText.trim() || rename.isPending) return;
    rename.mutate({ id: activeId, title: renameText.trim() });
  };

  const openDelete = () => {
    setDeleteError(undefined);
    setDeleting(true);
  };

  const confirmDelete = () => {
    if (!activeId || remove.isPending) return;
    remove.mutate(activeId);
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
                          setAttachments([]);
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
              {renaming && thread.data?.can_edit ? (
                <form
                  className="chat-rename"
                  onSubmit={(event) => {
                    event.preventDefault();
                    submitRename();
                  }}
                >
                  <Input
                    size="xs"
                    aria-label="New chat title"
                    value={renameText}
                    onChange={(event) => setRenameText(event.currentTarget.value)}
                    rightSection={
                      <Button type="submit" size="xs" variant="filled" disabled={!renameText.trim() || rename.isPending}>
                        Save
                      </Button>
                    }
                  />
                  {renameError ? <Text c="red" size="xs">{renameError}</Text> : null}
                </form>
              ) : (
                <>
                  <Title order={2} id="current-chat-title" className="chat-thread-title">
                    {activeChat?.title ?? "Chat"}
                  </Title>
                  {thread.data?.can_edit ? (
                    <IconButton aria-label="Rename chat" variant="subtle" size={28} className="chat-head-btn chat-rename-btn" onClick={openRename}>
                      <IconPencil size={16} stroke={1.75} aria-hidden="true" />
                    </IconButton>
                  ) : null}
                </>
              )}
            </div>
            {activeChat ? (
              <Button variant="subtle" size="xs" onClick={() => setSharingId(activeChat.id)}>
                Share
              </Button>
            ) : null}
            {thread.data?.can_delete ? deleting ? (
              <div className="chat-delete">
                {deleteError ? <Text c="red" size="xs">{deleteError}</Text> : null}
                <Text c="red" size="xs">
                  Delete this chat?
                </Text>
                <Button size="xs" variant="default" aria-label="Confirm delete chat" onClick={confirmDelete}>
                  Delete
                </Button>
                <Button size="xs" variant="subtle" aria-label="Cancel delete chat" onClick={() => setDeleting(false)}>
                  Cancel
                </Button>
              </div>
            ) : (
              <IconButton aria-label="Delete chat" variant="subtle" size={28} className="chat-head-btn chat-delete-btn" onClick={openDelete}>
                <IconTrash size={16} stroke={1.75} aria-hidden="true" />
              </IconButton>
            ) : null}
          </div>

          <ChatThread
            className="chat-messages"
            scrollKey={`${activeId ?? ""}:${sentCounts[activeId ?? ""] ?? 0}`}
            announcement={announcement}
            busy={thinking}
            empty={nothingUsable ? <SetupGuide /> : <Text className="chat-thread-empty">Write a message to start this chat.</Text>}
          >
            {lines.map((line) => {
              if ((line.status === "failed" || line.status === "stopped") && retrying.has(line.id)) return null;
              if (line.status === "pending") {
                return <ChatMessage key={line.id} role="assistant" content={line.body} streaming copyable={false} />;
              }
              if (line.status === "stopped") {
                const text = line.body === "" ? "Reply stopped." : line.body;
                return (
                  <Fragment key={line.id}>
                    <ChatMessage role="assistant" content={text} copyable={false} />
                    {lineActions(line)}
                  </Fragment>
                );
              }
              if (line.status === "failed") {
                return (
                  <ChatMessage key={line.id} role="assistant" copyable={false}>
                    <div className="chat-failed">
                      <StatusAlert tone="critical">{line.body}</StatusAlert>
                      {line.attempts?.length ? <Text className="chat-attempts">{attemptsText(line.attempts)}</Text> : null}
                      {lineActions(line)}
                    </div>
                  </ChatMessage>
                );
              }
              if (line.role === "user" && (line.author_name || (line.attachments && line.attachments.length > 0))) {
                return (
                  <ChatMessage key={line.id} role="user" tokens={line.tokens} cost={formatCost(line.cost_usd)}>
                    <div className="chat-user-with-attachments">
                      {line.author_name ? (
                        <div className="chat-user-author">
                          <ChatAuthorAvatar avatarUrl={line.author_avatar} name={line.author_name} />
                          <span className="chat-user-author-name">{line.author_name}</span>
                        </div>
                      ) : null}
                      <div className="chat-user-body">{line.body}</div>
                      {line.attachments && line.attachments.length > 0 ? (
                        <div className="chat-line-attachments" aria-label="Attachments">
                          {line.attachments.map((att, index) => (
                            <span className="chat-attachment-badge" key={`${att.name}-${index}`}>
                              {att.name}
                            </span>
                          ))}
                        </div>
                      ) : null}
                      {lineActions(line)}
                    </div>
                  </ChatMessage>
                );
              }
              return (
                <Fragment key={line.id}>
                  <ChatMessage
                    role={line.role}
                    content={line.body}
                    model={line.model ?? undefined}
                    tokens={line.tokens}
                    cost={formatCost(line.cost_usd)}
                  />
                  {lineActions(line)}
                </Fragment>
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
            <input
              ref={fileInputRef}
              type="file"
              multiple
              accept={ATTACHMENT_ACCEPT}
              aria-label="Attach files"
              className="chat-file-input"
              hidden
              onChange={handleFileChange}
            />
            {attachments.length > 0 ? (
              <div className="chat-attachments-pending" role="list" aria-label="Selected attachments">
                {attachments.map((file, index) => (
                  <span className="chat-attachment-chip" role="listitem" key={`${file.name}-${file.size}-${index}`}>
                    <span className="chat-attachment-chip-name">{file.name}</span>
                    <button
                      type="button"
                      className="chat-attachment-chip-remove"
                      aria-label={`Remove ${file.name}`}
                      onClick={() => handleRemoveAttachment(index)}
                    >
                      ×
                    </button>
                  </span>
                ))}
              </div>
            ) : null}
            {thinking ? (
              <Button size="xs" variant="default" onClick={handleStop} disabled={stop.isPending}>
                Stop
              </Button>
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
                  { label: "Agent", value: picks.agent, options: agentOptions, onChange: pick("agent"), searchable: true },
                  { label: "Route", value: picks.route, options: routeOptions, onChange: pick("route"), searchable: true },
                  { label: "Model", value: picks.model, options: modelOptions, onChange: pick("model"), searchable: true },
                ],
              }}
              onAttach={() => fileInputRef.current?.click()}
              onDictate={() => {}}
            />
          </div>
        </article>
      </div>
      {activeChat ? (
        <ShareDialog
          opened={sharing}
          objectId={activeChat.id}
          objectLabel="chat"
          onClose={() => setSharingId(null)}
          queryKey={chatShareQueryKey(activeChat.id)}
          loadShare={(id) => api.chats.getShare(id)}
          saveShare={(id, update) => api.chats.putShare(id, update)}
        />
      ) : null}
    </section>
  );
}
