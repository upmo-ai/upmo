import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { color as tokenValues } from "@upmo-ai/tokens";

import { App } from "../../App";
import {
  createStubApi,
  type ApiClient,
  type ArtifactDiff,
  type ArtifactDiffFile,
  type ArtifactRecord,
} from "../../app/api";
import { AppProviders } from "../../theme";
import { CodeView, DiffView } from "./code-view";
import { changedFiles, changeOf, fileLabel } from "./diff";
import { diffOptions, readOnlyOptions } from "./editor-options";
import { editorLanguages, languageFor } from "./editor-language";
import { baseOf, syntaxColors, themeData, tokenColor, uiColors, type TokenReader } from "./editor-theme";
import { behavior, mounted, resetFake } from "./fake-surface";
import { MAX_VIEW_BYTES } from "./types";

// The text of the module that holds Monaco, for the tests that check what it imports.
const surfaceSource = Object.values(
  import.meta.glob<string>("./monaco-surface.ts", { query: "?raw", import: "default", eager: true }),
).join("\n");

// jsdom cannot run Monaco, so the views are tested against a stand-in for the
// module that holds it. The real editor is covered by e2e/artifacts.mjs.
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

async function openArtifacts(taskId: string, heading: string, client?: ApiClient) {
  renderAt(`/tasks/${taskId}`, client);
  await screen.findByRole("heading", { level: 1, name: heading });
  click(screen.getByRole("tab", { name: "Artifacts" }));
}

/** Opens the mail task and the artifact named `name`. */
async function openMailArtifact(name: string, client?: ApiClient) {
  await openArtifacts("aster-mail", "Mail triage", client);
  const table = await screen.findByRole("table", { name: "Artifacts" });
  click(within(table).getByRole("button", { name }));
  return screen.findByRole("region", { name });
}

const record = (over: Partial<ArtifactRecord> = {}): ArtifactRecord => ({
  id: "x",
  name: "x.py",
  kind: "code",
  mime: "text/x-python",
  size: 10,
  created_at: "2026-11-05T08:00:00.000Z",
  ...over,
});

function withArtifacts(over: Partial<ApiClient["artifacts"]>): ApiClient {
  const stub = createStubApi();
  return { ...stub, artifacts: { ...stub.artifacts, ...over } };
}

function setWidth(width: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, writable: true, value: width });
}

const wideWidth = window.innerWidth;

