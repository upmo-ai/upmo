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

  // Code is read only.
  await list.getByRole("button", { name: "over_threshold.py", exact: true }).click();
  const code = page.getByRole("region", { name: "over_threshold.py" });
  await code.getByText("over_threshold", { exact: false }).first().waitFor();
  if ((await code.locator("textarea, input, [contenteditable]").count()) !== 0) {
    fail("the code view is editable");
  }

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
  await bundle.getByText("There is no preview for this kind of file.", { exact: false }).waitFor();
  await bundle.getByRole("link", { name: "Download invoice-bundle.zip" }).waitFor();

  // The row action downloads the file.
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    list.getByRole("button", { name: "Download invoices-oct.csv" }).click(),
  ]);
  if (download.suggestedFilename() !== "invoices-oct.csv") {
    fail(`the download is named ${download.suggestedFilename()}`);
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
