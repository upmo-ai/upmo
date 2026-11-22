import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient, type ArtifactRecord } from "../../app/api";
import { AppProviders } from "../../theme";
import { languageFor } from "./editor-language";
import { mounted, resetFake } from "./fake-surface";
import { MAX_VIEW_BYTES, formatSize } from "./types";
import { MAX_CSV_ROWS, htmlSandbox, viewerFor } from "./viewer";

// jsdom cannot run Monaco. monaco.test.tsx covers the editor views; here the
// editor is the stand-in, and the real one is covered by e2e/artifacts.mjs.
vi.mock("./monaco-surface", () => import("./fake-surface"));

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
}

function click(element: HTMLElement) {
  act(() => {
    element.click();
  });
}

/** Opens a task and its Artifacts tab, and waits for the list. */
async function openArtifacts(taskId: string, client?: ApiClient) {
  renderAt(`/tasks/${taskId}`, client);
  await screen.findByRole("heading", { level: 1 });
  click(screen.getByRole("tab", { name: "Artifacts" }));
}

async function openList() {
  return screen.findByRole("table", { name: "Artifacts" });
}

async function pick(name: string) {
  const table = await openList();
  click(within(table).getByRole("button", { name }));
  return screen.findByRole("region", { name });
}

const record = (over: Partial<ArtifactRecord> = {}): ArtifactRecord => ({
  id: "x",
  name: "x.csv",
  kind: "csv",
  mime: "text/csv",
  size: 10,
  created_at: "2026-11-05T08:00:00.000Z",
  ...over,
});

function withArtifacts(over: Partial<ApiClient["artifacts"]>): ApiClient {
  const stub = createStubApi();
  return { ...stub, artifacts: { ...stub.artifacts, ...over } };
}

