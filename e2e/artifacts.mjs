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

// The task screens read the in-memory API, so the state lives in the page.
const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

function fail(message) {
  throw new Error(`UPM-74 ${message}`);
}

function fail77(message) {
  throw new Error(`UPM-77 ${message}`);
}

// Monaco draws a space as a no-break space, so compare with plain spaces.
async function editorText(locator) {
  return (await locator.innerText()).replace(/\u00a0/g, " ");
}

// Waits for an editor to show `needle`. The text is drawn after the editor mounts.
async function expectShown(page, locator, needle, what) {
  const deadline = Date.now() + 15_000;
  let last = "";
  while (Date.now() < deadline) {
    last = await editorText(locator);
    if (last.includes(needle)) return;
    await page.waitForTimeout(100);
  }
  fail77(`${what} does not show ${JSON.stringify(needle)}, it shows ${JSON.stringify(last.slice(0, 200))}`);
}

// Waits for an editor to show no text at all.
async function expectBlank(page, locator, what) {
  const deadline = Date.now() + 15_000;
  let last = "";
  while (Date.now() < deadline) {
    last = (await editorText(locator)).trim();
    if (last === "") return;
    await page.waitForTimeout(100);
  }
  fail77(`${what} should be empty, it shows ${JSON.stringify(last.slice(0, 200))}`);
}

// Types into the editor and checks the text did not change: it is read only.
async function expectReadOnly(page, region, lines, what) {
  const inputs = region.locator("textarea.inputarea");
  const count = await inputs.count();
  if (count === 0) fail77(`${what} has no editor input`);
  for (let index = 0; index < count; index += 1) {
    if (!(await inputs.nth(index).evaluate((input) => input.readOnly))) {
      fail77(`${what} has an input that can be edited`);
    }
  }
  const before = await editorText(lines.first());
  await lines.first().click();
  await page.keyboard.type("zzz-typed");
  await page.keyboard.press("Backspace");
  await page.keyboard.press("Enter");
  if ((await editorText(lines.first())) !== before) {
    fail77(`${what} changed when typed into`);
  }
}

// The colour a design token has on the page now, as a computed colour.
function tokenColour(page, token) {
  return page.evaluate((name) => {
    const probe = document.createElement("div");
    probe.style.backgroundColor = `var(${name})`;
    document.body.appendChild(probe);
    const value = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return value;
  }, token);
}

