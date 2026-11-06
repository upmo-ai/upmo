import {
  fileProblem,
  type KnowledgeApi,
  type KnowledgeFolder,
  type KnowledgeHit,
  type KnowledgeSource,
} from "./types";

/** How long a URL or file stays pending in the stub before it settles. */
export const DEFAULT_INGEST_MS = 1500;

/** The most hits a search returns, as on the server. */
const SEARCH_LIMIT = 8;

interface StoredSource extends KnowledgeSource {
  body: string;
}

interface StoredFolder {
  id: string;
  name: string;
  created_at: string;
}

const folderSeed: StoredFolder[] = [
  { id: "folder-product", name: "Product", created_at: "2026-08-30T09:00:00.000Z" },
  { id: "folder-policies", name: "Policies", created_at: "2026-08-30T09:05:00.000Z" },
  { id: "folder-finance", name: "Finance", created_at: "2026-09-28T09:00:00.000Z" },
  { id: "folder-research", name: "Research", created_at: "2026-10-13T09:00:00.000Z" },
  { id: "folder-legal", name: "Legal", created_at: "2026-10-11T09:00:00.000Z" },
  { id: "folder-engineering", name: "Engineering", created_at: "2026-08-30T09:10:00.000Z" },
];

function source(
  id: string,
  title: string,
  kind: StoredSource["kind"],
  updated_at: string,
  body: string,
  extra: Partial<StoredSource> = {},
): StoredSource {
  return {
    id,
    title,
    kind,
    source_ref: kind === "file" ? title : "",
    provenance: "curated",
    status: "ready",
    revision: 1,
    updated_at,
    body,
    ...extra,
  };
}

const sourceSeed: Record<string, StoredSource[]> = {
  "folder-product": [
    source(
      "src-handbook",
      "Handbook.pdf",
      "file",
      "2026-08-30T10:00:00.000Z",
      "Every new starter gets a laptop on day one.\n\nHoliday requests go to your lead at least two weeks ahead.\n\nExpenses over 100 euros need a receipt and a second approval.",
    ),
    source(
      "src-pricing",
      "Pricing.md",
      "markdown",
      "2026-08-30T11:00:00.000Z",
      "The Team plan is 12 euros per seat per month.\n\nThe Business plan adds single sign-on and audit export for 24 euros per seat.\n\nNon-profits get 30 percent off any plan.",
      { revision: 2 },
    ),
  ],
  "folder-policies": [
    source(
      "src-access",
      "Access.md",
      "markdown",
      "2026-08-30T12:00:00.000Z",
      "Owners can change roles and billing.\n\nMembers can create tasks and chats.\n\nViewers can read what is shared with them and nothing else.",
    ),
  ],
  "folder-finance": [
    source(
      "src-close",
      "Close.md",
      "markdown",
      "2026-09-28T10:00:00.000Z",
      "The month closes on the third working day.\n\nAccruals are posted before the ledger is locked.",
    ),
    source(
      "src-ledger",
      "Ledger.md",
      "markdown",
      "2026-09-28T11:00:00.000Z",
      "Hetzner invoices arrive on the 20th of each month.\n\nInvoices over 1,000 euros are flagged for finance.",
    ),
  ],
  "folder-research": [
    source(
      "src-notes",
      "Notes.md",
      "markdown",
      "2026-10-13T10:00:00.000Z",
      "Interview notes from the October round.\n\nPeople ask for fewer clicks between a chat and the task it starts.",
    ),
    source(
      "src-market",
      "Market scan",
      "url",
      "2026-10-14T08:00:00.000Z",
      "",
      {
        source_ref: "https://example.com/market-scan",
        provenance: "web",
        status: "failed",
        revision: 0,
        error: "fetch failed",
      },
    ),
  ],
  "folder-legal": [
    source(
      "src-terms",
      "Terms.md",
      "markdown",
      "2026-10-11T10:00:00.000Z",
      "The terms apply to everyone who signs in to a workspace.\n\nMaterial you add belongs to your organisation.",
    ),
    source(
      "src-privacy",
      "Privacy.md",
      "markdown",
      "2026-10-11T11:00:00.000Z",
      "The workspace keeps the tasks, chat and files you add.\n\nYou can ask for any of it to be deleted.",
    ),
  ],
  "folder-engineering": [
    source(
      "src-pipelines",
      "Pipelines.md",
      "markdown",
      "2026-08-30T13:00:00.000Z",
      "Every change goes through the same pipeline: lint, test, build.\n\nA green pipeline is the only way into main.",
    ),
    source(
      "src-runtime",
      "Runtime.md",
      "url",
      "2026-08-30T14:00:00.000Z",
      "The runtime runs each task in its own sandbox.\n\nSandboxes have no network unless a connector grants it.",
      { source_ref: "https://docs.example.com/runtime", provenance: "web" },
    ),
  ],
};

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function publicSource(stored: StoredSource): KnowledgeSource {
  return clone<KnowledgeSource>({
    id: stored.id,
    title: stored.title,
    kind: stored.kind,
    source_ref: stored.source_ref,
    provenance: stored.provenance,
    status: stored.status,
    revision: stored.revision,
    updated_at: stored.updated_at,
    ...(stored.error ? { error: stored.error } : {}),
  });
}

