import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient } from "../../app/api";
import { AppProviders } from "../../theme";
import { MAX_FILE_BYTES } from "./types";

// A source settles this long after it is added. Long enough to be seen pending,
// short enough that the page's one second poll sees it ready.
const INGEST_MS = 400;
const SETTLE = { timeout: 5000 };

function renderAt(path: string, client: ApiClient = createStubApi({ knowledgeIngestMs: INGEST_MS })) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
  return client;
}

function click(element: HTMLElement) {
  act(() => {
    element.click();
  });
}

function enter(label: string, value: string) {
  fireEvent.change(screen.getByRole("textbox", { name: label }), { target: { value } });
}

function row(name: string | RegExp) {
  return screen.getByRole("row", { name });
}

async function findRow(name: string | RegExp) {
  return screen.findByRole("row", { name });
}

function chooseFile(file: File) {
  fireEvent.change(screen.getByLabelText("File to upload"), { target: { files: [file] } });
}

function fileOf(name: string, size = 12, mime = "text/plain") {
  const file = new File(["hello world!"], name, { type: mime });
  Object.defineProperty(file, "size", { value: size });
  return file;
}

async function openFolder(path: string, heading: string) {
  renderAt(path);
  await screen.findByRole("heading", { level: 1, name: heading });
}

type Sources = ApiClient["knowledge"]["sources"];

/** A stub client whose source calls can be replaced one by one. */
function withSources(patch: (base: Sources) => Partial<Sources>): ApiClient {
  const base = createStubApi({ knowledgeIngestMs: INGEST_MS });
  return {
    ...base,
    knowledge: {
      ...base.knowledge,
      sources: { ...base.knowledge.sources, ...patch(base.knowledge.sources) },
    },
  };
}

function pause(ms = 30) {
  return act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
}