describe("task artifacts", () => {
  beforeEach(() => {
    localStorage.clear();
    resetFake();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("UPM-74 lists a task's artifacts with name, kind, size and created", async () => {
    await openArtifacts("aster-digest");
    const table = await openList();
    expect(within(table).getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual([
      "Name",
      "Kind",
      "Size",
      "Created",
      "Actions",
    ]);
    expect(within(table).getAllByRole("row")).toHaveLength(9);

    const chart = within(table).getByRole("row", { name: /spend-chart\.png/ });
    const cells = within(chart).getAllByRole("cell");
    expect(cells[0]?.textContent).toBe("spend-chart.png");
    expect(cells[1]?.textContent).toBe("Image");
    expect(cells[2]?.textContent).toBe("118 B");
    expect(chart.querySelector("time")?.getAttribute("datetime")).toBe("2026-11-05T08:02:00.000Z");
    expect(screen.getByText("8 artifacts")).toBeTruthy();
  });

  it("UPM-74 reads nothing until the tab is opened, then reads the task's list", async () => {
    const stub = createStubApi();
    const list = vi.fn<ApiClient["artifacts"]["list"]>(stub.artifacts.list);
    renderAt("/tasks/aster-digest", withArtifacts({ list }));
    await screen.findByRole("heading", { level: 1, name: "Daily finance digest" });
    expect(list).not.toHaveBeenCalled();
    click(screen.getByRole("tab", { name: "Artifacts" }));
    await openList();
    expect(list).toHaveBeenCalledTimes(1);
    expect(list).toHaveBeenCalledWith("aster-digest");
  });

  it("UPM-74 shows an empty state when the task made no artifacts", async () => {
    await openArtifacts("aster-failed");
    expect(await screen.findByRole("heading", { level: 2, name: "No artifacts yet" })).toBeTruthy();
    expect(screen.queryByRole("table", { name: "Artifacts" })).toBeNull();
  });

  it("UPM-74 says so when the list cannot be read", async () => {
    await openArtifacts(
      "aster-digest",
      withArtifacts({
        list: async () => {
          throw new Error("Artifacts are offline.");
        },
      }),
    );
    expect(await screen.findByText("Artifacts are offline.")).toBeTruthy();
    expect(screen.queryByRole("table", { name: "Artifacts" })).toBeNull();
  });

  it("UPM-74 opens the first artifact and marks the one that is open", async () => {
    await openArtifacts("aster-digest");
    const table = await openList();
    // The list is oldest first, so the oldest file opens.
    const first = within(table).getByRole("button", { name: "run-notes.txt" });
    const second = within(table).getByRole("button", { name: "invoices-oct.csv" });
    expect(within(table).getAllByRole("row")[1]).toBe(first.closest("tr"));
    expect(first.getAttribute("aria-current")).toBe("true");
    expect(second.getAttribute("aria-current")).toBeNull();
    expect(await screen.findByRole("region", { name: "run-notes.txt" })).toBeTruthy();

    click(second);
    expect(second.getAttribute("aria-current")).toBe("true");
    expect(first.getAttribute("aria-current")).toBeNull();
    expect(await screen.findByRole("region", { name: "invoices-oct.csv" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "run-notes.txt" })).toBeNull();
  });

  it("UPM-74 starts from the first artifact again for each task", async () => {
    const history = createMemoryHistory({ initialEntries: ["/tasks/aster-digest"] });
    render(
      <AppProviders>
        <App history={history} />
      </AppProviders>,
    );
    await screen.findByRole("heading", { level: 1, name: "Daily finance digest" });
    click(screen.getByRole("tab", { name: "Artifacts" }));
    await pick("spend-chart.png");

    act(() => {
      history.push("/tasks/aster-mail");
    });
    await screen.findByRole("heading", { level: 1, name: "Mail triage" });
    click(screen.getByRole("tab", { name: "Artifacts" }));
    const mail = await openList();
    expect(within(mail).getByRole("button", { name: "triage-notes.md" }).getAttribute("aria-current")).toBe("true");

    act(() => {
      history.push("/tasks/aster-digest");
    });
    await screen.findByRole("heading", { level: 1, name: "Daily finance digest" });
    click(screen.getByRole("tab", { name: "Artifacts" }));
    const digest = await openList();
    expect(within(digest).getByRole("button", { name: "run-notes.txt" }).getAttribute("aria-current")).toBe("true");
    expect(within(digest).getByRole("button", { name: "spend-chart.png" }).getAttribute("aria-current")).toBeNull();
  });

  it("UPM-74 says the list is loading until it arrives", async () => {
    let release: (items: ArtifactRecord[]) => void = () => {};
    const pending = new Promise<ArtifactRecord[]>((resolve) => {
      release = resolve;
    });
    await openArtifacts("aster-digest", withArtifacts({ list: () => pending }));
    expect(await screen.findByText("Loading artifacts")).toBeTruthy();
    expect(screen.queryByRole("table", { name: "Artifacts" })).toBeNull();
    expect(screen.queryByRole("heading", { level: 2, name: "No artifacts yet" })).toBeNull();
    await act(async () => {
      release([]);
      await pending;
    });
    expect(await screen.findByRole("heading", { level: 2, name: "No artifacts yet" })).toBeTruthy();
    expect(screen.queryByText("Loading artifacts")).toBeNull();
  });

  it("UPM-74 shows an image in an img named after the file", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("spend-chart.png");
    const image = within(view).getByRole("img", { name: "spend-chart.png" }) as HTMLImageElement;
    expect(image.getAttribute("src")).toMatch(/^data:image\/png;base64,/);
  });

  it("UPM-74 renders Markdown as headings, lists and a safe link", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("digest-draft.md");
    expect(await within(view).findByRole("heading", { level: 5, name: "October invoices over threshold" })).toBeTruthy();
    expect(within(view).getAllByRole("listitem")).toHaveLength(3);
    expect(within(view).getByText("€18,420").tagName).toBe("STRONG");
    const link = within(view).getByRole("link", { name: /^finance policy\s*\(opens in a new tab\)$/ });
    expect(link.getAttribute("href")).toBe("https://example.com/finance-policy");
    expect(link.getAttribute("rel")).toContain("noopener");
    expect(link.getAttribute("target")).toBe("_blank");
  });

  it("UPM-74 shows a script payload in a Markdown artifact as text", async () => {
    const payload = '# Hi\n\n<script>window.pwned = 1</script>\n\n<img src=x onerror="window.pwned = 1">\n\n[go](javascript:window.pwned=1)';
    await openArtifacts("aster-digest", withArtifacts({ text: async () => payload }));
    const view = await pick("digest-draft.md");
    await within(view).findByRole("heading", { level: 4, name: "Hi" });
    const body = view.querySelector(".artifact-markdown");
    expect(body).not.toBeNull();
    expect(body?.querySelector("script, img, iframe, a, [onerror]")).toBeNull();
    expect(body?.textContent).toContain("<script>window.pwned = 1</script>");
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined();
  });

  it("UPM-74 shows a CSV as a table and keeps a quoted comma in its cell", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("invoices-oct.csv");
    const table = await within(view).findByRole("table", { name: "invoices-oct.csv as a table" });
    expect(within(table).getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual([
      "Vendor",
      "Amount",
      "Due",
    ]);
    expect(within(table).getAllByRole("row")).toHaveLength(6);
    const row = within(table).getByRole("row", { name: /Hetzner/ });
    expect(within(row).getAllByRole("cell").map((cell) => cell.textContent)).toEqual([
      "Hetzner",
      "€2,140.00",
      "24 Oct",
    ]);
  });

  it("UPM-74 shows only the first rows of a long CSV and says so", async () => {
    const rows = Array.from({ length: MAX_CSV_ROWS + 25 }, (_, index) => `row-${index},${index}`);
    const body = ["name,value", ...rows].join("\n");
    await openArtifacts(
      "aster-digest",
      withArtifacts({
        list: async () => [record({ id: "long", name: "long.csv", size: body.length })],
        text: async () => body,
      }),
    );
    const table = await screen.findByRole("table", { name: "long.csv as a table" });
    expect(within(table).getAllByRole("row")).toHaveLength(MAX_CSV_ROWS + 1);
    expect(screen.getByText(`Showing the first ${MAX_CSV_ROWS} of ${MAX_CSV_ROWS + 25} rows. Download the file for the rest.`)).toBeTruthy();
  });

  it("UPM-74 shows code in the editor with the language of its file name", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("over_threshold.py");
    await waitFor(() => {
      expect(view.textContent).toContain("def over_threshold(invoices, limit=1000):");
    });
    const editor = mounted.code.at(-1);
    expect(editor?.label).toBe("over_threshold.py");
    expect(editor?.language).toBe("python");
  });

  it("UPM-74 shows a plain text file in the editor as plain text", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("run-notes.txt");
    await waitFor(() => {
      expect(view.textContent).toContain("Fetched 31 invoices from the finance mailbox.");
    });
    expect(mounted.code.at(-1)?.language).toBe("plaintext");
  });

  it("UPM-74 shows HTML only in an iframe with an empty sandbox", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("digest-preview.html");
    const frame = (await within(view).findByTitle("digest-preview.html preview")) as HTMLIFrameElement;
    expect(frame.tagName).toBe("IFRAME");
    // An empty sandbox is the strictest one: no scripts, no same-origin access, no forms, no pop-ups.
    expect(frame.getAttribute("sandbox")).toBe("");
    expect(htmlSandbox).toBe("");
    expect(frame.getAttribute("sandbox") ?? "").not.toMatch(/allow-/);
    expect(frame.getAttribute("srcdoc")).toContain("<h1>Finance digest</h1>");
    // The file's markup stays inside the frame, not in the page. That a sandboxed
    // script does not run is a browser behavior, which e2e/artifacts.mjs checks.
    expect(screen.queryByRole("heading", { name: "Finance digest" })).toBeNull();
    expect(view.querySelector("script")).toBeNull();
  });

  it("UPM-74 offers a download for a PDF, which has no viewer yet", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("digest.pdf");
    expect(within(view).getByText(/There is no preview for PDF files yet/)).toBeTruthy();
    const link = within(view).getByRole("link", { name: "Download digest.pdf" });
    expect(link.getAttribute("href")).toMatch(/^data:application\/pdf/);
    expect(link.getAttribute("download")).toBe("digest.pdf");
    expect(within(view).queryByTitle(/preview/)).toBeNull();
    expect(view.querySelector("iframe, embed, object")).toBeNull();
  });

  it("UPM-74 offers a download for a kind with no viewer", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("invoice-bundle.zip");
    expect(within(view).getByText(/There is no preview for this kind of file/)).toBeTruthy();
    const link = within(view).getByRole("link", { name: "Download invoice-bundle.zip" });
    expect(link.getAttribute("href")).toMatch(/^data:application\/zip;base64,/);
    expect(link.getAttribute("download")).toBe("invoice-bundle.zip");
  });

  it("UPM-74 treats a kind it does not know as a plain file", async () => {
    await openArtifacts(
      "aster-digest",
      withArtifacts({
        list: async () => [record({ id: "n", name: "scan.heic", kind: "photo" as ArtifactRecord["kind"] })],
      }),
    );
    const table = await openList();
    expect(within(within(table).getByRole("row", { name: /scan\.heic/ })).getByText("File")).toBeTruthy();
    expect(within(await screen.findByRole("region", { name: "scan.heic" })).getByRole("link", { name: "Download scan.heic" })).toBeTruthy();
  });

  it("UPM-74 does not read a text file over the limit and offers the download", async () => {
    const stub = createStubApi();
    const text = vi.fn<ApiClient["artifacts"]["text"]>(stub.artifacts.text);
    await openArtifacts(
      "aster-digest",
      withArtifacts({
        list: async () => [record({ id: "big", name: "ledger.csv", size: 5 * 1024 * 1024 })],
        text,
      }),
    );
    const view = await screen.findByRole("region", { name: "ledger.csv" });
    expect(within(view).getByText(/5\.0 MB, too large to show here/)).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download ledger.csv" })).toBeTruthy();
    expect(text).not.toHaveBeenCalled();
  });

  it("UPM-74 says so when a file cannot be read", async () => {
    await openArtifacts(
      "aster-digest",
      withArtifacts({
        text: async () => {
          throw new Error("The file is gone.");
        },
      }),
    );
    const view = await screen.findByRole("region", { name: "run-notes.txt" });
    expect(await within(view).findByText(/This file is missing or could not be read\./)).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download run-notes.txt" })).toBeTruthy();
    expect(mounted.code).toHaveLength(0);
  });

  it("UPM-74 says so when a Markdown file cannot be read", async () => {
    await openArtifacts(
      "aster-digest",
      withArtifacts({
        text: async () => {
          throw new Error("The file is gone.");
        },
      }),
    );
    const view = await pick("digest-draft.md");
    expect(await within(view).findByText("The file is gone.")).toBeTruthy();
  });

  it("UPM-74 falls back to a download when an image will not load", async () => {
    await openArtifacts("aster-digest");
    const view = await pick("spend-chart.png");
    const image = within(view).getByRole("img", { name: "spend-chart.png" });
    act(() => {
      image.dispatchEvent(new Event("error"));
    });
    expect(await within(view).findByText(/This image could not be shown/)).toBeTruthy();
    expect(within(view).queryByRole("img")).toBeNull();
    expect(within(view).getByRole("link", { name: "Download spend-chart.png" })).toBeTruthy();
  });

  it("UPM-74 downloads from the row without opening the artifact", async () => {
    const downloads: { href: string; name: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      downloads.push({ href: this.href, name: this.download });
    });
    await openArtifacts("aster-digest");
    const table = await openList();
    click(within(table).getByRole("button", { name: "Download invoices-oct.csv" }));
    expect(downloads).toHaveLength(1);
    expect(downloads[0]?.name).toBe("invoices-oct.csv");
    expect(downloads[0]?.href).toMatch(/^data:text\/csv/);
    expect(within(table).getByRole("button", { name: "run-notes.txt" }).getAttribute("aria-current")).toBe("true");
  });

  it("UPM-74 gives each task its own artifacts", async () => {
    await openArtifacts("aster-mail");
    const table = await openList();
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    expect(within(table).getByRole("button", { name: "triage-notes.md" })).toBeTruthy();
    expect(screen.getByText("3 artifacts")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "run-notes.txt" })).toBeNull();
  });
});