describe("code and text in the editor", () => {
  beforeEach(() => {
    localStorage.clear();
    resetFake();
  });

  afterEach(() => {
    cleanup();
    setWidth(wideWidth);
    vi.restoreAllMocks();
  });

  it("UPM-77 opens a code file in the editor with its text, name and language", async () => {
    const stub = createStubApi();
    renderAt("/tasks/aster-digest");
    await screen.findByRole("heading", { level: 1, name: "Daily finance digest" });
    click(screen.getByRole("tab", { name: "Artifacts" }));
    const table = await screen.findByRole("table", { name: "Artifacts" });
    click(within(table).getByRole("button", { name: "over_threshold.py" }));
    const view = await screen.findByRole("region", { name: "over_threshold.py" });
    await waitFor(() => expect(mounted.code.filter((item) => !item.disposed)).toHaveLength(1));
    const editor = mounted.code.find((item) => !item.disposed);
    await waitFor(() => expect(editor?.label).toBe("over_threshold.py"));
    expect(editor?.language).toBe("python");
    expect(editor?.text).toBe(await stub.artifacts.text("over-threshold"));
    expect(view.querySelector('[data-fake-editor="code"]')?.textContent).toContain("def over_threshold(");
  });

  it("UPM-77 does not put Markdown, CSV or HTML in the editor", async () => {
    await openArtifacts("aster-digest", "Daily finance digest");
    const table = await screen.findByRole("table", { name: "Artifacts" });
    // The oldest file is plain text, so the editor opens once and is let go of.
    await waitFor(() => expect(mounted.code).toHaveLength(1));
    for (const name of ["digest-draft.md", "invoices-oct.csv", "digest-preview.html"]) {
      click(within(table).getByRole("button", { name }));
      await screen.findByRole("region", { name });
    }
    await screen.findByTitle("digest-preview.html preview");
    expect(mounted.code).toHaveLength(1);
    expect(mounted.code[0]?.disposed).toBe(true);
  });

  it("UPM-77 loads the editor only when a code or text file is opened", async () => {
    await openArtifacts("aster-digest", "Daily finance digest");
    const table = await screen.findByRole("table", { name: "Artifacts" });
    click(within(table).getByRole("button", { name: "spend-chart.png" }));
    await screen.findByRole("region", { name: "spend-chart.png" });
    click(within(table).getByRole("button", { name: "invoice-bundle.zip" }));
    await screen.findByRole("region", { name: "invoice-bundle.zip" });
    expect(mounted.code.filter((item) => item.label !== "run-notes.txt")).toHaveLength(0);
  });

  it("UPM-77 swaps the file in the same editor when the name or text changes, with no loading flash", async () => {
    const { container, rerender } = render(
      <AppProviders>
        <CodeView name="a.py" text="print('a')" />
      </AppProviders>,
    );
    await waitFor(() => expect(mounted.code).toHaveLength(1));
    await waitFor(() => expect(container.querySelector(".artifact-editor")?.getAttribute("data-phase")).toBe("ready"));

    rerender(
      <AppProviders>
        <CodeView name="b.json" text='{"b": 1}' />
      </AppProviders>,
    );
    await waitFor(() => expect(mounted.code[0]?.label).toBe("b.json"));
    expect(mounted.code[0]?.language).toBe("json");
    expect(mounted.code[0]?.text).toBe('{"b": 1}');
    expect(container.querySelector('[data-fake-editor="code"]')?.textContent).toBe('{"b": 1}');
    // The same editor serves both files: it is not let go of and made again.
    expect(mounted.code).toHaveLength(1);
    expect(mounted.code[0]?.disposed).toBe(false);
    expect(screen.queryByText("Loading the viewer")).toBeNull();
    expect(container.querySelector(".artifact-editor")?.getAttribute("data-phase")).toBe("ready");

    // A change of the text alone is a swap too.
    rerender(
      <AppProviders>
        <CodeView name="b.json" text='{"b": 2}' />
      </AppProviders>,
    );
    await waitFor(() => expect(mounted.code[0]?.text).toBe('{"b": 2}'));
    expect(mounted.code[0]?.files.map((file) => file.label)).toEqual(["a.py", "b.json", "b.json"]);
    expect(mounted.code).toHaveLength(1);

    rerender(<AppProviders>{null}</AppProviders>);
    expect(mounted.code[0]?.disposed).toBe(true);
  });

  it("UPM-77 lets go of the editor when another artifact is picked", async () => {
    await openArtifacts("aster-digest", "Daily finance digest");
    const table = await screen.findByRole("table", { name: "Artifacts" });
    await waitFor(() => expect(mounted.code).toHaveLength(1));
    click(within(table).getByRole("button", { name: "over_threshold.py" }));
    await waitFor(() => expect(mounted.code).toHaveLength(2));
    expect(mounted.code[0]?.disposed).toBe(true);
    expect(mounted.code[1]?.disposed).toBe(false);
    click(within(table).getByRole("button", { name: "spend-chart.png" }));
    await screen.findByRole("region", { name: "spend-chart.png" });
    expect(mounted.code[1]?.disposed).toBe(true);
  });

  it("UPM-77 says an empty file is empty instead of showing a blank editor", async () => {
    await openArtifacts(
      "aster-digest",
      "Daily finance digest",
      withArtifacts({
        list: async () => [record({ id: "blank", name: "blank.py", size: 0 })],
        text: async () => "",
      }),
    );
    const view = await screen.findByRole("region", { name: "blank.py" });
    expect(await within(view).findByText(/This file is empty\. Use Download to get a copy\./)).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download blank.py" })).toBeTruthy();
    expect(mounted.code).toHaveLength(0);
  });

  it("UPM-77 treats a file of only whitespace as empty", async () => {
    await openArtifacts(
      "aster-digest",
      "Daily finance digest",
      withArtifacts({
        list: async () => [record({ id: "space", name: "space.txt", kind: "text", size: 4 })],
        text: async () => " \n\t\n",
      }),
    );
    const view = await screen.findByRole("region", { name: "space.txt" });
    expect(await within(view).findByText(/This file is empty\./)).toBeTruthy();
    expect(mounted.code).toHaveLength(0);
  });

  it("UPM-77 says a missing file is missing and offers the download, with no editor", async () => {
    const view = await openMailArtifact("rules-export.json");
    expect(await within(view).findByText(/This file is missing or could not be read\. Use Download to get a copy\./)).toBeTruthy();
    const link = within(view).getByRole("link", { name: "Download rules-export.json" });
    expect(link.getAttribute("href")).toBe("/api/artifacts/rules-export/download");
    expect(link.getAttribute("download")).toBe("rules-export.json");
    expect(mounted.code).toHaveLength(0);
    expect(view.querySelector('[data-fake-editor="code"]')).toBeNull();
  });

  it("UPM-77 does not read a code file over the limit and offers the download", async () => {
    const stub = createStubApi();
    const text = vi.fn<ApiClient["artifacts"]["text"]>(stub.artifacts.text);
    await openArtifacts(
      "aster-digest",
      "Daily finance digest",
      withArtifacts({
        list: async () => [record({ id: "big", name: "big.py", size: MAX_VIEW_BYTES + 1 })],
        text,
      }),
    );
    const view = await screen.findByRole("region", { name: "big.py" });
    expect(within(view).getByText(/too large to show here/)).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download big.py" })).toBeTruthy();
    expect(text).not.toHaveBeenCalled();
    expect(mounted.code).toHaveLength(0);
  });

  it("UPM-77 says so when the editor cannot start, and keeps the download", async () => {
    behavior.failMount = true;
    await openArtifacts("aster-digest", "Daily finance digest");
    const view = await screen.findByRole("region", { name: "run-notes.txt" });
    expect(await within(view).findByText("The viewer could not be loaded. Download the file to read it.")).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download run-notes.txt" })).toBeTruthy();
    expect(view.querySelector(".artifact-editor")?.hasAttribute("hidden")).toBe(true);
    expect(within(view).queryByText("Loading the viewer")).toBeNull();
  });

  it("UPM-77 follows the app theme", async () => {
    localStorage.setItem("upmo-theme", "dark");
    await openArtifacts("aster-digest", "Daily finance digest");
    await waitFor(() => expect(mounted.code).toHaveLength(1));
    expect(mounted.code[0]?.themes[0]).toBe("dark");

    const choice = within(screen.getByTestId("theme-switch")).getByRole("radio", { name: "High contrast" });
    click(choice);
    await waitFor(() => expect(mounted.code[0]?.themes.at(-1)).toBe("hc"));
    // The same editor changes theme; it is not made again.
    expect(mounted.code).toHaveLength(1);
    expect(mounted.code[0]?.disposed).toBe(false);
  });
});

