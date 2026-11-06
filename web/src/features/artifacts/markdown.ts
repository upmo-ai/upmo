/**
 * A small Markdown reader that makes a tree, never HTML. The tree has no node
 * for raw HTML, scripts, event handlers or images, so whatever a file holds,
 * the page can only show it as text. Markup such as <script> reads as the
 * characters it is made of. A link keeps its address only when it is http,
 * https or mailto.
 */

export type Inline =
  | { type: "text"; value: string }
  | { type: "code"; value: string }
  | { type: "strong"; children: Inline[] }
  | { type: "em"; children: Inline[] }
  | { type: "link"; href: string; children: Inline[] };

export interface ListItem {
  children: Inline[];
  /** Paragraphs, code and lists nested under the item. */
  nested: Block[];
}

export type Block =
  | { type: "heading"; level: 1 | 2 | 3 | 4 | 5 | 6; children: Inline[] }
  | { type: "paragraph"; children: Inline[] }
  | { type: "code"; language: string; value: string }
  | { type: "quote"; children: Block[] }
  | { type: "list"; ordered: boolean; items: ListItem[] }
  | { type: "rule" }
  | { type: "table"; header: Inline[][]; rows: Inline[][][] };

/** How far past an opening mark the reader looks for the one that closes it. */
const WINDOW = 500;
const MAX_DEPTH = 6;

/** The only addresses a link may keep. Anything else is shown as plain text. */
export function safeHref(url: string): string | null {
  // Control characters and spaces are dropped first, so "java\tscript:" is not let through.
  const cleaned = url.replace(/[\p{Cc}\s]+/gu, "");
  if (cleaned.length === 0 || cleaned.length > 2048) return null;
  return /^(https?:\/\/|mailto:)/i.test(cleaned) ? cleaned : null;
}

function closeIndex(source: string, from: number, token: string): number {
  const at = source.slice(from, from + WINDOW).indexOf(token);
  return at < 0 ? -1 : from + at;
}

const escapable = "\\`*_{}[]()#+-.!|>~<";

function isSpace(char: string | undefined) {
  return char === undefined || /\s/.test(char);
}

function isWordChar(char: string | undefined) {
  return char !== undefined && /[\p{L}\p{N}]/u.test(char);
}

interface LinkMatch {
  label: string;
  url: string;
  end: number;
}

/** Reads [label](address) that starts at the "[" at `start`. */
function readLink(source: string, start: number): LinkMatch | null {
  const labelEnd = closeIndex(source, start + 1, "]");
  if (labelEnd < 0 || source.charAt(labelEnd + 1) !== "(") return null;
  // The address may hold balanced parentheses, as in https://example.com/a_(b).
  let urlEnd = -1;
  let open = 0;
  const stop = Math.min(source.length, labelEnd + 2 + WINDOW);
  for (let at = labelEnd + 2; at < stop; at += 1) {
    const char = source.charAt(at);
    if (char === "(") open += 1;
    else if (char === ")") {
      if (open === 0) {
        urlEnd = at;
        break;
      }
      open -= 1;
    }
  }
  if (urlEnd < 0) return null;
  const url = source.slice(labelEnd + 2, urlEnd).trim().split(/\s+/)[0] ?? "";
  return { label: source.slice(start + 1, labelEnd), url, end: urlEnd + 1 };
}

