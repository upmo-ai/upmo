import type { ArtifactDiffFile, ArtifactKind, ArtifactRecord, ArtifactsApi } from "./types";

interface StoredArtifact extends ArtifactRecord {
  /** The body of a text file. */
  text?: string;
  /** The body of any other file, as base64. */
  base64?: string;
  /** The changed files of a diff. */
  diff?: ArtifactDiffFile[];
  /** The record is listed but its content is gone from storage, so reading it fails as a 404 would. */
  missing?: boolean;
}

const encoder = new TextEncoder();

function textFile(
  id: string,
  name: string,
  kind: ArtifactKind,
  mime: string,
  created_at: string,
  text: string,
): StoredArtifact {
  return { id, name, kind, mime, size: encoder.encode(text).length, created_at, text };
}

function binaryFile(
  id: string,
  name: string,
  kind: ArtifactKind,
  mime: string,
  created_at: string,
  base64: string,
): StoredArtifact {
  const padding = base64.endsWith("==") ? 2 : base64.endsWith("=") ? 1 : 0;
  const size = (base64.length / 4) * 3 - padding;
  return { id, name, kind, mime, size, created_at, base64 };
}

// A 48 by 24 PNG of four green bands.
const bandsPng =
  "iVBORw0KGgoAAAANSUhEUgAAADAAAAAYCAIAAAAzn+mLAAAAPUlEQVR42mMQqEghiDQWVBBEUft6CKIFd1YRRAyjDhp10KiDRh006qBRB406aNRBow4addCog0YdNLIcBADVBbUbXlqBdgAAAABJRU5ErkJggg==";

// The 22 bytes of an empty ZIP archive.
const emptyZip = "UEsFBgAAAAAAAAAAAAAAAAAAAAAAAA==";

const digestMarkdown = `## October invoices over threshold

Three of 31 invoices are over **€1,000**. Total spend this month is **€18,420**.

- Hetzner: €2,140, due 24 Oct
- Vercel: €1,310, due 28 Oct
- Acme Design: €1,050, due 31 Oct

See the [finance policy](https://example.com/finance-policy) for the threshold.

Draft. Upmo sends it to finance after approval.`;

const invoicesCsv = `Vendor,Amount,Due
Hetzner,"€2,140.00",24 Oct
Vercel,"€1,310.00",28 Oct
Acme Design,"€1,050.00",31 Oct
GitHub,€420.00,1 Nov
Linear,€192.00,3 Nov`;

const digestHtml = `<!doctype html>
<html>
  <body>
    <h1>Finance digest</h1>
    <p>Three invoices are over the threshold.</p>
    <script>document.title = "changed";</script>
  </body>
</html>`;

const reconcileScript = `def over_threshold(invoices, limit=1000):
    """Return the invoices above the limit, largest first."""
    flagged = [item for item in invoices if item["amount"] > limit]
    return sorted(flagged, key=lambda item: item["amount"], reverse=True)
`;

const runNotes = `Run 14
Fetched 31 invoices from the finance mailbox.
Matched 28 to a contract.
3 are over the threshold.`;

const triageBefore = `def route(message):
    if message.sender.startswith("billing@"):
        return "finance"
    return "review"
`;

const triageAfter = `def route(message):
    if message.sender.startswith("billing@"):
        return "finance"
    if message.subject.lower().startswith("receipt"):
        return "receipts"
    return "review"
`;

const invoicesRuleBefore = `match:
  from: billing@
action: forward
to: finance
`;

const invoicesRuleAfter = `match:
  from: billing@
action: forward
to: finance
label: invoice
`;

const receiptsRule = `match:
  subject: receipt
action: file
to: receipts
`;

const legacyRule = `match:
  from: accounts@
action: forward
to: finance
`;

const triageChanges: ArtifactDiffFile[] = [
  { path: "triage.py", before: triageBefore, after: triageAfter },
  { path: "rules/invoices.yaml", before: invoicesRuleBefore, after: invoicesRuleAfter },
  { path: "rules/receipts.yaml", before: null, after: receiptsRule },
  { path: "rules/legacy.yaml", before: legacyRule, after: null },
];

function lines(text: string): string[] {
  return text === "" ? [] : text.replace(/\n$/, "").split("\n");
}

/**
 * A patch for a set of changes, as the download of a diff. Each file is one
 * hunk that removes the old lines and adds the new ones: valid, not minimal.
 */
