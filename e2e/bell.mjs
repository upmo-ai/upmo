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
  const want = titles.map(([title, approve]) => `Unread: ${approve ? `${title} Approve` : title}`);
  if (JSON.stringify(texts) !== JSON.stringify(want)) {
    throw new Error(`rows ${JSON.stringify(texts)}, expected ${JSON.stringify(want)}`);
  }
}

async function approve(page, title) {
  const row = rows(page).filter({ hasText: title });
  await row.getByRole("button", { name: "Approve" }).click();
  await row.waitFor({ state: "detached" });
}

async function expectBellFocused(page, name) {
  const bell = page.getByRole("button", { name });
  const deadline = Date.now() + 5_000;
  while (!(await bell.evaluate((node) => node === document.activeElement))) {
    if (Date.now() > deadline) {
      const active = await page.evaluate(() => document.activeElement?.tagName ?? "none");
      throw new Error(`the bell does not have focus after the last approval, ${active} does`);
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
      throw new Error(`${label} does not leave the bell after the last approval, ${state.active} has focus`);
    }
  }
}

// Focus lands on the popover heading, not on the first Approve button: a key
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

async function expectApproveFocused(page, title) {
  const button = rows(page).filter({ hasText: title }).getByRole("button", { name: "Approve" });
  const deadline = Date.now() + 5_000;
  while (!(await button.evaluate((node) => node === document.activeElement))) {
    if (Date.now() > deadline) {
      const active = await page.evaluate(() => document.activeElement?.tagName ?? "none");
      throw new Error(`the Approve button of ${JSON.stringify(title)} does not have focus, ${active} does`);
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
  // Stock fixtures: four unread, two of them need approval.
  {
    const { context, page } = await newPage(browser);
    await expectUnread(page, 4);
    await openBell(page, "Notifications, 4 unread");
    await expectHeadingFocused(page, "UPM-196 bell");
    await expectRows(page, [
      ["Approve the digest", true],
      ["Question about the budget", true],
      ["Mail triage done", false],
      ["Weekly close failed", false],
    ]);
    if ((await dialog(page).getByRole("button", { name: "Deny" }).count()) !== 0) {
      throw new Error("the bell offers Deny");
    }

    await approve(page, "Approve the digest");
    await expectUnread(page, 3);
    await expectRows(page, [
      ["Question about the budget", true],
      ["Mail triage done", false],
      ["Weekly close failed", false],
    ]);
    // Another approval is left, so focus moves to its button, not to body.
    await expectApproveFocused(page, "Question about the budget");

    await approve(page, "Question about the budget");
    await expectUnread(page, 2);
    // The pressed button is gone with its row, so focus must not fall to body.
    await expectBellFocused(page, "Notifications, 2 unread");
    // Nothing is left to approve, so the popover has closed and Tab moves on.
    await expectTabLeavesBell(page, "Notifications, 2 unread");

    // Opening it again lists the two finished tasks.
    await openBell(page, "Notifications, 2 unread");
    await expectRows(page, [
      ["Mail triage done", false],
      ["Weekly close failed", false],
    ]);
    await context.close();
  }

  console.log("bell check passed");
} finally {
  await browser.close();
}
