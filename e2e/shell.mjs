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

const desktop = { width: 1280, height: 800 };
const tablet = { width: 800, height: 800 };
const phone = { width: 360, height: 800 };

function rail(page) {
  return page.locator(".upmo-app-frame__rail");
}

function drawer(page) {
  return page.getByRole("dialog", { name: "Navigation" });
}

// The size tokens the frame reads: "248px" for --sidebar, "56px" for --rail.
async function tokenWidth(page, name) {
  const value = await page.evaluate(
    (token) => getComputedStyle(document.documentElement).getPropertyValue(token).trim(),
    name,
  );
  const width = Number.parseFloat(value);
  if (!value.endsWith("px") || Number.isNaN(width)) {
    throw new Error(`${name} was ${JSON.stringify(value)}, expected a pixel width`);
  }
  return width;
}

async function until(label, read, expected, timeout = 5000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await read();
    if (last === expected) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`${label} was ${JSON.stringify(last)}, expected ${JSON.stringify(expected)}`);
}

async function railWidth(page) {
  return rail(page).evaluate((el) => Math.round(el.getBoundingClientRect().width));
}

async function isRail(page) {
  return rail(page).evaluate((el) => el.hasAttribute("data-rail"));
}

async function expectRail(page, label, on, token) {
  await until(`${label} rail state`, () => isRail(page), on);
  await until(`${label} width`, () => railWidth(page), await tokenWidth(page, token));
}

async function resize(page, size) {
  await page.setViewportSize(size);
}

// Rail and drawer both hold the navigation at tablet width. Only the one in
// the open drawer may reach the accessibility tree, so count the unignored
// navigation nodes in the browser's own tree rather than asking Playwright,
// which does not treat `inert` as hidden.
async function expectOneNavigation(page, label) {
  const client = await page.context().newCDPSession(page);
  try {
    const { nodes } = await client.send("Accessibility.getFullAXTree");
    const landmarks = nodes.filter((node) => !node.ignored && node.role?.value === "navigation");
    if (landmarks.length !== 1) {
      const names = landmarks.map((node) => node.name?.value ?? "");
      throw new Error(
        `${landmarks.length} navigation landmarks exposed ${label} (${JSON.stringify(names)}), expected 1`,
      );
    }
  } finally {
    await client.detach();
  }
}

async function drawerWidth(page) {
  return drawer(page).evaluate((el) => Math.round(el.getBoundingClientRect().width));
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: desktop,
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await signInWithPassword(page, url);

  // From md: the full sidebar, collapsed to the rail by the top bar toggle and
  // by Ctrl+B.
  await resize(page, desktop);
  await expectRail(page, "desktop", false, "--sidebar");
  const banner = page.getByRole("banner");
  await banner.getByRole("button", { name: "Collapse navigation" }).click();
  await expectRail(page, "collapsed desktop", true, "--rail");
  await banner.getByRole("button", { name: "Expand navigation" }).click();
  await expectRail(page, "expanded desktop", false, "--sidebar");
  await page.keyboard.press("Control+b");
  await expectRail(page, "desktop after Ctrl+B", true, "--rail");
  await page.keyboard.press("Control+b");
  await expectRail(page, "desktop after a second Ctrl+B", false, "--sidebar");

  // Between sm and md: the icon rail, with its labels as tooltips.
  await resize(page, tablet);
  await expectRail(page, "tablet", true, "--rail");
  await page.getByRole("link", { name: "Home" }).hover();
  const tooltip = page.getByRole("tooltip", { name: "Home" });
  await tooltip.waitFor({ timeout: 5000 });
  const tip = (await tooltip.innerText()).trim();
  if (tip !== "Home") {
    throw new Error(`rail tooltip was ${JSON.stringify(tip)}, expected Home`);
  }
  await page.mouse.move(400, 400);

  // The same width opens the full navigation as a drawer.
  await page.getByRole("button", { name: "Open navigation" }).click();
  await drawer(page).waitFor({ timeout: 5000 });
  await until("tablet drawer width", () => drawerWidth(page), await tokenWidth(page, "--sidebar"));
  await expectOneNavigation(page, "with the rail and the drawer open at tablet width");
  await page.keyboard.press("Escape");
  await drawer(page).waitFor({ state: "hidden", timeout: 5000 });

  // Below sm: no rail, and the drawer is a dialog named Navigation.
  await resize(page, phone);
  await until("phone rail count", () => rail(page).count(), 0);
  if (await drawer(page).isVisible()) {
    throw new Error("phone drawer started open");
  }
  await page.getByRole("button", { name: "Open navigation" }).click();
  await drawer(page).waitFor({ timeout: 5000 });
  await until("phone drawer width", () => drawerWidth(page), await tokenWidth(page, "--sidebar"));
  await expectOneNavigation(page, "with the drawer open on a phone");
  await drawer(page).getByRole("link", { name: "Tasks" }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  await drawer(page).waitFor({ state: "hidden", timeout: 5000 });

  console.log("shell check passed");
} finally {
  await browser.close();
}