export function parseInline(source: string, depth = 0, inLink = false): Inline[] {
  const out: Inline[] = [];
  let buffer = "";
  const flush = () => {
    if (buffer !== "") out.push({ type: "text", value: buffer });
    buffer = "";
  };

  let i = 0;
  while (i < source.length) {
    const char = source.charAt(i);
    const next = source.charAt(i + 1);

    if (char === "\\" && next !== "" && escapable.includes(next)) {
      buffer += next;
      i += 2;
      continue;
    }

    if (char === "`") {
      let run = 1;
      while (source.charAt(i + run) === "`") run += 1;
      const ticks = "`".repeat(run);
      const end = closeIndex(source, i + run, ticks);
      if (end > i + run) {
        let value = source.slice(i + run, end);
        if (value.startsWith(" ") && value.endsWith(" ") && value.trim() !== "") value = value.slice(1, -1);
        flush();
        out.push({ type: "code", value });
        i = end + run;
        continue;
      }
      buffer += ticks;
      i += run;
      continue;
    }

    // An image is not loaded: a remote address would tell its host who is reading. Its alt text shows.
    if (char === "!" && next === "[") {
      const image = readLink(source, i + 1);
      if (image) {
        buffer += image.label;
        i = image.end;
        continue;
      }
    }

    if (char === "[" && !inLink) {
      const link = readLink(source, i);
      if (link) {
        const href = safeHref(link.url);
        const children = parseInline(link.label, depth + 1, true);
        flush();
        if (href === null) out.push(...children);
        else out.push({ type: "link", href, children });
        i = link.end;
        continue;
      }
    }

    if ((char === "*" || char === "_") && depth < MAX_DEPTH) {
      const token = next === char ? char + char : char;
      const inner = i + token.length;
      const opens =
        !isSpace(source.charAt(inner)) && (char === "*" || !isWordChar(source[i - 1]));
      if (opens) {
        // One bounded slice per opener keeps a long run of unmatched marks from costing a scan each.
        const scope = source.slice(inner + 1, inner + WINDOW);
        let offset = 0;
        let end = -1;
        while (offset < scope.length) {
          const at = scope.indexOf(token, offset);
          if (at < 0) break;
          const found = inner + 1 + at;
          const before = source.charAt(found - 1);
          const after = source.charAt(found + token.length);
          const single = token.length === 1;
          const closes =
            !isSpace(before) &&
            (char === "*" || !isWordChar(after)) &&
            (!single || (after !== char && before !== char));
          if (closes) {
            end = found;
            break;
          }
          offset = at + 1;
        }
        if (end > inner) {
          flush();
          const children = parseInline(source.slice(inner, end), depth + 1, inLink);
          out.push({ type: token.length === 2 ? "strong" : "em", children });
          i = end + token.length;
          continue;
        }
      }
    }

    buffer += char;
    i += 1;
  }
  flush();
  return out;
}

