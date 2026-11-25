import { BrainError, type ApiClient, type ChatPicks, type ShareUpdate, type TaskShare } from "../../app/api";
import type { Chat, ChatLine, ChatSummary } from "./types";

// How long the stub takes to answer. A real model takes longer.
export const defaultReplyDelayMs = 600;

const newChatTitle = "New chat";
const titleLength = 40;
const previewLength = 80;
const stubModel = "claude-3-7-sonnet";
const localModel = "llama3.3:70b";

interface StoredChat {
  id: string;
  title: string;
  createdAt: string;
  lines: ChatLine[];
  privateRead?: boolean;
  share?: TaskShare;
}

/** JSON round-trip so a caller cannot change the stored record. */
function copy<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function clip(text: string, length: number) {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > length ? `${flat.slice(0, length - 3).trimEnd()}...` : flat;
}

function estimateTokens(text: string) {
  return Math.max(6, Math.round(text.length / 4));
}

function startOfDay(value: Date) {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate());
}

/** A fixed hour on a day some calendar days back, so the list groups the same way at any time. */
function daysAgo(now: Date, days: number, hour: number, minute: number) {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate() - days, hour, minute).toISOString();
}

/** Minutes back from now, but never before today began. */
function minutesAgo(now: Date, minutes: number) {
  const earliest = startOfDay(now).getTime();
  return new Date(Math.max(now.getTime() - minutes * 60_000, earliest)).toISOString();
}

function fixtures(now: Date): StoredChat[] {
  const roadmap = minutesAgo(now, 20);
  const research = minutesAgo(now, 45);
  const update = daysAgo(now, 1, 16, 30);
  const supplier = daysAgo(now, 8, 11, 5);
  return [
    {
      id: "roadmap-priorities",
      title: "Roadmap priorities",
      createdAt: roadmap,
      lines: [
        {
          id: "roadmap-1",
          role: "user",
          body: "Summarize the launch notes and highlight anything that needs a decision.",
          model: null,
          tokens: 18,
          cost_usd: 0.0002,
          at: roadmap,
        },
        {
          id: "roadmap-2",
          role: "assistant",
          body: "The notes point to three priorities: finish the onboarding copy, confirm the support rota and agree the release date. The release date is the only open decision.",
          model: stubModel,
          tokens: 184,
          cost_usd: 0.002,
          at: roadmap,
        },
      ],
    },
    {
      id: "research-summary",
      title: "Research summary",
      createdAt: research,
      lines: [
        {
          id: "research-1",
          role: "user",
          body: "What did the interviews tell us about onboarding?",
          model: null,
          tokens: 12,
          cost_usd: 0.00012,
          at: research,
        },
        {
          id: "research-2",
          role: "assistant",
          body: "Key findings: people stall at the first connector, and most want a sample task before they link anything. Next steps: add a sample task and shorten the connector form.",
          model: stubModel,
          tokens: 97,
          cost_usd: 0.0012,
          at: research,
        },
      ],
    },
    {
      id: "team-update",
      title: "Team update draft",
      createdAt: update,
      lines: [
        {
          id: "update-1",
          role: "user",
          body: "Draft Friday's team update from the sprint notes.",
          model: null,
          tokens: 14,
          cost_usd: 0.00014,
          at: update,
        },
        {
          id: "update-2",
          role: "assistant",
          body: "Here is a draft: the onboarding copy shipped, support cover is set for the holiday week, and the release date is the one open question for Monday.",
          model: stubModel,
          tokens: 76,
          cost_usd: 0.0009,
          at: update,
        },
      ],
    },
    {
      id: "supplier-review",
      title: "Supplier review",
      createdAt: supplier,
      lines: [
        {
          id: "supplier-1",
          role: "user",
          body: "Which questions should we prioritize before the supplier meeting?",
          model: null,
          tokens: 16,
          cost_usd: null,
          at: supplier,
        },
        {
          id: "supplier-2",
          role: "assistant",
          body: "Start with price stability over the next year, then delivery lead times, then what happens when a shipment is late.",
          model: localModel,
          tokens: 61,
          // A local model has no price list, so the cost is not known.
          cost_usd: null,
          at: supplier,
        },
      ],
    },
  ];
}

function summarize(chat: StoredChat): ChatSummary {
  const last = chat.lines.at(-1);
  return {
    id: chat.id,
    title: chat.title,
    preview: last ? clip(last.body, previewLength) : "",
    last_activity: last?.at ?? chat.createdAt,
  };
}

function replyTo(body: string) {
  return `A stubbed reply to: "${clip(body, 60)}". The real answer arrives here once the brain serves chats.`;
}

/**
 * The chat half of the stub API. It lives here so the shared stub file stays
 * small. The types it returns are the contract for GET /api/chats,
 * GET /api/chats/{id}, POST /api/chats and POST /api/chats/{id}.
 *
 * send resolves as soon as the line is stored, like the 202 it stands for. The
 * assistant line is written later, so a caller reads the chat again to see it.
 */