describe("diffs in the diff editor", () => {
  beforeEach(() => {
    localStorage.clear();
    resetFake();
  });

  afterEach(() => {
    cleanup();
    setWidth(wideWidth);
    vi.restoreAllMocks();
  });

  it("UPM-77 opens a diff with one tab per changed file, side by side", async () => {
    const view = await openMailArtifact("triage-rules.patch");
    const tabs = await within(view).findByRole("tablist", { name: "Changed files" });
    expect(within(tabs).getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "triage.py",
      "rules/invoices.yaml",
      "rules/receipts.yaml (added)",
      "rules/legacy.yaml (deleted)",
    ]);
    expect(within(tabs).getByRole("tab", { name: "triage.py" }).getAttribute("aria-selected")).toBe("true");
    expect(within(view).getByText("4 files changed")).toBeTruthy();

    await waitFor(() => expect(mounted.diff).toHaveLength(1));
    const diff = mounted.diff[0];
    expect(diff?.sideBySide).toEqual([true]);
    expect(diff?.shown).toHaveLength(1);
    const first = diff?.shown[0];
    expect(first?.language).toBe("python");
    expect(first?.label).toBe("triage.py, before and after");
    expect(first?.original).toContain('return "review"');
    expect(first?.modified).toContain('startswith("receipt")');
    expect(first?.original).not.toContain("receipt");
  });

  it("UPM-77 shows the picked file, with an added file against nothing and a deleted file to nothing", async () => {
    const view = await openMailArtifact("triage-rules.patch");
    const tabs = await within(view).findByRole("tablist", { name: "Changed files" });
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(1));

    click(within(tabs).getByRole("tab", { name: "rules/invoices.yaml" }));
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(2));
    const modified = mounted.diff[0]?.shown[1];
    expect(modified?.language).toBe("yaml");
    expect(modified?.original).not.toContain("label: invoice");
    expect(modified?.modified).toContain("label: invoice");

    click(within(tabs).getByRole("tab", { name: "rules/receipts.yaml (added)" }));
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(3));
    expect(mounted.diff[0]?.shown[2]?.original).toBe("");
    expect(mounted.diff[0]?.shown[2]?.modified).toContain("subject: receipt");
    expect(within(tabs).getByRole("tab", { name: "rules/receipts.yaml (added)" }).getAttribute("aria-selected")).toBe("true");

    click(within(tabs).getByRole("tab", { name: "rules/legacy.yaml (deleted)" }));
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(4));
    expect(mounted.diff[0]?.shown[3]?.original).toContain("accounts@");
    expect(mounted.diff[0]?.shown[3]?.modified).toBe("");

    // One editor serves every file.
    expect(mounted.diff).toHaveLength(1);
    expect(mounted.diff[0]?.disposed).toBe(false);
  });

  it("UPM-77 reads the diff from the diff route and not as text", async () => {
    const stub = createStubApi();
    const diff = vi.fn<ApiClient["artifacts"]["diff"]>(stub.artifacts.diff);
    const text = vi.fn<ApiClient["artifacts"]["text"]>(stub.artifacts.text);
    await openMailArtifact("triage-rules.patch", withArtifacts({ diff, text }));
    await waitFor(() => expect(mounted.diff).toHaveLength(1));
    expect(diff).toHaveBeenCalledTimes(1);
    expect(diff).toHaveBeenCalledWith("triage-changes");
    expect(text).not.toHaveBeenCalledWith("triage-changes");
  });

  it("UPM-77 shows a diff inline, with a select for the file, on a phone", async () => {
    setWidth(390);
    const view = await openMailArtifact("triage-rules.patch");
    const select = await within(view).findByRole("combobox", { name: "Changed file" });
    expect(within(view).queryByRole("tablist")).toBeNull();
    expect(select).toHaveProperty("value", "triage.py");
    await waitFor(() => expect(mounted.diff).toHaveLength(1));
    expect(mounted.diff[0]?.sideBySide).toEqual([false]);

    act(() => {
      select.click();
    });
    const listbox = screen.getByRole("listbox", { name: "Changed file", hidden: true });
    expect(within(listbox).getAllByRole("option", { hidden: true }).map((option) => option.textContent)).toEqual([
      "triage.py",
      "rules/invoices.yaml",
      "rules/receipts.yaml (added)",
      "rules/legacy.yaml (deleted)",
    ]);
    act(() => {
      fireEvent.click(within(listbox).getByRole("option", { name: "rules/legacy.yaml (deleted)", hidden: true }));
    });
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(2));
    expect(mounted.diff[0]?.shown[1]?.modified).toBe("");
    expect(mounted.diff[0]?.shown[1]?.original).toContain("accounts@");
  });

  it("UPM-77 changes layout when the screen does, without making the editor again", async () => {
    const view = await openMailArtifact("triage-rules.patch");
    await within(view).findByRole("tablist", { name: "Changed files" });
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(1));
    click(within(view).getByRole("tab", { name: "rules/receipts.yaml (added)" }));
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(2));

    setWidth(390);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    const select = await within(view).findByRole("combobox", { name: "Changed file" });
    // The file that was picked stays picked.
    expect(select).toHaveProperty("value", "rules/receipts.yaml (added)");
    await waitFor(() => expect(mounted.diff[0]?.sideBySide).toEqual([true, false]));
    expect(mounted.diff).toHaveLength(1);
    expect(mounted.diff[0]?.disposed).toBe(false);
    expect(mounted.diff[0]?.shown).toHaveLength(2);
  });

  it("UPM-77 keeps the picked tab and the shown file in step when the diff shrinks", async () => {
    const file = (path: string): ArtifactDiffFile => ({ path, before: `old ${path}`, after: `new ${path}` });
    const four = ["a.py", "b.py", "c.py", "d.py"].map(file);
    const { rerender } = render(
      <AppProviders>
        <DiffView files={four} />
      </AppProviders>,
    );
    const tabs = await screen.findByRole("tablist", { name: "Changed files" });
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(1));
    click(within(tabs).getByRole("tab", { name: "d.py" }));
    await waitFor(() => expect(mounted.diff[0]?.shown.at(-1)?.modified).toBe("new d.py"));

    rerender(
      <AppProviders>
        <DiffView files={four.slice(0, 2).map((item) => ({ ...item }))} />
      </AppProviders>,
    );
    await waitFor(() => expect(screen.getAllByRole("tab")).toHaveLength(2));
    // The last file left is picked, in the tabs and in the editor.
    const selected = screen.getAllByRole("tab").filter((tab) => tab.getAttribute("aria-selected") === "true");
    expect(selected.map((tab) => tab.textContent)).toEqual(["b.py"]);
    await waitFor(() => expect(mounted.diff[0]?.shown.at(-1)?.modified).toBe("new b.py"));
    expect(mounted.diff[0]?.shown.at(-1)?.label).toBe("b.py, before and after");
    expect(mounted.diff).toHaveLength(1);
  });

  it("UPM-77 keeps the phone select and the shown file in step when the diff shrinks", async () => {
    setWidth(390);
    const file = (path: string): ArtifactDiffFile => ({ path, before: `old ${path}`, after: `new ${path}` });
    const three = ["a.py", "b.py", "c.py"].map(file);
    const { rerender } = render(
      <AppProviders>
        <DiffView files={three} />
      </AppProviders>,
    );
    const select = await screen.findByRole("combobox", { name: "Changed file" });
    await waitFor(() => expect(mounted.diff[0]?.shown).toHaveLength(1));
    act(() => {
      select.click();
    });
    const listbox = screen.getByRole("listbox", { name: "Changed file", hidden: true });
    act(() => {
      fireEvent.click(within(listbox).getByRole("option", { name: "c.py", hidden: true }));
    });
    await waitFor(() => expect(mounted.diff[0]?.shown.at(-1)?.modified).toBe("new c.py"));

    rerender(
      <AppProviders>
        <DiffView files={three.slice(0, 1).map((item) => ({ ...item }))} />
      </AppProviders>,
    );
    await waitFor(() => expect(mounted.diff[0]?.shown.at(-1)?.modified).toBe("new a.py"));
    expect(screen.getByRole("combobox", { name: "Changed file" })).toHaveProperty("value", "a.py");
  });

  it("UPM-77 follows the app theme", async () => {
    localStorage.setItem("upmo-theme", "dark");
    await openMailArtifact("triage-rules.patch");
    await waitFor(() => expect(mounted.diff).toHaveLength(1));
    expect(mounted.diff[0]?.themes[0]).toBe("dark");
    click(within(screen.getByTestId("theme-switch")).getByRole("radio", { name: "Light" }));
    await waitFor(() => expect(mounted.diff[0]?.themes.at(-1)).toBe("light"));
    expect(mounted.diff).toHaveLength(1);
  });

  it("UPM-77 lets go of the diff editor when another artifact is picked", async () => {
    const view = await openMailArtifact("triage-rules.patch");
    await waitFor(() => expect(mounted.diff).toHaveLength(1));
    expect(view).toBeTruthy();
    const table = screen.getByRole("table", { name: "Artifacts" });
    click(within(table).getByRole("button", { name: "triage-notes.md" }));
    await screen.findByRole("region", { name: "triage-notes.md" });
    expect(mounted.diff[0]?.disposed).toBe(true);
  });

  it("UPM-77 says a diff with no changed files has none, with the download", async () => {
    const view = await openMailArtifact(
      "triage-rules.patch",
      withArtifacts({ diff: async (): Promise<ArtifactDiff> => ({ files: [] }) }),
    );
    expect(await within(view).findByText(/This diff has no changed files\. Use Download to get a copy\./)).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download triage-rules.patch" })).toBeTruthy();
    expect(within(view).queryByRole("tablist")).toBeNull();
    expect(mounted.diff).toHaveLength(0);
  });

  it("UPM-77 says a missing diff is missing, with the download and no editor", async () => {
    const view = await openMailArtifact(
      "triage-rules.patch",
      withArtifacts({
        diff: async () => {
          throw new Error("Not found");
        },
      }),
    );
    expect(await within(view).findByText(/This diff is missing or could not be read\. Use Download to get a copy\./)).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download triage-rules.patch" })).toBeTruthy();
    expect(mounted.diff).toHaveLength(0);
  });

  it("UPM-77 does not read a diff over the limit and offers the download", async () => {
    const stub = createStubApi();
    const diff = vi.fn<ApiClient["artifacts"]["diff"]>(stub.artifacts.diff);
    await openArtifacts(
      "aster-mail",
      "Mail triage",
      withArtifacts({
        list: async () => [record({ id: "huge", name: "huge.patch", kind: "diff", size: MAX_VIEW_BYTES + 1 })],
        diff,
      }),
    );
    const view = await screen.findByRole("region", { name: "huge.patch" });
    expect(within(view).getByText(/too large to show here/)).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download huge.patch" })).toBeTruthy();
    expect(diff).not.toHaveBeenCalled();
    expect(mounted.diff).toHaveLength(0);
  });

  it("UPM-77 says so when the diff editor cannot start", async () => {
    behavior.failMount = true;
    const view = await openMailArtifact("triage-rules.patch");
    expect(await within(view).findByText("The viewer could not be loaded. Download the file to read it.")).toBeTruthy();
    expect(within(view).getByRole("link", { name: "Download triage-rules.patch" })).toBeTruthy();
  });

  it("UPM-77 lists a diff as a Diff in the artifact table", async () => {
    await openArtifacts("aster-mail", "Mail triage");
    const table = await screen.findByRole("table", { name: "Artifacts" });
    const row = within(table).getByRole("row", { name: /triage-rules\.patch/ });
    expect(within(row).getAllByRole("cell")[1]?.textContent).toBe("Diff");
  });
});

