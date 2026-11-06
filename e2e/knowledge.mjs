import { createRequire } from "node:module";
import { signInWithPassword, storagePath } from "./session.mjs";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

// The knowledge screens read the in-memory API, so the state lives in the page.
// Every step stays in one page load: a reload would start the folders over.
const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

async function expectGone(locator, what) {
  if ((await locator.count()) !== 0) {
    throw new Error(`UPM-139 ${what} is still on the page`);
  }
}

try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 900 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await signInWithPassword(page, url);

  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Knowledge", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Knowledge" }).waitFor();
  await page.getByText("Folders · 6", { exact: true }).waitFor();

  // New folder: a name is needed, then the empty folder opens.
  await page.getByRole("button", { name: "New folder" }).click();
  const naming = page.getByRole("dialog", { name: "New folder" });
  if (await naming.getByRole("button", { name: "Create folder" }).isEnabled()) {
    throw new Error("UPM-139 Create folder was enabled without a name");
  }
  await naming.getByRole("textbox", { name: "Folder name" }).fill("E2E notes");
  await naming.getByRole("button", { name: "Create folder" }).click();
  await page.getByRole("heading", { level: 1, name: "E2E notes" }).waitFor();
  await page.getByRole("heading", { level: 3, name: "Nothing in this folder yet" }).waitFor();

  // Markdown paste is ready at once.
  await page.getByRole("tab", { name: "Markdown" }).click();
  await page.getByRole("textbox", { name: "Title", exact: true }).fill("Standup");
  await page.getByRole("textbox", { name: "Markdown", exact: true }).fill("Standup is at nine.\n\nBring the ledger.");
  await page.getByRole("button", { name: "Add Markdown" }).click();
  const standup = page.getByRole("row", { name: /Standup/ });
  await standup.getByText("Ready", { exact: true }).waitFor();
  await expectGone(page.getByRole("heading", { level: 3, name: "Nothing in this folder yet" }), "the empty folder state");

  // File: the limit is shown, an oversize file is refused, a small one goes pending, then ready.
  await page.getByRole("tab", { name: "File" }).click();
  await page.getByText("Up to 32 MiB.").waitFor();
  const fileInput = page.locator("input[type='file']");
  await fileInput.setInputFiles({
    name: "Archive.pdf",
    mimeType: "application/pdf",
    buffer: Buffer.alloc(32 * 1024 * 1024 + 1),
  });
  await page.getByRole("alert").getByText(/The limit is 32 MiB\./).waitFor();
  if (await page.getByRole("button", { name: "Add file" }).isEnabled()) {
    throw new Error("UPM-139 Add file was enabled for a file over the limit");
  }
  await page.getByRole("button", { name: "Clear file" }).click();
  await fileInput.setInputFiles({
    name: "Rota.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Alice has the Monday shift."),
  });
  await page.getByRole("button", { name: "Add file" }).click();
  const rota = page.getByRole("row", { name: /Rota/ });
  await rota.getByText("Pending", { exact: true }).waitFor();
  await page.getByText("1 source is still being added.").waitFor();
  await rota.getByText("Ready", { exact: true }).waitFor({ timeout: 15_000 });
  await expectGone(page.getByText("1 source is still being added."), "the pending note");

  // Web page: an address that cannot be fetched ends failed with the reason and a retry.
  await page.getByRole("tab", { name: "Web page" }).click();
  await page.getByRole("textbox", { name: "Web address" }).fill("https://example.com/unreachable");
  await page.getByRole("button", { name: "Add web page" }).click();
  const web = page.getByRole("row", { name: /example\.com/ });
  await web.getByText("Pending", { exact: true }).waitFor();
  await web.getByText("Failed", { exact: true }).waitFor({ timeout: 15_000 });
  await web.getByText("Upmo could not fetch that address.").waitFor();
  await web.getByRole("button", { name: "Retry example.com" }).waitFor();

  // Search names the source of each match.
  await page.getByRole("textbox", { name: "Search query" }).fill("ledger");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  const hit = page.getByRole("list", { name: "Search results" }).getByRole("listitem");
  await hit.getByText("Bring the ledger.").waitFor();
  await hit.getByText("Standup", { exact: true }).waitFor();

  // Delete a source: Cancel keeps it, the confirm button removes it.
  await standup.getByRole("button", { name: "Delete Standup" }).click();
  const confirmSource = page.getByRole("dialog", { name: "Delete Standup?" });
  await confirmSource.getByRole("button", { name: "Cancel" }).click();
  await confirmSource.waitFor({ state: "hidden" });
  await standup.waitFor();
  await standup.getByRole("button", { name: "Delete Standup" }).click();
  await confirmSource.getByRole("button", { name: "Delete source" }).click();
  await standup.waitFor({ state: "detached" });

  // Rename, then delete the folder: back on the list with the original six.
  await page.getByRole("button", { name: "Rename" }).click();
  const renaming = page.getByRole("dialog", { name: "Rename folder" });
  await renaming.getByRole("textbox", { name: "Folder name" }).fill("E2E notes renamed");
  await renaming.getByRole("button", { name: "Save name" }).click();
  await page.getByRole("heading", { level: 1, name: "E2E notes renamed" }).waitFor();

  await page.getByRole("button", { name: "Delete folder" }).click();
  await page
    .getByRole("dialog", { name: "Delete E2E notes renamed?" })
    .getByRole("button", { name: "Delete folder" })
    .click();
  await page.getByRole("heading", { level: 1, name: "Knowledge" }).waitFor();
  await page.getByText("Folders · 6", { exact: true }).waitFor();
  await expectGone(page.getByRole("link", { name: "E2E notes renamed" }), "the deleted folder");

  // A failed source from the folders retries to ready.
  await page.getByRole("link", { name: "Research" }).click();
  await page.getByRole("heading", { level: 1, name: "Research" }).waitFor();
  const market = page.getByRole("row", { name: /Market scan/ });
  await market.getByText("Failed", { exact: true }).waitFor();
  await market.getByRole("button", { name: "Retry Market scan" }).click();
  await market.getByText("Pending", { exact: true }).waitFor();
  await market.getByText("Ready", { exact: true }).waitFor({ timeout: 15_000 });

  // On a phone the sources table scrolls sideways inside a named region.
  await page.setViewportSize({ width: 360, height: 800 });
  const region = page.getByRole("region", { name: "Sources" });
  await region.waitFor();
  const phone = await region.evaluate((el) => ({
    scrolls: el.scrollWidth > el.clientWidth,
    tabIndex: el.tabIndex,
  }));
  if (!phone.scrolls || phone.tabIndex !== 0) {
    throw new Error(`UPM-139 sources table on a phone: ${JSON.stringify(phone)}`);
  }

  console.log("knowledge flow passed");
} finally {
  await browser.close();
}
