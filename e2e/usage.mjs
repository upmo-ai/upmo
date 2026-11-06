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
    if (!(await page.getByRole("group", { name }).isVisible())) {
      throw new Error(`usage chart ${name} was not visible`);
    }
  }
  if (!(await page.getByText("Unknown", { exact: true }).first().isVisible())) {
    throw new Error("usage page did not show Unknown cost");
  }
  if (await page.getByText("Usage shows up here").count()) {
    throw new Error("empty usage copy still on the page");
  }

  // The period selector and the per-task table.
  const taskRows = page.getByRole("table", { name: "Task breakdown" }).getByRole("row");
  await page.getByText("Last 7 days. Figures come from the ledger.").waitFor();
  const weekRows = await taskRows.count();
  const weekSpend = await page.locator(".upmo-stat-tile").first().innerText();
  const unknownTask = page
    .getByRole("table", { name: "Task breakdown" })
    .getByRole("row", { name: /Index the handbook/ });
  if (!(await unknownTask.getByText("Unknown", { exact: true }).isVisible())) {
    throw new Error("a task with unknown cost did not show Unknown");
  }

  await page.getByText("30 days", { exact: true }).click();
  await page.getByText("Last 30 days. Figures come from the ledger.").waitFor();
  await page.getByText("Excludes 3 days with unknown cost").waitFor();
  await page.waitForFunction(
    (before) =>
      document.querySelectorAll('table[aria-label="Task breakdown"] tr').length > before,
    weekRows,
  );
  const monthRows = await taskRows.count();

  await page.getByText("90 days", { exact: true }).click();
  await page.getByText("Last 90 days. Figures come from the ledger.").waitFor();
  await page.waitForFunction(
    (before) =>
      document.querySelectorAll('table[aria-label="Task breakdown"] tr').length > before,
    monthRows,
  );
  if (!(await page.getByRole("group", { name: "Spend" }).isVisible())) {
    throw new Error("spend chart hidden on the 90 day period");
  }

  await page.getByText("7 days", { exact: true }).click();
  await page.getByText("Last 7 days. Figures come from the ledger.").waitFor();
  await page.getByText("Excludes 1 day with unknown cost").waitFor();
  await page.waitForFunction(
    (expected) =>
      document.querySelectorAll('table[aria-label="Task breakdown"] tr').length === expected,
    weekRows,
  );
  const backSpend = await page.locator(".upmo-stat-tile").first().innerText();
  if (backSpend !== weekSpend) {
    throw new Error(`spend tile did not revert on 7 days: ${JSON.stringify(backSpend)} vs ${JSON.stringify(weekSpend)}`);
  }
  if (await page.getByText("Excludes 3 days with unknown cost").count()) {
    throw new Error("30 day spend note still shown after returning to 7 days");
  }

  await page.setViewportSize({ width: 360, height: 800 });
  await page.getByRole("heading", { level: 1, name: "Usage" }).waitFor();
  if (!(await page.getByRole("group", { name: "Spend" }).isVisible())) {
    throw new Error("spend chart hidden on a phone");
  }
  if (!(await page.getByRole("table", { name: "Task breakdown" }).isVisible())) {
    throw new Error("task table hidden on a phone");
  }
  await page.waitForTimeout(2000);
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  if (overflow > 0) {
    throw new Error(`usage page scrolls sideways on a phone by ${overflow}px`);
  }

  console.log("usage check passed");
} finally {
  await browser.close();
}