describe("artifacts api for diffs and missing files", () => {
  it("UPM-77 lists a diff with the contract fields and the kind diff", async () => {
    const listed = await createStubApi().artifacts.list("aster-mail");
    const diff = listed.find((item) => item.kind === "diff");
    expect(diff).toBeTruthy();
    expect(Object.keys(diff ?? {}).sort()).toEqual(["created_at", "id", "kind", "mime", "name", "size"]);
    expect(diff?.id).toBe("triage-changes");
  });

  it("UPM-77 answers the diff route with before and after, null for an added or deleted file", async () => {
    const { files } = await createStubApi().artifacts.diff("triage-changes");
    expect(files.length).toBeGreaterThan(1);
    for (const file of files) {
      expect(Object.keys(file).sort()).toEqual(["after", "before", "path"]);
    }
    expect(files.some((file) => file.before === null && typeof file.after === "string")).toBe(true);
    expect(files.some((file) => file.after === null && typeof file.before === "string")).toBe(true);
    expect(files.some((file) => typeof file.before === "string" && typeof file.after === "string")).toBe(true);
  });

  it("UPM-77 returns a copy of a diff", async () => {
    const api = createStubApi().artifacts;
    const first = await api.diff("triage-changes");
    first.files[0]!.path = "Mutated";
    first.files.pop();
    const again = await api.diff("triage-changes");
    expect(again.files[0]?.path).toBe("triage.py");
    expect(again.files).toHaveLength(4);
  });

  it("UPM-77 refuses a diff of something that is not a diff, or is missing", async () => {
    const api = createStubApi().artifacts;
    await expect(api.diff("triage-notes")).rejects.toThrow("not a diff");
    await expect(api.diff("nope")).rejects.toThrow("not in the workspace");
    await expect(api.diff("rules-export")).rejects.toThrow("missing");
  });

  it("UPM-77 fails to read an artifact whose content is missing, though it is listed", async () => {
    const api = createStubApi().artifacts;
    const listed = await api.list("aster-mail");
    expect(listed.some((item) => item.id === "rules-export")).toBe(true);
    await expect(api.text("rules-export")).rejects.toThrow("missing");
    expect(api.downloadUrl("rules-export")).toBe("/api/artifacts/rules-export/download");
  });

  it("UPM-77 gives a diff a patch to download", async () => {
    const url = createStubApi().artifacts.downloadUrl("triage-changes");
    expect(url).toMatch(/^data:text\/x-diff;charset=utf-8,/);
    const patch = decodeURIComponent(url.slice(url.indexOf(",") + 1));
    expect(patch).toContain("--- a/triage.py\n+++ b/triage.py\n");
    expect(patch).toContain("--- /dev/null\n+++ b/rules/receipts.yaml\n");
    expect(patch).toContain("--- a/rules/legacy.yaml\n+++ /dev/null\n");
  });
});