export function createChatsStub(
  replyDelayMs: number = defaultReplyDelayMs,
  clock: () => Date = () => new Date(),
): ApiClient["chats"] {
  const chats = fixtures(clock());
  let serial = 0;

  function find(id: string) {
    const chat = chats.find((item) => item.id === id);
    if (!chat) throw new Error("Chat missing.");
    return chat;
  }

  function newLine(role: ChatLine["role"], body: string, tokens: number, cost: number): ChatLine {
    serial += 1;
    return {
      id: `line-${serial}`,
      role,
      body,
      model: role === "assistant" ? stubModel : null,
      tokens,
      cost_usd: cost,
      at: clock().toISOString(),
    };
  }

  function reply(chat: StoredChat, body: string) {
    const text = replyTo(body);
    const tokens = estimateTokens(text);
    chat.lines.push(newLine("assistant", text, tokens, Math.round(tokens * 12) / 1_000_000));
  }

  return {
    // The stub writes an assistant line on its own, so a send is worth waiting for.
    repliesLater: true,

    async list(query) {
      const needle = query?.trim().toLocaleLowerCase("en-GB") ?? "";
      return chats
        .filter((chat) => {
          if (!needle) return true;
          if (chat.title.toLocaleLowerCase("en-GB").includes(needle)) return true;
          return chat.lines.some((line) => line.body.toLocaleLowerCase("en-GB").includes(needle));
        })
        .map(summarize)
        .sort((a, b) => Date.parse(b.last_activity) - Date.parse(a.last_activity))
        .map((item) => copy(item));
    },
    async create() {
      serial += 1;
      const chat: StoredChat = {
        id: `chat-${serial}`,
        title: newChatTitle,
        createdAt: clock().toISOString(),
        lines: [],
      };
      chats.push(chat);
      return copy(summarize(chat));
    },
    async get(id): Promise<Chat> {
      return copy({ id, lines: find(id).lines, can_edit: true, can_delete: true });
    },
    async send(id, body, picks) {
      const chat = find(id);
      const text = body.trim();
      if (!text) throw new Error("Write a message.");
      if (chat.lines.at(-1)?.role === "user") throw new Error("Wait for the reply.");
      const tokens = estimateTokens(text);
      const userLine = newLine("user", text, tokens, Math.round(tokens * 3) / 1_000_000);
      if (picks?.files && picks.files.length > 0) {
        userLine.attachments = picks.files.map((file) => ({
          name: file.name,
          mime: file.type || "application/octet-stream",
          size_bytes: file.size,
        }));
      }
      chat.lines.push(userLine);
      if (chat.title === newChatTitle) chat.title = clip(text, titleLength);
      setTimeout(() => reply(chat, text), replyDelayMs);
    },
    async retry() {
      throw new Error("The sample chats have no failed replies.");
    },
    async stop(id: string) {
      const chat = find(id);
      const last = chat.lines.at(-1);
      if (last?.role === "assistant" && (last.status === "pending" || last.status === undefined)) {
        last.status = "stopped";
        return;
      }
      const line = newLine("assistant", "", 0, 0);
      line.status = "stopped";
      chat.lines.push(line);
    },
    async branch(id: string, upToLineId: string, opts?: { retry?: boolean; picks?: ChatPicks }) {
      const source = find(id);
      let index = source.lines.findIndex((line) => line.id === upToLineId);
      if (index < 0) throw new Error("Line missing.");
      const chosen = source.lines[index];
      if (opts?.retry && chosen?.role === "assistant" && chosen.reply_to) {
        const userIndex = source.lines.findIndex((line) => line.id === chosen.reply_to);
        if (userIndex >= 0) index = userIndex;
      }
      serial += 1;
      const chat: StoredChat = {
        id: `chat-${serial}`,
        title: source.title,
        createdAt: clock().toISOString(),
        lines: source.lines.slice(0, index + 1).map((line) => ({ ...line, id: `${line.id}-b${serial}` })),
      };
      chats.unshift(chat);
      return copy(summarize(chat));
    },
    async getShare(id): Promise<TaskShare> {
      const chat = find(id);
      const share: TaskShare = chat.share ?? {
        visibility: "private",
        private_read: Boolean(chat.privateRead),
        members: [{ subject_type: "person", subject_id: "sam", name: "Sam Rivera", role: "owner" }],
      };
      return copy(share);
    },
    async putShare(id, update: ShareUpdate): Promise<TaskShare> {
      const chat = find(id);
      const last = chat.lines.at(-1);
      if (last?.role === "user" || last?.status === "pending") {
        throw new BrainError(409, "a reply is already on its way");
      }
      const next: TaskShare = {
        visibility: update.visibility,
        ...(update.visibility === "team" && update.team_id ? { team_id: update.team_id } : {}),
        private_read: Boolean(chat.privateRead),
        members: update.members.map((member) => ({
          ...member,
          name: member.subject_id === "sam" ? "Sam Rivera" : member.subject_id,
        })),
      };
      chat.share = next;
      return copy(next);
    },
    async rename(id, title) {
      const chat = find(id);
      chat.title = title.trim();
      return copy(summarize(chat));
    },
    async remove(id) {
      const index = chats.findIndex((item) => item.id === id);
      if (index < 0) throw new Error("Chat missing.");
      chats.splice(index, 1);
    },
  };
}
