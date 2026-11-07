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
  for (const name of ["Spend", "Cost per task", "Cache hit rate"]) {
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
    .getByRole("row", { name: /Fix the flaky login test/ });
  if (!(await unknownTask.getByText("Unknown", { exact: true }).isVisible())) {
    throw new Error("a task with unknown cost did not show Unknown");
  }

  await page.getByText("30 days", { exact: true }).click();
  await page.getByText("Last 30 days. Figures come from the ledger.").waitFor();
  await page.waitForURL((current) => current.searchParams.get("range") === "30");
  await page.getByText("Excludes 3 days with unknown cost").waitFor();
  await page.waitForFunction(
    (before) =>
      document.querySelectorAll('table[aria-label="Task breakdown"] tr').length > before,
    weekRows,
  );
  const monthRows = await taskRows.count();

  await page.getByText("90 days", { exact: true }).click();
  await page.getByText("Last 90 days. Figures come from the ledger.").waitFor();
  await page.waitForURL((current) => current.searchParams.get("range") === "90");
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
  await page.waitForURL((current) => current.searchParams.get("range") === "7");
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

  // The six stat tiles, in order.
  const tileLabels = await page.locator(".upmo-stat-tile__label").allInnerTexts();
  if (tileLabels.join("|") !== "Spend|Requests|Error rate|Input tokens|Output tokens|Cache hit rate") {
    throw new Error(`usage tiles were ${JSON.stringify(tileLabels)}`);
  }
  await page.getByText(/calls in range had no configured price and are excluded from spend\./).waitFor();

  // Today: hourly labels, and the range is kept in the URL.
  await page.getByRole("radiogroup", { name: "Range" }).getByText("Today", { exact: true }).click();
  await page.getByText("Today. Figures come from the ledger.").waitFor();
  await page.waitForURL((current) => current.searchParams.get("range") === "today");
  await page.getByText("Excludes 1 hour with unknown cost").waitFor();
  const hourLabels = await page
    .getByRole("table", { name: "Spend" })
    .getByRole("rowheader")
    .allInnerTexts();
  if (hourLabels.length === 0 || !hourLabels.every((label) => /^\d\d:00$/.test(label))) {
    throw new Error(`today did not show hourly labels: ${JSON.stringify(hourLabels)}`);
  }
  await page.getByText("7 days", { exact: true }).click();
  await page.getByText("Last 7 days. Figures come from the ledger.").waitFor();
  await page.waitForFunction(
    (expected) =>
      document.querySelectorAll('table[aria-label="Task breakdown"] tr').length === expected,
    weekRows,
  );

  // Sort by cost: the order flips, aria-sort follows, and unknown cost stays last.
  const taskTable = page.getByRole("table", { name: "Task breakdown" });
  const taskNames = () => taskTable.locator("tbody a").allInnerTexts();
  const costHeader = taskTable.getByRole("columnheader", { name: "Cost" });
  if ((await costHeader.getAttribute("aria-sort")) !== "descending") {
    throw new Error("the task table did not start sorted by cost, biggest first");
  }
  const byCostDesc = await taskNames();
  if (byCostDesc[byCostDesc.length - 1] !== "Fix the flaky login test") {
    throw new Error(`unknown cost was not last: ${JSON.stringify(byCostDesc)}`);
  }
  await costHeader.getByRole("button", { name: "Cost" }).click();
  await page.waitForFunction(
    () => document.querySelector('table[aria-label="Task breakdown"] th[aria-sort="ascending"]') !== null,
  );
  const byCostAsc = await taskNames();
  const known = byCostDesc.filter((name) => name !== "Fix the flaky login test");
  if (byCostAsc.join("|") !== [...known].reverse().concat("Fix the flaky login test").join("|")) {
    throw new Error(`cost ascending was ${JSON.stringify(byCostAsc)} for ${JSON.stringify(byCostDesc)}`);
  }
  await taskTable.getByRole("columnheader", { name: "Tokens" }).getByRole("button", { name: "Tokens" }).click();
  await page.waitForFunction(
    () => document.querySelector('table[aria-label="Task breakdown"] th[aria-sort="descending"]')?.textContent === "Tokens",
  );
  if ((await costHeader.getAttribute("aria-sort")) !== "none") {
    throw new Error("cost header kept its sort after sorting by tokens");
  }

  // Expand a task: its cost split by model adds up to the task cost.
  // The open split is a row of its own that also mentions the task, so pick the row by its link.
  const reviewRow = taskTable
    .locator("tbody > tr")
    .filter({ has: page.getByRole("link", { name: "Daily finance digest" }) });
  const expand = reviewRow.getByRole("button", { name: "Show the model split for Daily finance digest" });
  if ((await expand.getAttribute("aria-expanded")) !== "false") {
    throw new Error("the model split started open");
  }
  await expand.click();
  const split = page.getByRole("table", { name: "Cost split for Daily finance digest" });
  await split.waitFor();
  const toggle = reviewRow.getByRole("button", { name: "Hide the model split for Daily finance digest" });
  if ((await toggle.getAttribute("aria-expanded")) !== "true") {
    throw new Error("the model split button did not report it was open");
  }
  const money = (text) => Number(text.replace(/[^0-9.]/g, ""));
  const taskCost = money(await reviewRow.getByRole("cell").nth(7).innerText());
  const partCosts = await split.locator("tbody tr").evaluateAll((rows) =>
    rows.map((row) => row.lastElementChild?.textContent ?? ""),
  );
  const partTotal = partCosts.reduce((sum, text) => sum + money(text), 0);
  if (partCosts.length < 2 || Math.abs(partTotal - taskCost) > 0.005) {
    throw new Error(`model split ${JSON.stringify(partCosts)} did not add up to ${taskCost}`);
  }
  const link = reviewRow.getByRole("link", { name: "Daily finance digest" });
  if (!(await link.getAttribute("href"))?.startsWith("/tasks/")) {
    throw new Error("the task name did not link to the task page");
  }

  await page.setViewportSize({ width: 360, height: 800 });
  await page.getByRole("heading", { level: 1, name: "Usage" }).waitFor();
  if (!(await page.getByRole("group", { name: "Spend" }).isVisible())) {
    throw new Error("spend chart hidden on a phone");
  }
  if (!(await page.getByRole("table", { name: "Task breakdown" }).isVisible())) {
    throw new Error("task table hidden on a phone");
  }
  if (!(await page.getByRole("table", { name: "Cost split for Daily finance digest" }).isVisible())) {
    throw new Error("the open model split hidden on a phone");
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