describe("artifacts api", () => {
  it("UPM-74 lists the contract fields only and returns copies", async () => {
    const api = createStubApi().artifacts;
    const listed = await api.list("aster-digest");
    expect(listed).toHaveLength(8);
    for (const item of listed) {
      expect(Object.keys(item).sort()).toEqual(["created_at", "id", "kind", "mime", "name", "size"]);
    }
    listed[0]!.name = "Mutated";
    expect((await api.list("aster-digest"))[0]?.name).toBe("run-notes.txt");
  });

  it("UPM-74 lists oldest first, as the contract says", async () => {
    const listed = await createStubApi().artifacts.list("aster-digest");
    const times = listed.map((item) => item.created_at);
    expect(times).toEqual([...times].sort());
    expect(listed[0]?.name).toBe("run-notes.txt");
    expect(listed.at(-1)?.name).toBe("digest.pdf");
  });

  it("UPM-74 lists nothing for a task that has no artifacts", async () => {
    const api = createStubApi().artifacts;
    expect(await api.list("aster-failed")).toEqual([]);
    expect(await api.list("no-such-task")).toEqual([]);
  });

  it("UPM-74 reads the text of a text file and refuses the rest", async () => {
    const api = createStubApi().artifacts;
    expect(await api.text("run-notes")).toContain("Run 14");
    await expect(api.text("spend-chart")).rejects.toThrow("not text");
    await expect(api.text("nope")).rejects.toThrow("not in the workspace");
  });

  it("UPM-74 points an unknown artifact at the download route", () => {
    expect(createStubApi().artifacts.downloadUrl("nope")).toBe("/api/artifacts/nope/download");
  });
});

