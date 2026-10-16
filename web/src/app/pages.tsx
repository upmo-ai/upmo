import { Stack, Text, Title } from "@mantine/core";
import {
  IconActivity,
  IconArrowUp,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconCpu,
  IconLayoutSidebar,
  IconMicrophone,
  IconPaperclip,
  IconSearch,
} from "@tabler/icons-react";
import { useState } from "react";
import { Badge, Button, IconButton, Input, Kbd, KbdGroup, Menu, Textarea } from "@upmo-ai/components";

import { CaretIcon, ShellIcon } from "../ui/icons";
import { Section } from "../ui/section";

import "../features/chat/chat.css";

const chatDays = ["Today", "Yesterday", "Earlier"] as const;

type SampleChat = {
  id: string;
  day: (typeof chatDays)[number];
  title: string;
  preview: string;
  time: string;
  archived?: boolean;
};

const sampleChats: readonly SampleChat[] = [
  {
    id: "roadmap-priorities",
    day: "Today",
    title: "Roadmap priorities",
    preview: "A concise summary of the launch notes.",
    time: "09:42",
  },
  {
    id: "research-summary",
    day: "Today",
    title: "Research summary",
    preview: "Key findings and recommended next steps.",
    time: "08:15",
  },
  {
    id: "team-update",
    day: "Yesterday",
    title: "Team update draft",
    preview: "A polished draft for Friday's update.",
    time: "16:30",
    archived: true,
  },
  {
    id: "supplier-review",
    day: "Earlier",
    title: "Supplier review",
    preview: "Questions to prioritise before the meeting.",
    time: "28 Oct",
  },
];

export function HomePage() {
  return (
    <Section
      title="Home"
      emptyTitle="Your first task"
      emptyBody="Give Upmo a goal. It plans the work, does it and checks the result while you do something else."
      emptyAction={{ label: "Create task", to: "/tasks" }}
    />
  );
}

const AGENTS = [
  { id: "auto", name: "Auto", desc: "Intelligently select the best agent" },
  { id: "general", name: "General Assistant", desc: "General tasks and questions" },
  { id: "coder", name: "Code Specialist", desc: "Code generation and refactoring" },
  { id: "research", name: "Research Agent", desc: "Web search and summarization" },
  { id: "planner", name: "Planning Agent", desc: "Multi-step plan decomposition" },
];

const ROUTES = [
  { id: "default", name: "Default", desc: "Optimized speed and quality" },
  { id: "fastest", name: "Fastest", desc: "Lowest latency routing" },
  { id: "cost", name: "Cost Optimized", desc: "Most economical model routing" },
  { id: "direct", name: "Direct Provider", desc: "Direct upstream connection" },
];

const MODELS = [
  { id: "gpt-5.6-luna", name: "Gpt-5.6-Luna", provider: "OpenAI" },
  { id: "claude-3.7-sonnet", name: "Claude 3.7 Sonnet", provider: "Anthropic" },
  { id: "claude-3.5-haiku", name: "Claude 3.5 Haiku", provider: "Anthropic" },
  { id: "gpt-4o", name: "GPT-4o", provider: "OpenAI" },
  { id: "gemini-2.0-flash", name: "Gemini 2.0 Flash", provider: "Google" },
  { id: "deepseek-r1", name: "DeepSeek R1", provider: "DeepSeek" },
];

const TOOLS_OPTIONS = [
  { id: "auto", name: "Auto", desc: "Allow model to call connected tools" },
  { id: "all", name: "All Tools", desc: "Connectors and MCPs enabled" },
  { id: "search", name: "Search Only", desc: "Only web search allowed" },
  { id: "disabled", name: "Disabled", desc: "Pure model generation" },
];

type ChatMessageItem = {
  id: string;
  role: "user" | "assistant";
  content: string;
  tokens: number;
  cost: string;
  duration?: string;
  model?: string;
};

const initialChatMessages: ChatMessageItem[] = [
  {
    id: "m-user-1",
    role: "user",
    content: "Summarise the launch notes and highlight anything that needs a decision.",
    tokens: 18,
    cost: "$0.0002",
  },
  {
    id: "m-assistant-1",
    role: "assistant",
    content:
      "The notes point to three priorities: finish the onboarding copy, confirm the support rota and agree the release date. The release date is the only open decision.",
    tokens: 184,
    duration: "1.4 s",
    model: "Upmo",
    cost: "$0.002",
  },
];

