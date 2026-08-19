import { createRequire } from "node:module";
import { signInWithPassword, storagePath } from "./session.mjs";

const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
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

  await page.goto(new URL("/usage", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Usage" }).waitFor();
  for (const name of ["Spend", "Tokens per task", "Cache hit rate"]) {
    if (!(await page.getByRole("figure", { name }).isVisible())) {
      throw new Error(`usage chart ${name} was not visible`);
    }
  }
  if (!(await page.getByText("Unknown", { exact: true }).first().isVisible())) {
    throw new Error("usage page did not show Unknown cost");
  }
  if (await page.getByText("Usage shows up here").count()) {
    throw new Error("empty usage copy still on the page");
  }

  await page.setViewportSize({ width: 360, height: 800 });
  await page.getByRole("heading", { level: 1, name: "Usage" }).waitFor();
  if (!(await page.getByRole("figure", { name: "Spend" }).isVisible())) {
    throw new Error("spend chart hidden on a phone");
  }

  console.log("usage check passed");
} finally {
  await browser.close();
}