describe("changed files", () => {
  it("UPM-77 tells added, deleted and modified files apart", () => {
    expect(changeOf({ path: "a", before: null, after: "x" })).toBe("added");
    expect(changeOf({ path: "a", before: "x", after: null })).toBe("deleted");
    expect(changeOf({ path: "a", before: "x", after: "y" })).toBe("modified");
    // An empty file is a file: it is not the same as no file.
    expect(changeOf({ path: "a", before: "", after: "y" })).toBe("modified");
    expect(changeOf({ path: "a", before: "x", after: "" })).toBe("modified");
  });

  it("UPM-77 names a changed file, with added and deleted in words", () => {
    expect(fileLabel({ path: "src/a.ts", before: "x", after: "y" })).toBe("src/a.ts");
    expect(fileLabel({ path: "src/a.ts", before: null, after: "y" })).toBe("src/a.ts (added)");
    expect(fileLabel({ path: "src/a.ts", before: "x", after: null })).toBe("src/a.ts (deleted)");
  });

  it("UPM-77 counts changed files", () => {
    expect(changedFiles(1)).toBe("1 file changed");
    expect(changedFiles(3)).toBe("3 files changed");
  });
});

describe("editor languages", () => {
  it("UPM-77 picks a language from the file name alone", () => {
    expect(languageFor("over_threshold.py")).toBe("python");
    expect(languageFor("rules/invoices.yaml")).toBe("yaml");
    expect(languageFor("deploy/app.config.json")).toBe("json");
    expect(languageFor("src/app/Dockerfile")).toBe("dockerfile");
    expect(languageFor("page.TSX")).toBe("typescript");
    expect(languageFor("main.c")).toBe("cpp");
    expect(languageFor("run.sh")).toBe("shell");
    expect(languageFor("icon.svg")).toBe("xml");
  });

  it("UPM-77 shows a name it does not know as plain text", () => {
    for (const name of ["README", ".gitignore", "notes.unknown", "x.constructor", "x.toString", "dir.py/file", "changes.patch"]) {
      expect(languageFor(name)).toBe("plaintext");
    }
  });

  it("UPM-77 registers a tokenizer for every language a name can pick", () => {
    // JSON is the one language with no plain definition: its tokenizer is registered by hand.
    const registered = editorLanguages.map((language) =>
      language === "json" ? 'id: "json"' : `monaco-editor/languages/definitions/${language}/register"`,
    );
    expect(surfaceSource.length).toBeGreaterThan(0);
    for (const snippet of registered) {
      expect(surfaceSource).toContain(snippet);
    }
  });

  it("UPM-77 keeps the editor from loading anything from a CDN", () => {
    expect(surfaceSource).not.toMatch(/https?:\/\//);
    expect(surfaceSource).toContain("getWorker");
    expect(surfaceSource).toContain("editor.worker?worker");
  });
});

describe("editor options", () => {
  const read: TokenReader = () => "";

  it("UPM-77 makes both editors read only, in the page and in the model", () => {
    for (const options of [readOnlyOptions(read), diffOptions(read, true), diffOptions(read, false)]) {
      expect(options.readOnly).toBe(true);
      expect(options.domReadOnly).toBe(true);
      expect(options.links).toBe(false);
      expect(options.contextmenu).toBe(false);
      // Tab moves on, so the editor is not a keyboard trap.
      expect(options.tabFocusMode).toBe(true);
    }
    expect(diffOptions(read, true).originalEditable).toBe(false);
  });

  it("UPM-77 names the diff editor from the start, before a file is shown", () => {
    for (const sideBySide of [true, false]) {
      const label = diffOptions(read, sideBySide).ariaLabel;
      expect(typeof label).toBe("string");
      expect((label ?? "").trim()).not.toBe("");
    }
  });

  it("UPM-77 picks the diff layout from the argument alone", () => {
    expect(diffOptions(read, true).renderSideBySide).toBe(true);
    expect(diffOptions(read, false).renderSideBySide).toBe(false);
    // Monaco's own switch to inline on a narrow box is off, so the screen decides.
    expect(diffOptions(read, true).useInlineViewWhenSpaceIsLimited).toBe(false);
  });

  it("UPM-77 takes the font from the tokens and leaves it alone when they are not set", () => {
    const tokens: Record<string, string> = {
      "--font-mono": '"Fira Code", monospace',
      "--text-code-size": "13px",
      "--text-code-line": "20px",
    };
    const options = readOnlyOptions((token) => tokens[token] ?? "");
    expect(options.fontFamily).toBe('"Fira Code", monospace');
    expect(options.fontSize).toBe(13);
    expect(options.lineHeight).toBe(20);
    const bare = readOnlyOptions(read);
    expect("fontFamily" in bare).toBe(false);
    expect("fontSize" in bare).toBe(false);
    expect("lineHeight" in bare).toBe(false);
  });
});

describe("editor theme", () => {
  const themes = ["light", "dark", "hc"] as const;
  // The colour check over web/src allows no colour literals, so the test colours are built.
  const hex = (digits: string) => `#${digits}`;
  const call = (name: string, ...channels: number[]) => `${name}(${channels.join(", ")})`;

  /** A page whose every token is a different colour, so a colour in the theme can be traced to its token. */
  function paint(): { read: TokenReader; byToken: Map<string, string> } {
    const byToken = new Map<string, string>();
    const read: TokenReader = (token) => {
      if (!byToken.has(token)) {
        const n = byToken.size + 1;
        byToken.set(token, hex(`${n.toString(16).padStart(2, "0")}${(255 - n).toString(16).padStart(2, "0")}7f`));
      }
      return byToken.get(token) ?? "";
    };
    return { read, byToken };
  }

  it("UPM-77 gives each app theme its Monaco base", () => {
    const { read } = paint();
    expect(themeData("light", read).base).toBe("vs");
    expect(themeData("dark", read).base).toBe("vs-dark");
    expect(themeData("hc", read).base).toBe("hc-light");
    expect(Object.keys(baseOf).sort()).toEqual([...themes].sort());
  });

  it("UPM-77 takes every colour from a design token and hard-codes none", () => {
    for (const theme of themes) {
      const { read, byToken } = paint();
      const data = themeData(theme, read);
      const fromTokens = new Set(byToken.values());
      expect(Object.keys(data.colors)).toHaveLength(uiColors.length);
      for (const [id, token, alpha] of uiColors) {
        const color = data.colors[id];
        expect(color).toBe(`${byToken.get(token)}${alpha ?? ""}`);
        expect(fromTokens.has((color ?? "").slice(0, 7))).toBe(true);
      }
      expect(data.rules).toHaveLength(syntaxColors.length);
      for (const rule of data.rules) {
        expect(fromTokens.has(`#${rule.foreground}`)).toBe(true);
      }
    }
  });

  it("UPM-77 changes with the tokens, so a theme switch changes the editor", () => {
    const light: TokenReader = () => hex("ffffff");
    const dark: TokenReader = () => hex("000000");
    expect(themeData("light", light).colors["editor.background"]).toBe(hex("ffffff"));
    expect(themeData("dark", dark).colors["editor.background"]).toBe(hex("000000"));
  });

  it("UPM-77 leaves a colour to Monaco when its token is not a plain colour", () => {
    const data = themeData("light", () => "");
    expect(data.colors).toEqual({});
    expect(data.rules).toEqual([]);
    const odd = themeData("light", () => "calc(1px)");
    expect(odd.colors).toEqual({});
  });

  it("UPM-77 reads hex, short hex, rgb and a token that points at another", () => {
    const tokens: Record<string, string> = {
      "--a": hex("1F8A5B"),
      "--b": hex("abc"),
      "--c": call("rgb", 31, 138, 91),
      "--d": call("rgba", 0, 0, 0, 0.5),
      "--e": "var(--a)",
      "--loop": "var(--loop)",
    };
    const read: TokenReader = (token) => tokens[token] ?? "";
    expect(tokenColor(read, "--a")).toBe(hex("1f8a5b"));
    expect(tokenColor(read, "--b")).toBe(hex("aabbcc"));
    expect(tokenColor(read, "--c")).toBe(hex("1f8a5b"));
    expect(tokenColor(read, "--d")).toBe(hex("000000"));
    expect(tokenColor(read, "--e")).toBe(hex("1f8a5b"));
    expect(tokenColor(read, "--loop")).toBeUndefined();
    expect(tokenColor(read, "--none")).toBeUndefined();
  });

  it("UPM-77 builds the editor theme from the real tokens of each Upmo theme", () => {
    const values = tokenValues as unknown as Record<string, Record<string, string>>;
    const themeOf = (theme: (typeof themes)[number]) =>
      themeData(theme, (token) => values[token.slice(2)]?.[theme] ?? "");
    for (const theme of themes) {
      const data = themeOf(theme);
      // A token that is missing from a theme would leave a colour out.
      expect(Object.keys(data.colors)).toHaveLength(uiColors.length);
      expect(data.rules).toHaveLength(syntaxColors.length);
      expect(data.colors["editor.background"]).toBe(values["surface-200"]?.[theme]);
      expect(data.colors["editor.foreground"]).toBe(values["ink"]?.[theme]);
    }
    expect(themeOf("dark").colors["editor.background"]).not.toBe(themeOf("light").colors["editor.background"]);
    expect(themeOf("hc").colors["contrastBorder"]).toBe(values["border-strong"]?.["hc"]);
  });
});
