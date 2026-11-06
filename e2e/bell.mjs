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
  const deadline = Date.now() + 10_000;
  while ((await list.count()) !== titles.length) {
    if (Date.now() > deadline) {
      throw new Error(`expected ${titles.length} rows, found ${await list.count()}`);
    }
    await page.waitForTimeout(100);
  }
  const texts = (await list.allInnerTexts()).map((text) => text.replace(/\s+/g, " ").trim());
  // Every listed notification is counted as unread, so each row says so.
  const want = titles.map(([title, meta, action]) => `Unread: ${title} ${meta} ${action}`);
  if (JSON.stringify(texts) !== JSON.stringify(want)) {
    throw new Error(`rows ${JSON.stringify(texts)}, expected ${JSON.stringify(want)}`);
  }
}

async function press(page, title, action) {
  const row = rows(page).filter({ hasText: title });
  await row.getByRole("button", { name: action }).click();
  await row.waitFor({ state: "detached" });
}

async function expectBellFocused(page, name) {
  const bell = page.getByRole("button", { name });
  const deadline = Date.now() + 5_000;
  while (!(await bell.evaluate((node) => node === document.activeElement))) {
    if (Date.now() > deadline) {
      const active = await page.evaluate(() => document.activeElement?.tagName ?? "none");
      throw new Error(`the bell does not have focus after the last row, ${active} does`);
    }
    await page.waitForTimeout(100);
  }
}

// With focus on the bell and nothing left to approve, the popover is closed
// and Tab moves on. An open popover traps focus and swallows Tab when nothing
// inside can take it, which leaves the keyboard user stuck on the bell.
async function expectTabLeavesBell(page, name) {
  await dialog(page).waitFor({ state: "hidden", timeout: 5_000 });
  const bell = page.getByRole("button", { name });
  for (const [key, label] of [["Tab", "Tab"], ["Shift+Tab", "Shift+Tab"]]) {
    // Put focus back on the bell between the two presses.
    await bell.focus();
    await page.keyboard.press(key);
    const state = await bell.evaluate((node) => ({
      onBell: node === document.activeElement,
      active: document.activeElement?.tagName ?? "none",
    }));
    if (state.onBell || state.active === "BODY") {
      throw new Error(`${label} does not leave the bell after the last row, ${state.active} has focus`);
    }
  }
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
  // Stock fixtures: five unread. The approval is approved, the rest dismissed.
  {
    const { context, page } = await newPage(browser);
    await expectUnread(page, 5);
    await openBell(page, "Notifications, 5 unread");
    await expectHeadingFocused(page, "UPM-196 bell");
    await expectRows(page, [
      ["Approve the digest", "Needs your approval", "Approve"],
      ["Question about the budget", "Question", "Dismiss"],
      ["Archive the filed receipts", "Proposal", "Dismiss"],
      ["Mail triage done", "Finished", "Dismiss"],
      ["Weekly close failed", "Failed", "Dismiss"],
    ]);
    if ((await dialog(page).getByRole("button", { name: "Deny" }).count()) !== 0) {
      throw new Error("the bell offers Deny");
    }
    if ((await dialog(page).getByText(/Showing the latest/).count()) !== 0) {
      throw new Error("the bell shows the overflow line with five notifications");
    }

    await press(page, "Approve the digest", "Approve");
    await expectUnread(page, 4);
    await expectRows(page, [
      ["Question about the budget", "Question", "Dismiss"],
      ["Archive the filed receipts", "Proposal", "Dismiss"],
      ["Mail triage done", "Finished", "Dismiss"],
      ["Weekly close failed", "Failed", "Dismiss"],
    ]);
    // Another row is left, so focus moves to its button, not to body.
    await expectRowButtonFocused(page, "Question about the budget");

    await press(page, "Question about the budget", "Dismiss");
    await expectUnread(page, 3);
    await expectRows(page, [
      ["Archive the filed receipts", "Proposal", "Dismiss"],
      ["Mail triage done", "Finished", "Dismiss"],
      ["Weekly close failed", "Failed", "Dismiss"],
    ]);
    await expectRowButtonFocused(page, "Archive the filed receipts");

    // A notification about a task links to it and closes the popover.
    await dialog(page).getByRole("link", { name: "Archive the filed receipts" }).click();
    await page.waitForURL(/\/tasks\/aster-done$/);
    await dialog(page).waitFor({ state: "hidden", timeout: 5_000 });
    await context.close();
  }

  // Dismissing everything leaves the empty state, with focus on the bell.
  {
    const { context, page } = await newPage(browser);
    await openBell(page, "Notifications, 5 unread");
    await press(page, "Approve the digest", "Approve");
    for (const title of [
      "Question about the budget",
      "Archive the filed receipts",
      "Mail triage done",
      "Weekly close failed",
    ]) {
      await press(page, title, "Dismiss");
    }
    // The pressed button is gone with its row, so focus must not fall to body.
    await expectBellFocused(page, "Notifications");
    // Nothing is left, so the popover has closed and Tab moves on.
    await expectTabLeavesBell(page, "Notifications");

    await openBell(page, "Notifications");
    await dialog(page).getByRole("heading", { level: 3, name: "You are up to date" }).waitFor();
    await context.close();
  }

  console.log("bell check passed");
} finally {
  await browser.close();
}