const fenceOpen = /^ {0,3}(`{3,}|~{3,})\s*([\w+#.-]*)/;
const headingLine = /^ {0,3}(#{1,6})\s+(.*)$/;
const ruleLine = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const quoteLine = /^ {0,3}>/;
const itemLine = /^( *)([-*+]|\d{1,9}[.)])\s+(.*)$/;
const tableRule = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

function indentOf(line: string) {
  return line.length - line.trimStart().length;
}

/** A line of only the fence character, at least as long as the opening run. */
function closesFence(line: string, mark: string) {
  const text = line.trim();
  return text.length >= mark.length && text === mark.charAt(0).repeat(text.length);
}

function startsBlock(line: string) {
  return (
    fenceOpen.test(line) ||
    headingLine.test(line) ||
    ruleLine.test(line) ||
    quoteLine.test(line) ||
    itemLine.test(line)
  );
}

function splitRow(line: string): string[] {
  let text = line.trim();
  if (text.startsWith("|")) text = text.slice(1);
  if (text.endsWith("|") && !text.endsWith("\\|")) text = text.slice(0, -1);
  return text.split(/(?<!\\)\|/).map((cell) => cell.trim().replace(/\\\|/g, "|"));
}

function plain(lines: string[]): Block[] {
  const text = lines.join(" ").trim();
  return text === "" ? [] : [{ type: "paragraph", children: [{ type: "text", value: text }] }];
}

function parseBlocks(lines: string[], depth: number): Block[] {
  // Deep nesting reads as one paragraph of text, so a hostile file cannot run the reader out of stack.
  if (depth > MAX_DEPTH) return plain(lines);
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i] ?? "";
    if (line.trim() === "") {
      i += 1;
      continue;
    }

    const fence = fenceOpen.exec(line);
    if (fence) {
      const mark = fence[1] ?? "```";
      const body: string[] = [];
      i += 1;
      while (i < lines.length) {
        const current = lines[i] ?? "";
        if (closesFence(current, mark)) {
          i += 1;
          break;
        }
        body.push(current);
        i += 1;
      }
      blocks.push({ type: "code", language: fence[2] ?? "", value: body.join("\n") });
      continue;
    }

    const heading = headingLine.exec(line);
    if (heading) {
      const level = (heading[1] ?? "#").length as 1 | 2 | 3 | 4 | 5 | 6;
      // A closing run of # after a space is decoration, as in "## Title ##".
      const title = (heading[2] ?? "").trimEnd().replace(/\s#+$/, "").trim();
      blocks.push({ type: "heading", level, children: parseInline(title) });
      i += 1;
      continue;
    }

    if (ruleLine.test(line)) {
      blocks.push({ type: "rule" });
      i += 1;
      continue;
    }

    if (quoteLine.test(line)) {
      const quoted: string[] = [];
      while (i < lines.length && quoteLine.test(lines[i] ?? "")) {
        quoted.push((lines[i] ?? "").replace(/^ {0,3}> ?/, ""));
        i += 1;
      }
      blocks.push({ type: "quote", children: parseBlocks(quoted, depth + 1) });
      continue;
    }

    const first = itemLine.exec(line);
    if (first) {
      const base = (first[1] ?? "").length;
      const ordered = /\d/.test(first[2] ?? "");
      const items: ListItem[] = [];
      while (i < lines.length) {
        const head = itemLine.exec(lines[i] ?? "");
        if (!head || (head[1] ?? "").length > base + 1 || /\d/.test(head[2] ?? "") !== ordered) break;
        const nested: string[] = [];
        i += 1;
        while (i < lines.length) {
          const current = lines[i] ?? "";
          if (current.trim() === "") {
            // A blank line belongs to the item only when indented text follows it.
            if (indentOf(lines[i + 1] ?? "") > base + 1) {
              nested.push("");
              i += 1;
              continue;
            }
            break;
          }
          if (indentOf(current) <= base + 1) break;
          nested.push(current);
          i += 1;
        }
        const strip = nested.filter((l) => l.trim() !== "").reduce((least, l) => Math.min(least, indentOf(l)), Infinity);
        const dedented = nested.map((l) => (l.trim() === "" ? "" : l.slice(Number.isFinite(strip) ? strip : 0)));
        items.push({ children: parseInline(head[3] ?? ""), nested: parseBlocks(dedented, depth + 1) });
        // Blank lines between items keep the list going when another item follows.
        let probe = i;
        while (probe < lines.length && (lines[probe] ?? "").trim() === "") probe += 1;
        if (probe > i && itemLine.test(lines[probe] ?? "")) i = probe;
      }
      blocks.push({ type: "list", ordered, items });
      continue;
    }

    if (line.includes("|") && tableRule.test(lines[i + 1] ?? "") && (lines[i + 1] ?? "").includes("|")) {
      const header = splitRow(line);
      const rows: Inline[][][] = [];
      i += 2;
      while (i < lines.length && (lines[i] ?? "").trim() !== "" && (lines[i] ?? "").includes("|")) {
        const cells = splitRow(lines[i] ?? "");
        rows.push(header.map((_, index) => parseInline(cells[index] ?? "")));
        i += 1;
      }
      blocks.push({ type: "table", header: header.map((cell) => parseInline(cell)), rows });
      continue;
    }

    const paragraph: string[] = [];
    while (i < lines.length) {
      const current = lines[i] ?? "";
      if (current.trim() === "" || (paragraph.length > 0 && startsBlock(current))) break;
      paragraph.push(current.trim());
      i += 1;
    }
    blocks.push({ type: "paragraph", children: parseInline(paragraph.join(" ")) });
  }
  return blocks;
}

export function parseMarkdown(source: string): Block[] {
  const lines = source
    .replace(/\r\n?/g, "\n")
    .split("\n")
    .map((line) => line.replace(/^\t+/, (tabs) => "    ".repeat(tabs.length)));
  return parseBlocks(lines, 0);
}