describe("Knowledge page", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-139 lists the folders from the API as cards with their source count", async () => {
    renderAt("/knowledge");
    expect(await screen.findByRole("heading", { level: 1, name: "Knowledge" })).toBeTruthy();
    const product = await screen.findByRole("heading", { level: 2, name: "Product" });
    expect(screen.getByText("Folders · 6")).toBeTruthy();
    expect(within(product.closest("article") as HTMLElement).getByText(/2 sources · updated/)).toBeTruthy();
    expect(within(screen.getByRole("heading", { level: 2, name: "Policies" }).closest("article") as HTMLElement).getByText(/1 source · updated/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "New folder" })).toBeTruthy();
    expect(screen.queryByText("Give Upmo something to read")).toBeNull();
    expect(screen.queryByText("Handbook.pdf")).toBeNull();
  });

  it("UPM-139 opens a folder from its card", async () => {
    renderAt("/knowledge");
    click(await screen.findByRole("link", { name: "Finance" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Finance" })).toBeTruthy();
    expect(await screen.findByText("Close.md")).toBeTruthy();
    const sidebar = screen.getByRole("navigation", { name: "Main" });
    expect(within(sidebar).getByRole("link", { name: "Knowledge" }).getAttribute("aria-current")).toBe("page");
  });

  it("UPM-139 shows the empty state when there are no folders, then creates the first one", async () => {
    const client = createStubApi({ knowledgeIngestMs: INGEST_MS });
    for (const folder of await client.knowledge.folders.list()) {
      await client.knowledge.folders.remove(folder.id);
    }
    renderAt("/knowledge", client);

    expect(await screen.findByRole("heading", { level: 2, name: "Give Upmo something to read" })).toBeTruthy();
    expect(screen.queryByText(/^Folders · /)).toBeNull();

    click(screen.getByRole("button", { name: "New folder" }));
    const dialog = await screen.findByRole("dialog", { name: "New folder" });
    enter("Folder name", "Handbook");
    click(within(dialog).getByRole("button", { name: "Create folder" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Handbook" })).toBeTruthy();
    expect(await screen.findByRole("heading", { level: 3, name: "Nothing in this folder yet" })).toBeTruthy();
    expect(screen.getByText("0 sources")).toBeTruthy();
  });

  it("UPM-139 keeps Create folder disabled until the folder has a name", async () => {
    renderAt("/knowledge");
    click(await screen.findByRole("button", { name: "New folder" }));
    const dialog = await screen.findByRole("dialog", { name: "New folder" });
    const create = within(dialog).getByRole("button", { name: "Create folder" });
    expect(create.hasAttribute("disabled")).toBe(true);
    enter("Folder name", "   ");
    expect(create.hasAttribute("disabled")).toBe(true);
    enter("Folder name", "Ops");
    expect(create.hasAttribute("disabled")).toBe(false);
  });

  it("UPM-139 lists a folder's sources with type, status and revision", async () => {
    await openFolder("/knowledge/folder-product", "Product");
    expect(await screen.findByText("2 sources")).toBeTruthy();

    const pricing = await findRow(/Pricing\.md/);
    expect(within(pricing).getByText("Markdown")).toBeTruthy();
    expect(within(pricing).getByText("Curated")).toBeTruthy();
    expect(within(pricing).getByText("Ready")).toBeTruthy();
    expect(within(pricing).getByText("2")).toBeTruthy();

    const handbook = row(/Handbook\.pdf/);
    expect(within(handbook).getByText("File")).toBeTruthy();
    expect(within(handbook).getByText("1")).toBeTruthy();
  });

  it("UPM-139 shows a failed source with its reason, and retry takes it through pending to ready", async () => {
    await openFolder("/knowledge/folder-research", "Research");

    const failed = await findRow(/Market scan/);
    expect(within(failed).getByText("Failed")).toBeTruthy();
    expect(within(failed).getByText("Upmo could not fetch that address.")).toBeTruthy();
    expect(within(failed).getByText("https://example.com/market-scan")).toBeTruthy();
    expect(within(failed).getByText("Not yet")).toBeTruthy();

    click(within(failed).getByRole("button", { name: "Retry Market scan" }));

    await waitFor(() => {
      expect(within(row(/Market scan/)).getByText("Pending")).toBeTruthy();
    });
    expect(screen.getByText("1 source is still being added.").getAttribute("role")).toBe("status");
    expect(within(row(/Market scan/)).queryByText("Upmo could not fetch that address.")).toBeNull();
    expect(within(row(/Market scan/)).queryByRole("button", { name: /Retry|Re-ingest/ })).toBeNull();

    await waitFor(() => {
      expect(within(row(/Market scan/)).getByText("Ready")).toBeTruthy();
    }, SETTLE);
    expect(within(row(/Market scan/)).getByText("1")).toBeTruthy();
    expect(screen.queryByText(/still being added/)).toBeNull();
    expect(within(row(/Market scan/)).getByRole("button", { name: "Re-ingest Market scan" })).toBeTruthy();
  }, 10_000);

  it("UPM-139 adds a web page as pending and resolves it without a reload", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    click(await screen.findByRole("tab", { name: "Web page" }));

    enter("Web address", "https://example.com/starter-guide");
    enter("Title (optional)", "Starter guide");
    click(screen.getByRole("button", { name: "Add web page" }));

    const added = await findRow(/Starter guide/);
    expect(within(added).getByText("Pending")).toBeTruthy();
    expect(within(added).getByText("From the web")).toBeTruthy();
    expect(within(added).getByText("Web page")).toBeTruthy();
    expect(screen.getByText("2 sources")).toBeTruthy();
    expect((screen.getByRole("textbox", { name: "Web address" }) as HTMLInputElement).value).toBe("");

    await waitFor(() => {
      expect(within(row(/Starter guide/)).getByText("Ready")).toBeTruthy();
    }, SETTLE);
  }, 10_000);

  it("UPM-139 shows why a web page failed and offers a retry", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    click(await screen.findByRole("tab", { name: "Web page" }));

    enter("Web address", "https://example.com/unreachable");
    click(screen.getByRole("button", { name: "Add web page" }));

    await waitFor(() => {
      expect(within(row(/example\.com/)).getByText("Failed")).toBeTruthy();
    }, SETTLE);
    const failed = row(/example\.com/);
    expect(within(failed).getByText("Upmo could not fetch that address.")).toBeTruthy();
    expect(within(failed).getByRole("button", { name: "Retry example.com" })).toBeTruthy();
  }, 10_000);

  it("UPM-139 refuses an address that is not a web address and keeps what was typed", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    click(await screen.findByRole("tab", { name: "Web page" }));

    enter("Web address", "not a url");
    click(screen.getByRole("button", { name: "Add web page" }));

    expect(await screen.findByText("Enter a web address that starts with http:// or https://.")).toBeTruthy();
    expect((screen.getByRole("textbox", { name: "Web address" }) as HTMLInputElement).value).toBe("not a url");
    expect(screen.getByText("1 source")).toBeTruthy();
  });

  it("UPM-139 adds pasted Markdown as a ready source", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    click(await screen.findByRole("tab", { name: "Markdown" }));

    const add = screen.getByRole("button", { name: "Add Markdown" });
    expect(add.hasAttribute("disabled")).toBe(true);
    enter("Title", "Onboarding");
    expect(add.hasAttribute("disabled")).toBe(true);
    enter("Markdown", "## Day one\n\nCollect your laptop.");
    expect(add.hasAttribute("disabled")).toBe(false);
    click(add);

    const added = await findRow(/Onboarding/);
    expect(within(added).getByText("Ready")).toBeTruthy();
    expect(within(added).getByText("Markdown")).toBeTruthy();
    expect(within(added).getByText("1")).toBeTruthy();
    expect((screen.getByRole("textbox", { name: "Title" }) as HTMLInputElement).value).toBe("");
  });

  it("UPM-139 shows the file size limit and adds a chosen file as pending", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");

    expect(await screen.findByText(/Up to 32 MiB\./)).toBeTruthy();
    const add = screen.getByRole("button", { name: "Add file" });
    expect(add.hasAttribute("disabled")).toBe(true);

    chooseFile(fileOf("Onboarding guide.txt", 2048));
    expect(screen.getByText("Onboarding guide.txt")).toBeTruthy();
    expect(screen.getByText("2 KiB")).toBeTruthy();
    expect(add.hasAttribute("disabled")).toBe(false);
    click(add);

    const added = await findRow(/Onboarding guide/);
    expect(within(added).getByText("Pending")).toBeTruthy();
    expect(within(added).getByText("File")).toBeTruthy();
    expect(screen.queryByText("Clear file")).toBeNull();

    await waitFor(() => {
      expect(within(row(/Onboarding guide/)).getByText("Ready")).toBeTruthy();
    }, SETTLE);
  }, 10_000);

  it("UPM-139 takes a file dropped on the drop area", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    const area = (await screen.findByText("Drop a file here, or choose one.")).closest("div") as HTMLElement;

    fireEvent.drop(area, { dataTransfer: { files: [fileOf("Rota.md", 300, "text/markdown")] } });

    expect(screen.getByText("Rota.md")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add file" }).hasAttribute("disabled")).toBe(false);
  });

  it("UPM-139 refuses a file over the limit and a type the server cannot read", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    const add = await screen.findByRole("button", { name: "Add file" });

    chooseFile(fileOf("Archive.pdf", MAX_FILE_BYTES + 1, "application/pdf"));
    expect((await screen.findByRole("alert")).textContent).toContain("That file is 32.0 MiB. The limit is 32 MiB.");
    expect(add.hasAttribute("disabled")).toBe(true);

    chooseFile(fileOf("setup.exe", 100, "application/octet-stream"));
    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("That file type is not supported.");
    });
    expect(add.hasAttribute("disabled")).toBe(true);

    chooseFile(fileOf("Fits.pdf", MAX_FILE_BYTES, "application/pdf"));
    await waitFor(() => {
      expect(screen.queryByRole("alert")).toBeNull();
    });
    expect(add.hasAttribute("disabled")).toBe(false);
  });

  it("UPM-139 re-ingests a ready source and raises its revision", async () => {
    await openFolder("/knowledge/folder-product", "Product");
    const pricing = await findRow(/Pricing\.md/);

    click(within(pricing).getByRole("button", { name: "Re-ingest Pricing.md" }));

    await waitFor(() => {
      expect(within(row(/Pricing\.md/)).getByText("3")).toBeTruthy();
    });
    expect(within(row(/Pricing\.md/)).getByText("Ready")).toBeTruthy();
  });

  it("UPM-139 deletes a source only after it is confirmed", async () => {
    await openFolder("/knowledge/folder-product", "Product");
    click(within(await findRow(/Pricing\.md/)).getByRole("button", { name: "Delete Pricing.md" }));

    const dialog = await screen.findByRole("dialog", { name: "Delete Pricing.md?" });
    click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    expect(screen.getByText("2 sources")).toBeTruthy();

    click(within(row(/Pricing\.md/)).getByRole("button", { name: "Delete Pricing.md" }));
    click(within(await screen.findByRole("dialog", { name: "Delete Pricing.md?" })).getByRole("button", { name: "Delete source" }));

    await waitFor(() => {
      expect(screen.queryByRole("row", { name: /Pricing\.md/ })).toBeNull();
    });
    expect(screen.getByText("1 source")).toBeTruthy();
  });

  it("UPM-139 shows the empty state once the last source is deleted", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    click(within(await findRow(/Access\.md/)).getByRole("button", { name: "Delete Access.md" }));
    click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete source" }));

    expect(await screen.findByRole("heading", { level: 3, name: "Nothing in this folder yet" })).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Search this folder" })).toBeNull();
  });

  it("UPM-139 searches a folder and names the source of each match", async () => {
    await openFolder("/knowledge/folder-product", "Product");
    await findRow(/Handbook\.pdf/);

    enter("Search query", "holiday");
    click(screen.getByRole("button", { name: "Search" }));

    const results = await screen.findByRole("list", { name: "Search results" });
    const hits = within(results).getAllByRole("listitem");
    expect(hits).toHaveLength(1);
    expect(within(hits[0]!).getByText("Handbook.pdf")).toBeTruthy();
    expect(within(hits[0]!).getByText("Holiday requests go to your lead at least two weeks ahead.")).toBeTruthy();

    enter("Search query", "zebra");
    click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText(/No matches for/)).toBeTruthy();
    expect(screen.queryByRole("list", { name: "Search results" })).toBeNull();
  });

  it("UPM-139 does not offer a search for a blank query", async () => {
    await openFolder("/knowledge/folder-product", "Product");
    const search = await screen.findByRole("button", { name: "Search" });
    expect(search.hasAttribute("disabled")).toBe(true);
    enter("Search query", "   ");
    expect(search.hasAttribute("disabled")).toBe(true);
  });

  it("UPM-139 renames a folder from its page", async () => {
    await openFolder("/knowledge/folder-product", "Product");

    click(screen.getByRole("button", { name: "Rename" }));
    const dialog = await screen.findByRole("dialog", { name: "Rename folder" });
    expect((within(dialog).getByRole("textbox", { name: "Folder name" }) as HTMLInputElement).value).toBe("Product");
    enter("Folder name", "Product docs");
    click(within(dialog).getByRole("button", { name: "Save name" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Product docs" })).toBeTruthy();
    click(screen.getByRole("link", { name: "All folders" }));
    expect(await screen.findByRole("heading", { level: 2, name: "Product docs" })).toBeTruthy();
    expect(screen.queryByRole("heading", { level: 2, name: "Product" })).toBeNull();
  });

  it("UPM-139 deletes a folder after confirming and lands back on the folders", async () => {
    await openFolder("/knowledge/folder-legal", "Legal");

    click(screen.getByRole("button", { name: "Delete folder" }));
    const dialog = await screen.findByRole("dialog", { name: "Delete Legal?" });
    expect(within(dialog).getByText(/2 sources in this folder/)).toBeTruthy();
    click(within(dialog).getByRole("button", { name: "Delete folder" }));

    expect(await screen.findByText("Folders · 5")).toBeTruthy();
    expect(screen.queryByRole("heading", { level: 2, name: "Legal" })).toBeNull();
    expect(screen.queryByText("Folder not found")).toBeNull();
  });

  it("UPM-139 sends one re-ingest for a double click and disables the actions while it runs", async () => {
    let calls = 0;
    let release: () => void = () => {};
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const client = withSources((base) => ({
      async reingest(folderId, sourceId) {
        calls += 1;
        await held;
        return base.reingest(folderId, sourceId);
      },
    }));
    renderAt("/knowledge/folder-product", client);
    const handbook = await findRow(/Handbook\.pdf/);
    const button = within(handbook).getByRole("button", { name: "Re-ingest Handbook.pdf" });

    click(button);
    await pause();
    click(within(row(/Handbook\.pdf/)).getByRole("button", { name: "Re-ingest Handbook.pdf" }));
    await pause();

    expect(calls).toBe(1);
    expect(within(row(/Handbook\.pdf/)).getByRole("button", { name: "Re-ingest Handbook.pdf" }).hasAttribute("disabled")).toBe(true);
    expect(within(row(/Pricing\.md/)).getByRole("button", { name: "Re-ingest Pricing.md" }).hasAttribute("disabled")).toBe(true);

    release();
    await waitFor(() => {
      expect(within(row(/Handbook\.pdf/)).getByRole("button", { name: "Re-ingest Handbook.pdf" }).hasAttribute("disabled")).toBe(false);
    }, SETTLE);
    expect(screen.queryByRole("alert")).toBeNull();
  }, 10_000);

  it("UPM-139 sends one retry for a double click on a failed source", async () => {
    let calls = 0;
    let release: () => void = () => {};
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    const client = withSources((base) => ({
      async reingest(folderId, sourceId) {
        calls += 1;
        await held;
        return base.reingest(folderId, sourceId);
      },
    }));
    renderAt("/knowledge/folder-research", client);
    const failed = await findRow(/Market scan/);

    click(within(failed).getByRole("button", { name: "Retry Market scan" }));
    await pause();
    click(within(row(/Market scan/)).getByRole("button", { name: "Retry Market scan" }));
    await pause();

    expect(calls).toBe(1);
    expect(within(row(/Market scan/)).getByRole("button", { name: "Retry Market scan" }).hasAttribute("disabled")).toBe(true);
    release();
    await waitFor(() => {
      expect(within(row(/Market scan/)).getByText("Pending")).toBeTruthy();
    }, SETTLE);
  }, 10_000);

  it("UPM-139 keeps unsent drafts when switching between the add tabs", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");

    click(await screen.findByRole("tab", { name: "Markdown" }));
    enter("Title", "Onboarding");
    enter("Markdown", "## Day one");
    click(screen.getByRole("tab", { name: "Web page" }));
    enter("Web address", "https://example.com/draft");
    click(screen.getByRole("tab", { name: "File" }));
    chooseFile(fileOf("Draft.txt", 10));
    expect(screen.getByText("Draft.txt")).toBeTruthy();

    click(screen.getByRole("tab", { name: "Markdown" }));
    expect((screen.getByRole("textbox", { name: "Title" }) as HTMLInputElement).value).toBe("Onboarding");
    expect((screen.getByRole("textbox", { name: "Markdown" }) as HTMLTextAreaElement).value).toBe("## Day one");

    click(screen.getByRole("tab", { name: "Web page" }));
    expect((screen.getByRole("textbox", { name: "Web address" }) as HTMLInputElement).value).toBe("https://example.com/draft");

    click(screen.getByRole("tab", { name: "File" }));
    expect(screen.getByText("Draft.txt")).toBeTruthy();
  });

  it("UPM-139 clears the Markdown error as soon as the title or the text changes", async () => {
    const client = withSources(() => ({
      async addMarkdown() {
        throw new Error("The workspace is full.");
      },
    }));
    renderAt("/knowledge/folder-policies", client);
    click(await screen.findByRole("tab", { name: "Markdown" }));

    enter("Title", "Onboarding");
    enter("Markdown", "## Day one");
    click(screen.getByRole("button", { name: "Add Markdown" }));
    expect(await screen.findByText("The workspace is full.")).toBeTruthy();

    enter("Title", "Onboarding guide");
    expect(screen.queryByText("The workspace is full.")).toBeNull();

    click(screen.getByRole("button", { name: "Add Markdown" }));
    expect(await screen.findByText("The workspace is full.")).toBeTruthy();

    enter("Markdown", "## Day one\n\nMore.");
    expect(screen.queryByText("The workspace is full.")).toBeNull();
  });

  it("UPM-139 does not read the sources of a folder that is not in the workspace", async () => {
    const listed: string[] = [];
    const client = withSources((base) => ({
      async list(folderId) {
        listed.push(folderId);
        return base.list(folderId);
      },
    }));
    renderAt("/knowledge/folder-missing", client);
    expect(await screen.findByRole("heading", { level: 1, name: "Folder not found" })).toBeTruthy();
    await pause();
    expect(listed).toEqual([]);
    cleanup();

    renderAt("/knowledge/folder-product", client);
    await screen.findByRole("heading", { level: 1, name: "Product" });
    await findRow(/Pricing\.md/);
    expect(listed).toEqual(["folder-product"]);
  });

  it("UPM-139 keeps the drop area highlighted while the drag moves over its contents", async () => {
    await openFolder("/knowledge/folder-policies", "Policies");
    const label = await screen.findByText("Drop a file here, or choose one.");
    const area = label.closest("div") as HTMLElement;

    fireEvent.dragOver(area);
    expect(area.getAttribute("data-dragging")).toBe("true");

    const inside = createEvent.dragLeave(area);
    Object.defineProperty(inside, "relatedTarget", { value: label });
    fireEvent(area, inside);
    expect(area.getAttribute("data-dragging")).toBe("true");

    const outside = createEvent.dragLeave(area);
    Object.defineProperty(outside, "relatedTarget", { value: document.body });
    fireEvent(area, outside);
    expect(area.hasAttribute("data-dragging")).toBe(false);
  });

  it("UPM-139 says so when the folder is not in the workspace", async () => {
    renderAt("/knowledge/folder-missing");
    expect(await screen.findByRole("heading", { level: 1, name: "Folder not found" })).toBeTruthy();
    click(screen.getByRole("link", { name: "Back to Knowledge" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Knowledge" })).toBeTruthy();
  });
});