describe("artifact viewers", () => {
  it("UPM-74 picks a viewer by kind and falls back to a download", () => {
    expect(viewerFor(record({ kind: "image" }))).toBe("image");
    for (const kind of ["markdown", "csv", "text", "code", "html"] as const) {
      expect(viewerFor(record({ kind }))).toBe("text");
    }
    expect(viewerFor(record({ kind: "diff" }))).toBe("diff");
    expect(viewerFor(record({ kind: "pdf" }))).toBe("none");
    expect(viewerFor(record({ kind: "binary" }))).toBe("none");
    expect(viewerFor(record({ kind: "photo" as ArtifactRecord["kind"] }))).toBe("none");
  });

  it("UPM-74 reads text up to the limit and not past it", () => {
    expect(viewerFor(record({ size: MAX_VIEW_BYTES }))).toBe("text");
    expect(viewerFor(record({ size: MAX_VIEW_BYTES + 1 }))).toBe("too-large");
    expect(viewerFor(record({ kind: "diff", size: MAX_VIEW_BYTES }))).toBe("diff");
    expect(viewerFor(record({ kind: "diff", size: MAX_VIEW_BYTES + 1 }))).toBe("too-large");
  });

  it("UPM-74 picks a highlight language from the file name", () => {
    expect(languageFor("a.py")).toBe("python");
    expect(languageFor("A.TS")).toBe("typescript");
    expect(languageFor("Makefile")).toBe("plaintext");
    expect(languageFor("notes.unknown")).toBe("plaintext");
    expect(languageFor("x.constructor")).toBe("plaintext");
  });

  it("UPM-74 writes sizes in B, KB and MB", () => {
    expect(formatSize(0)).toBe("0 B");
    expect(formatSize(1023)).toBe("1023 B");
    expect(formatSize(2867)).toBe("2.8 KB");
    expect(formatSize(12 * 1024)).toBe("12 KB");
    expect(formatSize(5 * 1024 * 1024)).toBe("5.0 MB");
  });
});