export function ChatPage() {
  const [activeChatId, setActiveChatId] = useState<string>(sampleChats[0].id);
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const [selectedAgent, setSelectedAgent] = useState("Auto");
  const [selectedRoute, setSelectedRoute] = useState("Default");
  const [selectedModel, setSelectedModel] = useState("Gpt-5.6-Luna");
  const [selectedTools, setSelectedTools] = useState("Auto");
  const [messages, setMessages] = useState<ChatMessageItem[]>(initialChatMessages);
  const activeChat = sampleChats.find((chat) => chat.id === activeChatId) ?? sampleChats[0];
  const matchingChats = sampleChats.filter((chat) =>
    chat.title.toLocaleLowerCase("en-GB").includes(query.trim().toLocaleLowerCase("en-GB")),
  );

  const handleSendMessage = () => {
    const text = draft.trim();
    if (!text) return;
    const userTokens = Math.max(6, Math.round(text.length / 4));
    const userCost = `$${(userTokens * 0.00001).toFixed(4)}`;
    const newUserMsg: ChatMessageItem = {
      id: `m-user-${Date.now()}`,
      role: "user",
      content: text,
      tokens: userTokens,
      cost: userCost,
    };
    const assistantTokens = 140 + Math.floor(Math.random() * 60);
    const assistantCost = `$${(assistantTokens * 0.000012).toFixed(3)}`;
    const newAssistantMsg: ChatMessageItem = {
      id: `m-asst-${Date.now() + 1}`,
      role: "assistant",
      content: `I have processed your request with ${selectedModel} via the ${selectedRoute} route.`,
      tokens: assistantTokens,
      duration: "1.2 s",
      model: selectedModel,
      cost: assistantCost,
    };
    setMessages((prev) => [...prev, newUserMsg, newAssistantMsg]);
    setDraft("");
  };

  return (
    <section className="chat-page">
      <header className="chat-page-header">
        <Title order={1} className="chat-sr-title">Chat</Title>
      </header>

      <div className="chat-workspace" data-collapsed={!sidebarOpen}>
        <nav
          id="chat-list"
          className="chat-list"
          aria-label="Chat list"
          data-open={sidebarOpen}
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
              onClick={() => setDraft("")}
            >
              <ShellIcon concept="action.add" size={16} />
            </IconButton>
            <IconButton
              aria-label="Collapse sidebar"
              variant="subtle"
              size={28}
              className="chat-head-btn chat-sidebar-collapse-btn"
              onClick={() => setSidebarOpen(false)}
            >
              <IconLayoutSidebar size={16} stroke={1.75} aria-hidden="true" />
            </IconButton>
          </div>

          <div className="chat-list-scroll">
            {chatDays.map((day) => {
              const chats = matchingChats.filter((chat) => chat.day === day);
              if (chats.length === 0) return null;
              return (
                <section className="chat-day" aria-labelledby={`chat-day-${day}`} key={day}>
                  <Text id={`chat-day-${day}`} className="chat-day-label">
                    {day}
                  </Text>
                  <ul>
                    {chats.map((chat) => {
                      const current = chat.id === activeChat.id;
                      return (
                        <li key={chat.id}>
                          <button
                            className="chat-list-item"
                            type="button"
                            aria-current={current ? "page" : undefined}
                            title={chat.preview}
                            onClick={() => {
                              setActiveChatId(chat.id);
                              if (typeof window !== "undefined" && window.innerWidth < 992) {
                                setSidebarOpen(false);
                              }
                            }}
                          >
                            <span className="chat-list-item-icon">
                              <ShellIcon concept="nav.chats" size={16} />
                            </span>
                            <span className="chat-list-item-title">{chat.title}</span>
                            {chat.archived ? (
                              <Badge size="xs" variant="light" className="chat-list-item-badge">
                                Archived
                              </Badge>
                            ) : (
                              <span className="chat-list-item-time">{chat.time}</span>
                            )}
                          </button>
                        </li>
                      );
                    })}
                  </ul>
                </section>
              );
            })}
          </div>
        </nav>

        <article className="chat-thread" aria-labelledby="current-chat-title">
          <div className="chat-thread-header">
            <div className="chat-thread-header-left">
              {!sidebarOpen ? (
                <IconButton
                  aria-label="Show chats"
                  variant="subtle"
                  size={28}
                  onClick={() => setSidebarOpen(true)}
                >
                  <IconLayoutSidebar size={16} stroke={1.75} aria-hidden="true" />
                </IconButton>
              ) : null}
              <Title order={2} id="current-chat-title" className="chat-thread-title">
                {activeChat.title}
              </Title>
            </div>
          </div>

          <Stack className="chat-messages" gap="md" aria-label="Messages">
            {messages.map((msg) =>
              msg.role === "user" ? (
                <div key={msg.id} className="chat-message chat-message-user">
                  <Text>{msg.content}</Text>
                  <div className="chat-message-meta chat-message-meta-user">
                    <span>{msg.tokens} tokens</span>
                    <span>·</span>
                    <span className="chat-cost">{msg.cost}</span>
                  </div>
                </div>
              ) : (
                <div key={msg.id} className="chat-message chat-message-assistant">
                  <span className="chat-avatar" aria-hidden>
                    <CaretIcon size={18} />
                  </span>
                  <div className="chat-reply">
                    <Text>{msg.content}</Text>
                    <div className="chat-message-meta">
                      <span>{msg.model ?? "Upmo"}</span>
                      <span>·</span>
                      <span>{msg.tokens} tokens</span>
                      <span>·</span>
                      <span>{msg.duration ?? "1.4 s"}</span>
                      <span>·</span>
                      <span className="chat-cost">{msg.cost}</span>
                      <IconButton
                        aria-label="Copy reply"
                        variant="subtle"
                        size={24}
                        onClick={() => {
                          void navigator.clipboard?.writeText(msg.content);
                        }}
                      >
                        <ShellIcon concept="action.copy" size={16} />
                      </IconButton>
                      <IconButton aria-label="Activity" variant="subtle" size={24}>
                        <IconActivity size={14} stroke={1.75} aria-hidden="true" />
                      </IconButton>
                    </div>
                  </div>
                </div>
              ),
            )}
          </Stack>

          <div className="chat-composer-wrap">
            <form
              className="chat-composer"
              aria-label="Chat composer"
              onSubmit={(event) => {
                event.preventDefault();
                handleSendMessage();
              }}
            >
              <Textarea
                className="chat-composer-textarea"
                aria-label="Message"
                placeholder="Message Upmo…"
                minRows={2}
                value={draft}
                onChange={(event) => setDraft(event.currentTarget.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.shiftKey) {
                    event.preventDefault();
                    handleSendMessage();
                  }
                }}
              />
              <div className="chat-composer-row">
                <Menu position="top-start" offset={8} shadow="md" width={240} withinPortal transitionProps={{ duration: 0 }}>
                  <Menu.Target>
                    <Button
                      variant="subtle"
                      size="xs"
                      className="chat-composer-pill"
                      leftSection={<IconCpu size={14} stroke={1.75} aria-hidden />}
                      rightSection={<IconChevronDown size={12} stroke={2} aria-hidden />}
                      aria-label="Agent, route, and model configuration"
                    >
                      {selectedAgent} · {selectedRoute} · {selectedModel}
                    </Button>
                  </Menu.Target>
                  <Menu.Dropdown className="chat-select-dropdown">
                    <Menu.Label className="chat-select-label">SELECT</Menu.Label>

                    <Menu.Sub>
                      <Menu.Sub.Target>
                        <Menu.Item
                          className="chat-select-menu-item"
                          rightSection={<IconChevronRight size={14} stroke={1.75} aria-hidden />}
                        >
                          <div className="chat-menu-item-content">
                            <span className="chat-menu-item-title">Agent</span>
                            <span className="chat-menu-item-sub">{selectedAgent}</span>
                          </div>
                        </Menu.Item>
                      </Menu.Sub.Target>
                      <Menu.Sub.Dropdown className="chat-select-dropdown">
                        <Menu.Label className="chat-select-label">AGENT</Menu.Label>
                        {AGENTS.map((item) => (
                          <Menu.Item
                            key={item.id}
                            leftSection={selectedAgent === item.name ? <IconCheck size={14} stroke={2} color="var(--emerald-deep)" /> : <span style={{ width: 14 }} />}
                            onClick={() => setSelectedAgent(item.name)}
                          >
                            <div>
                              <div className="chat-menu-subitem-title">{item.name}</div>
                              <div className="chat-menu-subitem-desc">{item.desc}</div>
                            </div>
                          </Menu.Item>
                        ))}
                      </Menu.Sub.Dropdown>
                    </Menu.Sub>

                    <Menu.Sub>
                      <Menu.Sub.Target>
                        <Menu.Item
                          className="chat-select-menu-item"
                          rightSection={<IconChevronRight size={14} stroke={1.75} aria-hidden />}
                        >
                          <div className="chat-menu-item-content">
                            <span className="chat-menu-item-title">Route</span>
                            <span className="chat-menu-item-sub">{selectedRoute}</span>
                          </div>
                        </Menu.Item>
                      </Menu.Sub.Target>
                      <Menu.Sub.Dropdown className="chat-select-dropdown">
                        <Menu.Label className="chat-select-label">ROUTE</Menu.Label>
                        {ROUTES.map((item) => (
                          <Menu.Item
                            key={item.id}
                            leftSection={selectedRoute === item.name ? <IconCheck size={14} stroke={2} color="var(--emerald-deep)" /> : <span style={{ width: 14 }} />}
                            onClick={() => setSelectedRoute(item.name)}
                          >
                            <div>
                              <div className="chat-menu-subitem-title">{item.name}</div>
                              <div className="chat-menu-subitem-desc">{item.desc}</div>
                            </div>
                          </Menu.Item>
                        ))}
                      </Menu.Sub.Dropdown>
                    </Menu.Sub>

                    <Menu.Sub>
                      <Menu.Sub.Target>
                        <Menu.Item
                          className="chat-select-menu-item"
                          rightSection={<IconChevronRight size={14} stroke={1.75} aria-hidden />}
                        >
                          <div className="chat-menu-item-content">
                            <span className="chat-menu-item-title">Model</span>
                            <span className="chat-menu-item-sub">{selectedModel}</span>
                          </div>
                        </Menu.Item>
                      </Menu.Sub.Target>
                      <Menu.Sub.Dropdown className="chat-select-dropdown">
                        <Menu.Label className="chat-select-label">MODEL</Menu.Label>
                        {MODELS.map((item) => (
                          <Menu.Item
                            key={item.id}
                            leftSection={selectedModel === item.name ? <IconCheck size={14} stroke={2} color="var(--emerald-deep)" /> : <span style={{ width: 14 }} />}
                            onClick={() => setSelectedModel(item.name)}
                          >
                            <div>
                              <div className="chat-menu-subitem-title">{item.name}</div>
                              <div className="chat-menu-subitem-desc">{item.provider}</div>
                            </div>
                          </Menu.Item>
                        ))}
                      </Menu.Sub.Dropdown>
                    </Menu.Sub>
                  </Menu.Dropdown>
                </Menu>

                <IconButton
                  aria-label="Attach file"
                  variant="subtle"
                  size={26}
                  className="chat-composer-icon-btn"
                  type="button"
                  tooltipPosition="top"
                >
                  <IconPaperclip size={16} stroke={1.75} aria-hidden />
                </IconButton>

                <IconButton
                  aria-label="Dictate"
                  variant="subtle"
                  size={26}
                  className="chat-composer-icon-btn"
                  type="button"
                  tooltipPosition="top"
                >
                  <IconMicrophone size={16} stroke={1.75} aria-hidden />
                </IconButton>

                <Menu position="top-start" offset={8} shadow="md" width={200} withinPortal transitionProps={{ duration: 0 }}>
                  <Menu.Target>
                    <Button
                      variant="subtle"
                      size="xs"
                      className="chat-composer-pill"
                      rightSection={<IconChevronDown size={12} stroke={2} aria-hidden />}
                      aria-label="Tools mode"
                    >
                      {selectedTools}
                    </Button>
                  </Menu.Target>
                  <Menu.Dropdown className="chat-select-dropdown">
                    <Menu.Label className="chat-select-label">TOOLS & CONTEXT</Menu.Label>
                    {TOOLS_OPTIONS.map((item) => (
                      <Menu.Item
                        key={item.id}
                        leftSection={selectedTools === item.name ? <IconCheck size={14} stroke={2} color="var(--emerald-deep)" /> : <span style={{ width: 14 }} />}
                        onClick={() => setSelectedTools(item.name)}
                      >
                        <div>
                          <div className="chat-menu-subitem-title">{item.name}</div>
                          <div className="chat-menu-subitem-desc">{item.desc}</div>
                        </div>
                      </Menu.Item>
                    ))}
                  </Menu.Dropdown>
                </Menu>

                <span className="chat-composer-spacer" />

                <IconButton
                  aria-label="Send"
                  type="submit"
                  variant="filled"
                  size={28}
                  disabled={draft.trim() === ""}
                  className="chat-composer-send"
                  tooltip="Send message"
                  tooltipPosition="top"
                >
                  <IconArrowUp size={16} stroke={2.5} aria-hidden />
                </IconButton>
              </div>
            </form>
            <div className="chat-composer-hint">
              <Kbd>Enter</Kbd> to send ·{" "}
              <KbdGroup>
                <Kbd>Shift</Kbd>
                <Kbd>Enter</Kbd>
              </KbdGroup>{" "}
              for a new line
            </div>
          </div>
        </article>
      </div>
    </section>
  );
}

export function AutomationsPage() {
  return (
    <Section
      title="Automations"
      emptyTitle="Run tasks on a schedule"
      emptyBody="A daily digest, a weekly review, a check every hour."
      emptyAction={{ label: "Add automation", to: "/automations" }}
    />
  );
}

export function AgentsPage() {
  return (
    <Section
      title="Agents"
      emptyTitle="Add an agent"
      emptyBody="Agents run tasks with the tools you give them."
      emptyAction={{ label: "Add agent", to: "/agents" }}
    />
  );
}