function hostOf(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

function withoutExtension(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(0, dot) : name;
}

function words(text: string): string[] {
  return text
    .toLowerCase()
    .split(/[^\p{L}\p{N}]+/u)
    .filter((word) => word !== "");
}

/**
 * The in-memory knowledge API. URL and file sources are accepted as pending and
 * settle after ingestMs. An address with "unreachable" in it, an address on a
 * private network and a file with "corrupt" in its name settle as failed, every
 * time. Any other failure (the seeded one) clears when it is retried.
 */
export function createKnowledgeStub(options: { ingestMs?: number } = {}): KnowledgeApi {
  const ingestMs = options.ingestMs ?? DEFAULT_INGEST_MS;
  const folders = clone(folderSeed);
  const sources = clone(sourceSeed);
  const doomed = new Map<string, string>();
  let serial = 0;

  function nextId(prefix: string) {
    serial += 1;
    return `${prefix}-${serial}`;
  }

  function folderOf(id: string): StoredFolder {
    const folder = folders.find((item) => item.id === id);
    if (!folder) throw new Error("That folder is not in the workspace.");
    return folder;
  }

  function sourcesOf(folderId: string): StoredSource[] {
    folderOf(folderId);
    sources[folderId] ??= [];
    return sources[folderId];
  }

  function sourceOf(folderId: string, sourceId: string): StoredSource {
    const found = sourcesOf(folderId).find((item) => item.id === sourceId);
    if (!found) throw new Error("That source is not in this folder.");
    return found;
  }

  function presentFolder(folder: StoredFolder): KnowledgeFolder {
    const list = sources[folder.id] ?? [];
    const stamps = [folder.created_at, ...list.map((item) => item.updated_at)];
    return {
      id: folder.id,
      name: folder.name,
      source_count: list.length,
      updated_at: stamps.reduce((latest, stamp) => (stamp > latest ? stamp : latest)),
    };
  }

  function cleanName(name: string): string {
    const trimmed = name.trim();
    if (!trimmed) throw new Error("Name the folder.");
    return trimmed;
  }

  function settle(folderId: string, sourceId: string) {
    setTimeout(() => {
      const found = sources[folderId]?.find((item) => item.id === sourceId);
      if (!found || found.status !== "pending") return;
      const reason = doomed.get(sourceId);
      found.updated_at = new Date().toISOString();
      if (reason) {
        found.status = "failed";
        found.error = reason;
        return;
      }
      found.status = "ready";
      found.revision += 1;
      delete found.error;
      found.body =
        found.body ||
        (found.kind === "url"
          ? `Content fetched from ${found.source_ref}.`
          : `Text extracted from ${found.source_ref}.`);
    }, ingestMs);
  }

  function addPending(
    folderId: string,
    created: Pick<StoredSource, "title" | "kind" | "source_ref" | "provenance">,
    failure: string | null,
  ): KnowledgeSource {
    const list = sourcesOf(folderId);
    const stored: StoredSource = {
      id: nextId("source"),
      ...created,
      status: "pending",
      revision: 0,
      updated_at: new Date().toISOString(),
      body: "",
    };
    list.unshift(stored);
    if (failure) doomed.set(stored.id, failure);
    settle(folderId, stored.id);
    return publicSource(stored);
  }

  return {
    folders: {
      async list() {
        return folders.map(presentFolder);
      },
      async create(name) {
        const folder: StoredFolder = {
          id: nextId("folder"),
          name: cleanName(name),
          created_at: new Date().toISOString(),
        };
        folders.unshift(folder);
        sources[folder.id] = [];
        return presentFolder(folder);
      },
      async rename(id, name) {
        const folder = folderOf(id);
        folder.name = cleanName(name);
        return presentFolder(folder);
      },
      async remove(id) {
        const folder = folderOf(id);
        folders.splice(folders.indexOf(folder), 1);
        delete sources[id];
      },
    },
    sources: {
      async list(folderId) {
        return sourcesOf(folderId).map(publicSource);
      },
      async addMarkdown(folderId, input) {
        const list = sourcesOf(folderId);
        const title = input.title.trim();
        if (!title) throw new Error("Give the source a title.");
        if (!input.body.trim()) throw new Error("Paste some Markdown.");
        const stored: StoredSource = {
          id: nextId("source"),
          title,
          kind: "markdown",
          source_ref: "",
          provenance: "curated",
          status: "ready",
          revision: 1,
          updated_at: new Date().toISOString(),
          body: input.body,
        };
        list.unshift(stored);
        return publicSource(stored);
      },
      async addUrl(folderId, input) {
        const url = input.url.trim();
        let parsed: URL | null = null;
        try {
          parsed = new URL(url);
        } catch {
          parsed = null;
        }
        if (!parsed || (parsed.protocol !== "http:" && parsed.protocol !== "https:")) {
          throw new Error("Enter a web address that starts with http:// or https://.");
        }
        let failure: string | null = null;
        if (/^(localhost|127\.|10\.|192\.168\.)/.test(parsed.hostname)) failure = "url blocked";
        else if (url.includes("unreachable")) failure = "fetch failed";
        return addPending(
          folderId,
          {
            title: input.title?.trim() || hostOf(url),
            kind: "url",
            source_ref: url,
            provenance: "web",
          },
          failure,
        );
      },
      async addFile(folderId, input) {
        const problem = fileProblem(input.file);
        if (problem) throw new Error(problem);
        return addPending(
          folderId,
          {
            title: input.title?.trim() || withoutExtension(input.file.name),
            kind: "file",
            source_ref: input.file.name,
            provenance: "curated",
          },
          input.file.name.includes("corrupt") ? "conversion failed" : null,
        );
      },
      async reingest(folderId, sourceId) {
        const found = sourceOf(folderId, sourceId);
        if (found.status === "pending") throw new Error("That source is still being added.");
        if (found.kind === "markdown" || found.kind === "clip") {
          found.revision += 1;
          found.status = "ready";
          found.updated_at = new Date().toISOString();
          delete found.error;
          return publicSource(found);
        }
        found.status = "pending";
        found.updated_at = new Date().toISOString();
        delete found.error;
        settle(folderId, sourceId);
        return publicSource(found);
      },
      async remove(folderId, sourceId) {
        const list = sourcesOf(folderId);
        list.splice(list.indexOf(sourceOf(folderId, sourceId)), 1);
        doomed.delete(sourceId);
      },
    },
    async search(folderId, query) {
      const wanted = words(query);
      if (wanted.length === 0) throw new Error("Enter something to search for.");
      const hits: KnowledgeHit[] = [];
      for (const found of sourcesOf(folderId)) {
        if (found.status !== "ready") continue;
        found.body.split(/\n\s*\n/).forEach((chunk, seq) => {
          const have = new Set(words(chunk));
          if (wanted.every((word) => have.has(word))) {
            hits.push({ source_id: found.id, seq, body: chunk.trim() });
          }
        });
      }
      return hits.slice(0, SEARCH_LIMIT);
    },
  };
}
