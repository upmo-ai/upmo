import { Text, Title } from "@mantine/core";
import { IconLayoutSidebar, IconSearch } from "@tabler/icons-react";
import { useState } from "react";
import { Badge, ChatMessage, ChatThread, Composer, IconButton, Input } from "@upmo-ai/components";
import type { ComposerOption } from "@upmo-ai/components";

import { ShellIcon } from "../ui/icons";
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

function optionLabel(options: readonly ComposerOption[], value: string) {
  return options.find((option) => option.value === value)?.label ?? value;
}

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
  const [selectedAgent, setSelectedAgent] = useState("auto");
  const [selectedRoute, setSelectedRoute] = useState("default");
  const [selectedModel, setSelectedModel] = useState("gpt-5.6-luna");
  const [selectedTools, setSelectedTools] = useState("auto");
  const [messages, setMessages] = useState<ChatMessageItem[]>(initialChatMessages);
  const [lastSentId, setLastSentId] = useState<string>();
  // The finished reply, for the thread to read out. It is reset when the next
  // draft starts, so an identical reply is read again.
  const [announcement, setAnnouncement] = useState<string>();
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
    const modelLabel = optionLabel(MODELS, selectedModel);
    const assistantTokens = 140 + Math.floor(Math.random() * 60);
    const assistantCost = `$${(assistantTokens * 0.000012).toFixed(3)}`;
    const newAssistantMsg: ChatMessageItem = {
      id: `m-asst-${Date.now() + 1}`,
      role: "assistant",
      content: `I have processed your request with ${modelLabel} via the ${optionLabel(ROUTES, selectedRoute)} route.`,
      tokens: assistantTokens,
      duration: "1.2 s",
      model: modelLabel,
      cost: assistantCost,
    };
    setMessages((prev) => [...prev, newUserMsg, newAssistantMsg]);
    setLastSentId(newUserMsg.id);
    setAnnouncement(newAssistantMsg.content);
    setDraft("");
  };

  const handleDraftChange = (value: string) => {
    setDraft(value);
    setAnnouncement(undefined);
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

          <ChatThread className="chat-messages" scrollKey={lastSentId} announcement={announcement}>
            {messages.map((msg) => (
              <ChatMessage
                key={msg.id}
                role={msg.role}
                content={msg.content}
                model={msg.model}
                tokens={msg.tokens}
                duration={msg.duration}
                cost={msg.cost}
              />
            ))}
          </ChatThread>

          <div className="chat-composer-wrap">
            <Composer
              value={draft}
              onChange={handleDraftChange}
              onSend={handleSendMessage}
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