function editorBackground(region) {
  return region
    .locator(".monaco-editor-background")
    .first()
    .evaluate((node) => getComputedStyle(node).backgroundColor);
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

  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  await page.getByRole("link", { name: "Daily finance digest" }).click();
  await page.getByRole("heading", { level: 1, name: "Daily finance digest" }).waitFor();

  // The tab lists what the task made, with name, kind, size and created.
  await page.getByRole("tab", { name: "Artifacts" }).click();
  const list = page.getByRole("table", { name: "Artifacts" });
  await list.waitFor();
  const headers = (await list.getByRole("columnheader").allInnerTexts()).map((text) => text.trim());
  if (headers.join("|") !== "Name|Kind|Size|Created|Actions") {
    fail(`artifact columns are ${JSON.stringify(headers)}`);
  }
  if ((await list.getByRole("row").count()) !== 9) {
    fail("the digest task should list 8 artifacts");
  }

  // The list is oldest first and the oldest file opens.
  await page.getByRole("region", { name: "run-notes.txt" }).waitFor();
  const firstRow = list.getByRole("row").nth(1);
  if (!(await firstRow.getByRole("button", { name: "run-notes.txt", exact: true }).count())) {
    fail("the oldest artifact should be the first row");
  }

  // The oldest file is plain text and opens in the editor, read only.
  const notes = page.getByRole("region", { name: "run-notes.txt" });
  await notes.locator(".monaco-editor").waitFor();
  await expectShown(page, notes.locator(".view-lines"), "Fetched 31 invoices from the finance mailbox.", "the text file");
  await expectReadOnly(page, notes, notes.locator(".view-lines"), "the text file");

  // Markdown is drawn as elements.
  await list.getByRole("button", { name: "digest-draft.md", exact: true }).click();
  const markdown = page.getByRole("region", { name: "digest-draft.md" });
  await markdown.getByRole("heading", { name: "October invoices over threshold" }).waitFor();
  await markdown.getByRole("link", { name: "finance policy (opens in a new tab)" }).waitFor();

  // An image shows in an img that decoded.
  await list.getByRole("button", { name: "spend-chart.png", exact: true }).click();
  const picture = page.getByRole("region", { name: "spend-chart.png" }).getByRole("img", { name: "spend-chart.png" });
  await picture.waitFor();
  if (!(await picture.evaluate((img) => img.complete && img.naturalWidth > 0))) {
    fail("the image did not load");
  }

  // CSV is a table, and a quoted comma stays in its cell.
  await list.getByRole("button", { name: "invoices-oct.csv", exact: true }).click();
  const sheet = page.getByRole("table", { name: "invoices-oct.csv as a table" });
  await sheet.getByRole("cell", { name: "€2,140.00", exact: true }).waitFor();

  // Code opens in the editor: highlighted, read only, with its text.
  await list.getByRole("button", { name: "over_threshold.py", exact: true }).click();
  const code = page.getByRole("region", { name: "over_threshold.py" });
  await code.locator(".monaco-editor").waitFor();
  const codeLines = code.locator(".view-lines");
  await expectShown(page, codeLines, "def over_threshold(invoices, limit=1000):", "the code file");
  const tokenClasses = () =>
    code.locator(".view-line span span").evaluateAll((spans) => new Set(spans.map((span) => span.className)).size);
  let colourClasses = await tokenClasses();
  for (let tries = 0; colourClasses < 3 && tries < 20; tries += 1) {
    await page.waitForTimeout(500);
    colourClasses = await tokenClasses();
  }
  if (colourClasses < 3) {
    fail77(`the code file is not highlighted, it has ${colourClasses} token classes`);
  }
  await expectReadOnly(page, code, codeLines, "the code file");
  if ((await code.locator(".monaco-editor").count()) !== 1) {
    fail77("the code file should have one editor");
  }

  // The editor follows the app theme.
  const themeSwitch = page.getByTestId("theme-switch");
  const lightBackground = await editorBackground(code);
  if (lightBackground !== (await tokenColour(page, "--surface-200"))) {
    fail77(`the light editor background ${lightBackground} is not --surface-200`);
  }
  await themeSwitch.getByText("Dark", { exact: true }).click();
  {
    const deadline = Date.now() + 15_000;
    let dark = lightBackground;
    while (Date.now() < deadline && dark === lightBackground) {
      await page.waitForTimeout(100);
      dark = await editorBackground(code);
    }
    if (dark === lightBackground || dark !== (await tokenColour(page, "--surface-200"))) {
      fail77(`the dark editor background ${dark} did not follow --surface-200`);
    }
  }
  await themeSwitch.getByText("Light", { exact: true }).click();

  // HTML shows only in a sandboxed iframe, and its script did not run.
  await list.getByRole("button", { name: "digest-preview.html", exact: true }).click();
  const frameElement = page.getByTitle("digest-preview.html preview");
  await frameElement.waitFor();
  const sandbox = await frameElement.getAttribute("sandbox");
  if (sandbox === null || sandbox.includes("allow-")) {
    fail(`the HTML preview sandbox is ${JSON.stringify(sandbox)}`);
  }
  const frame = await (await frameElement.elementHandle())?.contentFrame();
  if (!frame) fail("the HTML preview has no frame");
  await frame.getByRole("heading", { name: "Finance digest" }).waitFor();
  if ((await frame.title()) === "changed") {
    fail("the script in the HTML preview ran");
  }
  if ((await page.title()) === "changed") {
    fail("the script in the HTML preview reached the page");
  }

  // A kind with no viewer is a download.
  await list.getByRole("button", { name: "invoice-bundle.zip", exact: true }).click();
  const bundle = page.getByRole("region", { name: "invoice-bundle.zip" });
  await bundle.getByText("There is no preview for this kind of file", { exact: false }).waitFor();
  await bundle.getByRole("link", { name: "Download invoice-bundle.zip" }).waitFor();

  // The row action downloads the file.
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    list.getByRole("button", { name: "Download invoices-oct.csv" }).click(),
  ]);
  if (download.suggestedFilename() !== "invoices-oct.csv") {
    fail(`the download is named ${download.suggestedFilename()}`);
  }

  // A diff opens in the diff editor, one tab per changed file, side by side on a wide screen.
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("link", { name: "Mail triage" }).click();
  await page.getByRole("heading", { level: 1, name: "Mail triage" }).waitFor();
  await page.getByRole("tab", { name: "Artifacts" }).click();
  const mailList = page.getByRole("table", { name: "Artifacts" });
  await mailList.waitFor();
  await mailList.getByRole("button", { name: "triage-rules.patch", exact: true }).click();
  const patch = page.getByRole("region", { name: "triage-rules.patch" });
  const diffEditor = patch.locator(".monaco-diff-editor");
  await diffEditor.waitFor();
  if (!(await diffEditor.evaluate((node) => node.classList.contains("side-by-side")))) {
    fail77("the diff should be side by side on a wide screen");
  }
  const tabs = patch.getByRole("tablist", { name: "Changed files" });
  const tabNames = (await tabs.getByRole("tab").allInnerTexts()).map((text) => text.trim());
  const expectedTabs = ["triage.py", "rules/invoices.yaml", "rules/receipts.yaml (added)", "rules/legacy.yaml (deleted)"];
  if (tabNames.join("|") !== expectedTabs.join("|")) {
    fail77(`the diff tabs are ${JSON.stringify(tabNames)}`);
  }
  const before = patch.locator(".editor.original .view-lines");
  const after = patch.locator(".editor.modified .view-lines");
  await expectShown(page, after, 'startswith("receipt")', "the changed side of triage.py");
  await expectShown(page, before, 'return "review"', "the original side of triage.py");
  if ((await editorText(before)).includes("receipt")) {
    fail77("the original side of triage.py shows the new line");
  }
  try {
    await patch.locator(".line-insert, .char-insert").first().waitFor({ timeout: 15_000 });
  } catch {
    fail77("the diff marks no added lines");
  }
  await expectReadOnly(page, patch, after, "the diff");

  // An added file is shown against nothing, a deleted file against nothing.
  await tabs.getByRole("tab", { name: "rules/receipts.yaml (added)" }).click();
  await expectShown(page, after, "subject: receipt", "the added file");
  await expectBlank(page, before, "the original side of the added file");
  await tabs.getByRole("tab", { name: "rules/legacy.yaml (deleted)" }).click();
  await expectShown(page, before, "accounts@", "the deleted file");
  await expectBlank(page, after, "the changed side of the deleted file");
  await tabs.getByRole("tab", { name: "rules/invoices.yaml" }).click();
  await expectShown(page, after, "label: invoice", "the changed yaml file");

  // On a phone the same diff is inline, with a select instead of tabs.
  await page.setViewportSize({ width: 390, height: 844 });
  const picker = patch.getByRole("combobox", { name: "Changed file" });
  await picker.waitFor();
  if ((await patch.getByRole("tablist").count()) !== 0) {
    fail77("a phone should not show the file tabs");
  }
  {
    const deadline = Date.now() + 15_000;
    while (Date.now() < deadline && (await diffEditor.evaluate((node) => node.classList.contains("side-by-side")))) {
      await page.waitForTimeout(100);
    }
    if (await diffEditor.evaluate((node) => node.classList.contains("side-by-side"))) {
      fail77("the diff should be inline on a phone");
    }
  }
  if ((await picker.inputValue()) !== "rules/invoices.yaml") {
    fail77("the picked file should stay picked when the screen narrows");
  }
  await picker.click();
  await page.getByRole("option", { name: "triage.py", exact: true }).click();
  await expectShown(page, patch.locator(".view-lines").last(), 'startswith("receipt")', "the inline diff");
  await page.setViewportSize({ width: 1280, height: 900 });

  // A file whose content is missing says so and offers the download, with no editor.
  await mailList.getByRole("button", { name: "rules-export.json", exact: true }).click();
  const missing = page.getByRole("region", { name: "rules-export.json" });
  await missing.getByText("This file is missing or could not be read.", { exact: false }).waitFor();
  await missing.getByRole("link", { name: "Download rules-export.json" }).waitFor();
  if ((await missing.locator(".monaco-editor").count()) !== 0) {
    fail77("a missing file should not show an editor");
  }

  // A task with no artifacts says so.
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("link", { name: "Weekly close" }).click();
  await page.getByRole("heading", { level: 1, name: "Weekly close" }).waitFor();
  await page.getByRole("tab", { name: "Artifacts" }).click();
  await page.getByRole("heading", { level: 2, name: "No artifacts yet" }).waitFor();
} finally {
  await browser.close();
}
