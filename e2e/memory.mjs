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

// The stack's database outlives a run, so the fact carries a run suffix and is
// forgotten again before the check ends.
const suffix = Date.now().toString(36);
const added = `E2E fact ${suffix}: prefers tables over prose.`;
const edited = `E2E fact ${suffix}: prefers short answers.`;

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

async function forgetLeftovers(page) {
  await page.goto(new URL("/personalisation", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 2, name: "Memory" }).waitFor();
  // The facts load after the page renders; count only once they have.
  await page
    .getByRole("table")
    .or(page.getByRole("heading", { level: 3, name: "Nothing remembered yet" }))
    .waitFor();
  const dialog = page.getByRole("dialog", { name: "Forget this fact?" });
  const leftovers = page.getByRole("row", { name: `E2E fact ${suffix}` });
  // An edit is stored as a new fact, so one run can leave more than one row.
  for (let remaining = await leftovers.count(); remaining > 0; remaining = await leftovers.count()) {
    await leftovers.first().getByRole("button", { name: "Forget fact", exact: true }).click();
    await dialog.waitFor();
    await dialog.getByRole("button", { name: "Forget fact", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    await leftovers.nth(remaining - 1).waitFor({ state: "detached" });
  }
}

let page;
let passed = false;

try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 900 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  page = await context.newPage();
  await signInWithPassword(page, url);

  await page.goto(new URL("/personalisation", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Personalisation" }).waitFor();
  await page.getByRole("heading", { level: 2, name: "Memory" }).waitFor();

  // Add: the new fact shows up as a row with the user as its source.
  await page.getByRole("textbox", { name: "Add a fact" }).fill(added);
  await page.getByRole("button", { name: "Add", exact: true }).click();
  const row = page.getByRole("row", { name: added });
  await row.waitFor();
  await row.getByText(/^Added by you/).waitFor();
  await page.getByRole("textbox", { name: "Add a fact" }).evaluate((input) => {
    if (input.value !== "") throw new Error(`add field not cleared: ${input.value}`);
  });

  // On a phone the table keeps its width and scrolls sideways inside a region
  // named like the table, which a keyboard reaches and scrolls. Squeezed
  // narrower, the Fact column has too little room to read and edit in.
  await page.setViewportSize({ width: 360, height: 800 });
  const region = page.getByRole("region", { name: "Remembered facts" });
  await region.waitFor();
  const phone = await region.evaluate((el) => ({
    scrolls: el.scrollWidth > el.clientWidth,
    tabIndex: el.tabIndex,
    tableWidth: Math.round(el.querySelector("table").getBoundingClientRect().width),
  }));
  if (!phone.scrolls || phone.tabIndex !== 0 || phone.tableWidth < 560) {
    throw new Error(`UPM-196 facts table on a phone: ${JSON.stringify(phone)}`);
  }
  await region.focus();
  await page.keyboard.press("ArrowRight");
  await page.waitForFunction(
    (label) => document.querySelector(`[role="region"][aria-label="${label}"]`)?.scrollLeft > 0,
    "Remembered facts",
  );
  await page.setViewportSize({ width: 1280, height: 900 });

  // Cancel leaves the fact as it was.
  await row.getByRole("button", { name: "Edit fact", exact: true }).click();
  // While a row is edited its name no longer matches the fact text, so the
  // edited row is found by the field it holds.
  const field = page.getByRole("textbox", { name: "Fact", exact: true });
  const editing = page.getByRole("row").filter({ has: field });
  await field.waitFor();
  await field.fill("Discarded change.");
  await editing.getByRole("button", { name: "Cancel" }).click();
  await row.getByRole("button", { name: "Edit fact", exact: true }).waitFor();
  await row.getByText(added).waitFor();

  // Edit: saving replaces the text.
  await row.getByRole("button", { name: "Edit fact", exact: true }).click();
  await field.waitFor();
  await field.fill(edited);
  await editing.getByRole("button", { name: "Save" }).click();
  const editedRow = page.getByRole("row", { name: edited });
  await editedRow.waitFor();
  await editedRow.getByRole("button", { name: "Edit fact", exact: true }).waitFor();
  await page.getByRole("row", { name: added }).waitFor({ state: "detached" });

  // The edit is stored, not just shown: it survives a reload.
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByRole("row", { name: edited }).waitFor();

  // Forget: Cancel keeps the fact, the confirm button removes it.
  await page.getByRole("row", { name: edited }).getByRole("button", { name: "Forget fact", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Forget this fact?" });
  await dialog.waitFor();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await dialog.waitFor({ state: "hidden" });
  await page.getByRole("row", { name: edited }).waitFor();

  await page.getByRole("row", { name: edited }).getByRole("button", { name: "Forget fact", exact: true }).click();
  await dialog.waitFor();
  await dialog.getByRole("button", { name: "Forget fact", exact: true }).click();
  await page.getByRole("row", { name: edited }).waitFor({ state: "detached" });

  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { level: 2, name: "Memory" }).waitFor();
  const hasFacts = page.getByRole("table");
  const empty = page.getByRole("heading", { level: 3, name: "Nothing remembered yet" });
  await hasFacts.or(empty).waitFor();
  if ((await page.getByRole("row", { name: edited }).count()) !== 0) {
    throw new Error("forgotten fact came back after a reload");
  }

  passed = true;
  console.log("memory flow passed");
} finally {
  // A failed run must not leave its fact in the shared account's memory.
  if (!passed && page) {
    try {
      await forgetLeftovers(page);
    } catch (err) {
      console.error(`cleanup failed, forget "E2E fact ${suffix}" by hand: ${err}`);
    }
  }
  await browser.close();
}
