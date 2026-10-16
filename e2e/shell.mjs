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

function navbar(page) {
  return page.locator("nav.shell-navbar");
}

async function navbarWidth(page) {
  return navbar(page).evaluate((el) => Math.round(el.getBoundingClientRect().width));
}

async function drawerOpen(page) {
  return navbar(page).evaluate((el) => {
    const rect = el.getBoundingClientRect();
    return rect.left >= -1 && rect.width > 8 && rect.right > 8;
  });
}

async function waitForLayout(page, layout) {
  await page.locator(`nav.shell-navbar[data-layout="${layout}"]`).waitFor({ timeout: 5000 });
}

async function expectWidth(page, layout, width) {
  await page.setViewportSize({ width: layout === "full" ? 1280 : layout === "rail" ? 800 : 360, height: 800 });
  await waitForLayout(page, layout);
  const deadline = Date.now() + 5000;
  let last = -1;
  while (Date.now() < deadline) {
    last = await navbarWidth(page);
    if (last === width) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`${layout} sidebar width ${last}, expected ${width}`);
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await signInWithPassword(page, url);

  await expectWidth(page, "full", 248);

  await expectWidth(page, "rail", 56);
  await page.getByRole("link", { name: "Home" }).hover();
  const tooltip = page.getByRole("tooltip", { name: "Home" });
  await tooltip.waitFor({ timeout: 5000 });
  const tip = (await tooltip.innerText()).trim();
  if (tip !== "Home") {
    throw new Error(`rail tooltip was ${JSON.stringify(tip)}, expected Home`);
  }

  await page.mouse.move(200, 400);
  await page.setViewportSize({ width: 360, height: 800 });
  await waitForLayout(page, "drawer");
  const settled = Date.now() + 5000;
  while (Date.now() < settled && (await drawerOpen(page))) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (await drawerOpen(page)) {
    throw new Error("phone drawer started open");
  }
  await page.getByRole("banner").getByRole("button", { name: "Toggle sidebar" }).click();
  const opened = Date.now() + 5000;
  while (Date.now() < opened && !(await drawerOpen(page))) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (!(await drawerOpen(page))) {
    throw new Error("phone drawer did not open");
  }
  await page.getByRole("link", { name: "Tasks" }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  const closed = Date.now() + 5000;
  while (Date.now() < closed && (await drawerOpen(page))) {
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (await drawerOpen(page)) {
    throw new Error("phone drawer stayed open after navigation");
  }

  console.log("shell check passed");
} finally {
  await browser.close();
}