function patchOf(files: ArtifactDiffFile[]): string {
  return files
    .map((file) => {
      const before = lines(file.before ?? "");
      const after = lines(file.after ?? "");
      return [
        `--- ${file.before === null ? "/dev/null" : `a/${file.path}`}`,
        `+++ ${file.after === null ? "/dev/null" : `b/${file.path}`}`,
        `@@ -${before.length === 0 ? 0 : 1},${before.length} +${after.length === 0 ? 0 : 1},${after.length} @@`,
        ...before.map((line) => `-${line}`),
        ...after.map((line) => `+${line}`),
      ].join("\n");
    })
    .join("\n")
    .concat("\n");
}

function diffFile(
  id: string,
  name: string,
  created_at: string,
  files: ArtifactDiffFile[],
): StoredArtifact {
  const text = patchOf(files);
  return { id, name, kind: "diff", mime: "text/x-diff", size: encoder.encode(text).length, created_at, text, diff: files };
}

const seed: Record<string, StoredArtifact[]> = {
  "aster-digest": [
    textFile("digest-draft", "digest-draft.md", "markdown", "text/markdown", "2026-11-05T08:03:00.000Z", digestMarkdown),
    textFile("invoices-oct", "invoices-oct.csv", "csv", "text/csv", "2026-11-05T08:01:00.000Z", invoicesCsv),
    binaryFile("spend-chart", "spend-chart.png", "image", "image/png", "2026-11-05T08:02:00.000Z", bandsPng),
    textFile("digest-preview", "digest-preview.html", "html", "text/html", "2026-11-05T08:03:30.000Z", digestHtml),
    textFile("over-threshold", "over_threshold.py", "code", "text/x-python", "2026-11-05T08:02:30.000Z", reconcileScript),
    textFile("run-notes", "run-notes.txt", "text", "text/plain", "2026-11-05T08:00:30.000Z", runNotes),
    textFile("digest-pdf", "digest.pdf", "pdf", "application/pdf", "2026-11-05T08:03:40.000Z", "%PDF-1.4\n%%EOF\n"),
    binaryFile("invoice-bundle", "invoice-bundle.zip", "binary", "application/zip", "2026-11-05T08:01:30.000Z", emptyZip),
  ],
  "aster-mail": [
    textFile(
      "triage-notes",
      "triage-notes.md",
      "markdown",
      "text/markdown",
      "2026-11-05T08:16:00.000Z",
      "## Morning Mail Triage\n\n- 24 messages checked\n- 3 invoices forwarded to finance\n- 2 tickets escalated to tier 2",
    ),
    diffFile("triage-changes", "triage-rules.patch", "2026-11-05T08:17:00.000Z", triageChanges),
    {
      id: "rules-export",
      name: "rules-export.json",
      kind: "code",
      mime: "application/json",
      size: 2048,
      created_at: "2026-11-05T08:18:00.000Z",
      missing: true,
    },
  ],
  "aster-done": [
    textFile(
      "receipts-filed",
      "receipts-filed.csv",
      "csv",
      "text/csv",
      "2026-11-05T07:32:00.000Z",
      "Receipt ID,Category,Amount\nREC-001,Travel,€84.50\nREC-002,Software,€49.00\nREC-003,Meals,€32.00",
    ),
  ],
};

function copy<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

function record(item: StoredArtifact): ArtifactRecord {
  return {
    id: item.id,
    name: item.name,
    kind: item.kind,
    mime: item.mime,
    size: item.size,
    created_at: item.created_at,
  };
}

/** The in-memory artifacts. Live mode answers the same calls from the server. */
export function createArtifactsStub(): ArtifactsApi {
  const byTask = copy(seed);
  const all = () => Object.values(byTask).flat();
  const find = (id: string) => all().find((item) => item.id === id);

  return {
    async list(taskId) {
      // The contract lists oldest first.
      return (byTask[taskId] ?? [])
        .map((item) => record(item))
        .sort((a, b) => a.created_at.localeCompare(b.created_at));
    },
    async text(id) {
      const item = find(id);
      if (!item) throw new Error("That artifact is not in the workspace.");
      if (item.missing) throw new Error("That artifact's content is missing.");
      if (item.text === undefined) throw new Error("That artifact is not text.");
      return item.text;
    },
    async diff(id) {
      const item = find(id);
      if (!item) throw new Error("That artifact is not in the workspace.");
      if (item.missing) throw new Error("That artifact's content is missing.");
      if (item.diff === undefined) throw new Error("That artifact is not a diff.");
      return copy({ files: item.diff });
    },
    downloadUrl(id) {
      const item = find(id);
      if (!item || item.missing) return `/api/artifacts/${encodeURIComponent(id)}/download`;
      if (item.base64 !== undefined) return `data:${item.mime};base64,${item.base64}`;
      return `data:${item.mime};charset=utf-8,${encodeURIComponent(item.text ?? "")}`;
    },
  };
}
