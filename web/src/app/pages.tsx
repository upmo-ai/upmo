import {
  ActionIcon,
  Badge,
  Button,
  Group,
  Stack,
  Text,
  Textarea,
  TextInput,
  Title,
  Tooltip,
} from "@mantine/core";
import { IconActivity, IconSearch } from "@tabler/icons-react";
import { useState } from "react";

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

export function ChatPage() {
  const [activeChatId, setActiveChatId] = useState<string>(sampleChats[0].id);
  const [listOpen, setListOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState("");
  const activeChat = sampleChats.find((chat) => chat.id === activeChatId) ?? sampleChats[0];
  const matchingChats = sampleChats.filter((chat) =>
    chat.title.toLocaleLowerCase("en-GB").includes(query.trim().toLocaleLowerCase("en-GB")),
  );

  return (
    <section className="chat-page">
      <header className="chat-page-header">
        <Title order={1}>Chat</Title>
        <Button
          className="chat-list-toggle"
          variant="default"
          leftSection={<ShellIcon concept="nav.chats" />}
          aria-expanded={listOpen}
          aria-controls="chat-list"
          onClick={() => setListOpen((open) => !open)}
        >
          Chats
        </Button>
      </header>

      <div className="chat-workspace">
        <nav
          id="chat-list"
          className="chat-list"
          aria-label="Chat list"
          data-open={listOpen}
        >
          <Group gap="xs" wrap="nowrap">
            <TextInput
              className="chat-search"
              aria-label="Search chats"
              placeholder="Search chats"
              leftSection={<IconSearch size={20} stroke={1.75} aria-hidden />}
              value={query}
              onChange={(event) => setQuery(event.currentTarget.value)}
            />
            <Tooltip label="New chat" position="bottom" openDelay={400}>
              <ActionIcon
                aria-label="New chat"
                variant="filled"
                size={40}
                onClick={() => setDraft("")}
              >
                <ShellIcon concept="action.add" />
              </ActionIcon>
            </Tooltip>
          </Group>

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
                            onClick={() => {
                              setActiveChatId(chat.id);
                              setListOpen(false);
                            }}
                          >
                            <span className="chat-list-item-row">
                              <span className="chat-list-item-title">{chat.title}</span>
                              <span className="chat-list-item-time">{chat.time}</span>
                            </span>
                            <span className="chat-list-item-row">
                              <span className="chat-list-item-preview">{chat.preview}</span>
                              {chat.archived ? (
                                <Badge size="xs" variant="light">
                                  Archived
                                </Badge>
                              ) : null}
                            </span>
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
          <div className="chat-thread-title">
            <Title order={2} id="current-chat-title" size="h3">
              {activeChat.title}
            </Title>
          </div>

          <Stack className="chat-messages" gap="lg" aria-label="Messages">
            <div className="chat-message chat-message-user">
              <Text>
                Summarise the launch notes and highlight anything that needs a decision.
              </Text>
            </div>
            <div className="chat-message chat-message-assistant">
              <span className="chat-avatar" aria-hidden>
                <CaretIcon size={20} />
              </span>
              <div className="chat-reply">
                <Text>
                  The notes point to three priorities: finish the onboarding copy, confirm the
                  support rota and agree the release date. The release date is the only open
                  decision.
                </Text>
                <div className="chat-message-meta">
                  <span>Upmo</span>
                  <span>·</span>
                  <span>184 tokens</span>
                  <span>·</span>
                  <span>1.4 s</span>
                  <Tooltip label="Copy reply" position="bottom" openDelay={400}>
                    <ActionIcon
                      aria-label="Copy reply"
                      variant="subtle"
                      size={28}
                      onClick={() => {
                        void navigator.clipboard?.writeText(
                          "The notes point to three priorities: finish the onboarding copy, confirm the support rota and agree the release date. The release date is the only open decision.",
                        );
                      }}
                    >
                      <ShellIcon concept="action.copy" size={16} />
                    </ActionIcon>
                  </Tooltip>
                  <Tooltip label="Activity" position="bottom" openDelay={400}>
                    <ActionIcon aria-label="Activity" variant="subtle" size={28}>
                      <IconActivity size={16} stroke={1.75} aria-hidden="true" />
                    </ActionIcon>
                  </Tooltip>
                </div>
              </div>
            </div>
          </Stack>

          <div className="chat-composer-wrap">
            <form
              className="chat-composer"
              aria-label="Chat composer"
              onSubmit={(event) => {
                event.preventDefault();
                if (draft.trim() !== "") setDraft("");
              }}
            >
              <Textarea
                className="chat-composer-input"
                aria-label="Message"
                placeholder="Message"
                rows={2}
                value={draft}
                onChange={(event) => setDraft(event.currentTarget.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" && !event.shiftKey) {
                    event.preventDefault();
                    if (draft.trim() !== "") setDraft("");
                  }
                }}
              />
              <Tooltip label="Send" position="top" openDelay={400}>
                <ActionIcon
                  aria-label="Send"
                  type="submit"
                  size={40}
                  disabled={draft.trim() === ""}
                >
                  <ShellIcon concept="action.send" size={20} />
                </ActionIcon>
              </Tooltip>
            </form>
            <div className="chat-composer-hint">
              <kbd className="chat-kbd">Enter</kbd> to send · <kbd className="chat-kbd">Shift</kbd> <kbd className="chat-kbd">Enter</kbd> for a new line
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
