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

// What e2e/seed-tasks.sql puts in the e2e person's bell: the two pending
// approvals on top (named by their tasks' goals) and four notifications, none
// dismissed. The focus handling with the sample data is covered by the web
// tests; this one reads the live brain, which e2e/flows.mjs then decides on,
// so nothing here approves.
const seeded = [
  ["Draft the renewal notice", "Needs your approval", "Approve"],
  ["Email the supplier statements", "Needs your approval", "Approve"],
  ["Renew the domain before Friday?", "Question", "Dismiss"],
  ["Archive the October statements", "Proposal", "Dismiss"],
  ["The accounts are reconciled", "Finished", "Dismiss"],
  ["The weekly close failed", "Failed", "Dismiss"],
];

async function openBell(page, name) {
  await page.getByRole("button", { name }).click();
  // The popover animates in; the list or empty state is the positive signal.
  await dialog(page).waitFor();
}

// The label and the badge come from the same count, so they must agree. The
// badge is hidden from assistive tech, so it is read from the button's text. A
// missing badge leaves the text empty and fails here, which keeps it from
// passing as "no unread".
async function expectUnread(page, count) {
  const bell = page.getByRole("button", { name: `Notifications, ${count} unread` });
  await bell.waitFor();
  const text = (await bell.textContent())?.trim();
  if (text !== String(count)) {
    throw new Error(`badge shows ${JSON.stringify(text)}, expected ${JSON.stringify(String(count))}`);
  }
}

function dialog(page) {
  return page.getByRole("dialog", { name: /^Notifications/ });
}

function rows(page) {
  return dialog(page).getByRole("list", { name: "Notifications" }).getByRole("listitem");
}

async function expectRows(page, titles) {
  const list = rows(page);
  // Every listed notification is counted as unread, so each row says so.
  const want = titles.map(([title, meta, action]) => `Unread: ${title} ${meta} ${action}`);
  const deadline = Date.now() + 10_000;
  let texts = [];
  while (Date.now() < deadline) {
    texts = (await list.allInnerTexts()).map((text) => text.replace(/\s+/g, " ").trim());
    if (JSON.stringify(texts) === JSON.stringify(want)) return;
    await page.waitForTimeout(100);
  }
  throw new Error(`rows ${JSON.stringify(texts)}, expected ${JSON.stringify(want)}`);
}

async function press(page, title, action) {
  const row = rows(page).filter({ hasText: title });
  await row.getByRole("button", { name: action }).click();
  await row.waitFor({ state: "detached" });
}

// Focus lands on the popover heading, not on the first row's button: a key
// held down on the bell must not press an approval.
async function expectHeadingFocused(page, label) {
  const heading = dialog(page).getByRole("heading", { level: 2, name: "Notifications" });
  const deadline = Date.now() + 5_000;
  while (!(await heading.evaluate((node) => node === document.activeElement))) {
    if (Date.now() > deadline) {
      const active = await page.evaluate(() => {
        const node = document.activeElement;
        return node ? `${node.tagName} ${node.textContent?.trim().slice(0, 40)}` : "none";
      });
      throw new Error(`${label}: the heading does not have focus on open, ${active} does`);
    }
    await page.waitForTimeout(100);
  }
}

async function expectRowButtonFocused(page, title) {
  const button = rows(page).filter({ hasText: title }).getByRole("button");
  const deadline = Date.now() + 5_000;
  while (!(await button.evaluate((node) => node === document.activeElement))) {
    if (Date.now() > deadline) {
      const active = await page.evaluate(() => document.activeElement?.tagName ?? "none");
      throw new Error(`the button of ${JSON.stringify(title)} does not have focus, ${active} does`);
    }
    await page.waitForTimeout(100);
  }
}

async function newPage(browser) {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 900 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  // The shell renders Home only once /api/session answers, so wait for Home or
  // the login form before deciding whether a password sign-in is needed.
  await page.goto(new URL("/", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  const home = page.getByRole("heading", { level: 1, name: "Home" });
  await home.or(page.getByLabel("Email")).waitFor({ timeout: 30_000 });
  if (!(await home.isVisible())) {
    await signInWithPassword(page, url);
  }
  return { context, page };
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  // The live brain: the seeded notifications and the pending approvals, the
  // approvals on top.
  {
    const { context, page } = await newPage(browser);
    await expectUnread(page, seeded.length);
    await openBell(page, `Notifications, ${seeded.length} unread`);
    await expectHeadingFocused(page, "UPM-196 bell");
    await expectRows(page, seeded);
    // The bell decides approvals; it never offers Deny.
    if ((await dialog(page).getByRole("button", { name: "Deny" }).count()) !== 0) {
      throw new Error("the bell offers Deny");
    }

    // A notification about a task links to it, and the task page renders.
    await dialog(page).getByRole("link", { name: "The accounts are reconciled" }).click();
    await page.waitForURL(/\/tasks\/e2e00000-0000-7000-8000-000000000006$/);
    await dialog(page).waitFor({ state: "hidden", timeout: 5_000 });
    await page.getByRole("heading", { level: 1, name: "Reconcile the accounts" }).waitFor();
    await page.waitForFunction(
      () => document.querySelector(".task-header .upmo-status-badge")?.innerText.trim() === "Done",
    );

    // Dismissing one notification: the row leaves, focus moves to the next row,
    // and the dismissal survives a reload.
    await openBell(page, `Notifications, ${seeded.length} unread`);
    await press(page, "Renew the domain before Friday?", "Dismiss");
    await expectUnread(page, seeded.length - 1);
    await expectRowButtonFocused(page, "Archive the October statements");

    await page.reload();
    await page.getByRole("heading", { level: 1, name: "Reconcile the accounts" }).waitFor();
    await expectUnread(page, seeded.length - 1);
    await openBell(page, `Notifications, ${seeded.length - 1} unread`);
    await expectRows(page, seeded.filter(([title]) => title !== "Renew the domain before Friday?"));
    await context.close();
  }

  console.log("bell check passed");
} finally {
  await browser.close();
}
