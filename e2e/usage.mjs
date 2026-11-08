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
    if (!(await page.getByRole("group", { name, exact: true }).isVisible())) {
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
  if (!(await page.getByRole("group", { name: "Spend", exact: true }).isVisible())) {
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

  // Every chart panel is visible, and the heading ORDER is the reading order of the page: pairs first, then the two wide panels, then Tasks.
  const usagePanels = [
    "Spend",
    "Cache hit rate",
    "Spend by provider",
    "Spend by model",
    "Tokens per model",
    "Token consumption",
    "Latency per provider",
    "Spend share by provider",
    "Requests and error rate",
    "Cost per task",
  ];
  for (const name of usagePanels) {
    await page.getByRole("heading", { level: 2, name, exact: true }).waitFor();
    if (!(await page.getByRole("group", { name, exact: true }).isVisible())) {
      throw new Error(`usage chart ${name} was not visible`);
    }
  }
  const headings = await page.getByRole("heading", { level: 2 }).allInnerTexts();
  if (headings.join("|") !== [...usagePanels, "Tasks"].join("|")) {
    throw new Error(`usage panels were ${JSON.stringify(headings)}`);
  }

  // Layout on a wide screen: six tiles in one row, panels two to a row with equal height, the last two across the whole row.
  // The thresholds follow the width of the page content (the side navigation takes a share of the window), so a 1280px window, which leaves about 990px of content, must already get the wide layout.
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.waitForTimeout(1000);
  const contentWidth = (await page.locator(".usage-page").boundingBox())?.width ?? 0;
  if (contentWidth < 928) {
    throw new Error(`the wide layout check needs 928px of content, the page has ${contentWidth}px`);
  }
  const tops = await page
    .locator(".usage-figures .upmo-stat-tile")
    .evaluateAll((tiles) => tiles.map((tile) => Math.round(tile.getBoundingClientRect().top)));
  if (tops.length !== 6 || new Set(tops).size !== 1) {
    throw new Error(`the six tiles were not in one row: ${JSON.stringify(tops)}`);
  }
  const panelBox = async (name) => {
    // Page coordinates, so the scroll position never changes a comparison.
    const panel = page.locator(".usage-panel").filter({ has: page.getByRole("heading", { level: 2, name, exact: true }) });
    return panel.evaluate((element) => {
      const box = element.getBoundingClientRect();
      return { x: box.left + window.scrollX, y: box.top + window.scrollY, width: box.width, height: box.height };
    });
  };
  const rows = [
    ["Spend", "Cache hit rate"],
    ["Spend by provider", "Spend by model"],
    ["Tokens per model", "Token consumption"],
    ["Latency per provider", "Spend share by provider"],
  ];
  let previousBottom = 0;
  for (const [left, right] of rows) {
    const [l, r] = [await panelBox(left), await panelBox(right)];
    if (Math.abs(l.y - r.y) > 1 || Math.abs(l.height - r.height) > 1 || r.x <= l.x) {
      throw new Error(`${left} and ${right} are not two equal panels in one row: ${JSON.stringify([l, r])}`);
    }
    if (Math.abs(l.width - r.width) > 1) {
      throw new Error(`${left} and ${right} do not share the row evenly`);
    }
    if (l.y < previousBottom) {
      throw new Error(`${left} is not below the row above it`);
    }
    previousBottom = l.y + l.height;
  }
  const pairWidth = (await panelBox("Spend")).width;
  for (const name of ["Requests and error rate", "Cost per task"]) {
    const wide = await panelBox(name);
    if (wide.width < pairWidth * 1.9) {
      throw new Error(`${name} does not span the row: ${wide.width}px against ${pairWidth}px`);
    }
  }
  // Each plot is drawn at the size of its panel, with no sideways scroll.
  for (const name of usagePanels) {
    const plot = await page.getByRole("group", { name, exact: true }).locator(".upmo-chart__plot").boundingBox();
    if (!plot || plot.width < 150 || plot.height < 100) {
      throw new Error(`usage chart ${name} was drawn too small: ${JSON.stringify(plot)}`);
    }
  }
  await page.setViewportSize({ width: 1280, height: 800 });

  // The unpriced provider is named under the provider spend chart and under the donut.
  const unpriced = page.getByText("No price for: Ollama", { exact: true });
  await unpriced.first().waitFor();
  if ((await unpriced.count()) < 2) {
    throw new Error("usage should name the unpriced provider under the spend chart and the donut");
  }

  // Lines or stacked: the toggle starts on Lines, and stacking rescales the value axis to the total.
  const chartOf = (name) => page.getByRole("group", { name, exact: true });
  const plotOf = (name) => chartOf(name).locator('.upmo-chart__plot[data-chart-ready="true"]');
  const axisTexts = async (name) => (await plotOf(name).locator("svg text").allTextContents()).join("|");
  const providerView = page.getByRole("radiogroup", { name: "Spend by provider view", exact: true });
  if (!(await providerView.getByRole("radio", { name: "Lines" }).isChecked())) {
    throw new Error("spend by provider did not start on Lines");
  }
  await plotOf("Spend by provider").waitFor();
  const linesAxis = await axisTexts("Spend by provider");
  await providerView.getByText("Stacked", { exact: true }).click();
  if (!(await providerView.getByRole("radio", { name: "Stacked" }).isChecked())) {
    throw new Error("the Stacked view was not selected");
  }
  await page.waitForFunction(
    ([before]) => {
      const texts = Array.from(
        document.querySelectorAll('[role="group"][aria-label="Spend by provider"] .upmo-chart__plot svg text'),
        (node) => node.textContent,
      ).join("|");
      return texts !== "" && texts !== before;
    },
    [linesAxis],
  );
  await providerView.getByText("Lines", { exact: true }).click();
  await page.waitForFunction(
    ([before]) =>
      Array.from(
        document.querySelectorAll('[role="group"][aria-label="Spend by provider"] .upmo-chart__plot svg text'),
        (node) => node.textContent,
      ).join("|") === before,
    [linesAxis],
  );

  // Hover: a tooltip lists the series by name, and the legend hides and shows a series.
  const hoverPlot = async (name, fraction) => {
    const plot = plotOf(name);
    await plot.scrollIntoViewIfNeeded();
    const box = await plot.boundingBox();
    if (!box) {
      throw new Error(`${name} has no plot to hover`);
    }
    await page.mouse.move(box.x + box.width * fraction, box.y + box.height * 0.5, { steps: 4 });
  };
  const tipNames = async (name) => {
    const tip = chartOf(name).locator(".upmo-chart__tip").first();
    await tip.waitFor({ state: "visible", timeout: 5000 });
    return tip.locator(".upmo-chart__tip-name").allTextContents();
  };
  await hoverPlot("Spend by provider", 0.3);
  const shown = await tipNames("Spend by provider");
  for (const series of ["Anthropic (global)", "OpenAI (global)", "Mistral"]) {
    if (!shown.includes(series)) {
      throw new Error(`the spend tooltip did not list ${series}: ${JSON.stringify(shown)}`);
    }
  }
  const legendItem = chartOf("Spend by provider").locator(".upmo-chart__legend-button").filter({ hasText: "Anthropic" });
  if ((await legendItem.getAttribute("aria-pressed")) !== "true") {
    throw new Error("the Anthropic legend item did not start shown");
  }
  await legendItem.click();
  if ((await legendItem.getAttribute("aria-pressed")) !== "false") {
    throw new Error("clicking the legend item did not hide the series");
  }
  await hoverPlot("Spend by provider", 0.45);
  await chartOf("Spend by provider")
    .locator(".upmo-chart__tip")
    .filter({ hasNotText: "Anthropic" })
    .first()
    .waitFor({ state: "visible", timeout: 5000 });
  const afterHide = await tipNames("Spend by provider");
  if (afterHide.includes("Anthropic (global)") || !afterHide.includes("Mistral")) {
    throw new Error(`the tooltip after hiding Anthropic was ${JSON.stringify(afterHide)}`);
  }
  await legendItem.click();
  if ((await legendItem.getAttribute("aria-pressed")) !== "true") {
    throw new Error("clicking the legend item again did not show the series");
  }
  // The same hover works on the other charts: tokens, the cache hit rate and the bar chart.
  for (const [name, expected] of [
    ["Tokens per model", "Claude 3.7 Sonnet"],
    ["Cache hit rate", "Cache hit rate"],
    ["Spend", "Spend"],
  ]) {
    await hoverPlot(name, 0.3);
    const names = await tipNames(name);
    if (!names.includes(expected)) {
      throw new Error(`the ${name} tooltip listed ${JSON.stringify(names)}, not ${expected}`);
    }
  }

  // Today: hourly labels, and the range is kept in the URL.
  await page.getByRole("radiogroup", { name: "Range" }).getByText("Today", { exact: true }).click();
  await page.getByText("Today. Figures come from the ledger.").waitFor();
  await page.waitForURL((current) => current.searchParams.get("range") === "today");
  await page.getByText("Excludes 1 hour with unknown cost").waitFor();
  const hourLabels = await page
    .getByRole("table", { name: "Spend", exact: true })
    .getByRole("rowheader")
    .allInnerTexts();
  if (hourLabels.length === 0 || !hourLabels.every((label) => /^\d\d:00$/.test(label))) {
    throw new Error(`today did not show hourly labels: ${JSON.stringify(hourLabels)}`);
  }
  for (const name of [
    "Cache hit rate",
    "Spend by provider",
    "Spend by model",
    "Tokens per model",
    "Token consumption",
    "Requests and error rate",
  ]) {
    const labels = await page.getByRole("table", { name, exact: true }).getByRole("rowheader").allInnerTexts();
    if (labels.length !== hourLabels.length || !labels.every((label) => /^\d\d:00$/.test(label))) {
      throw new Error(`today did not show hourly labels in ${name}: ${JSON.stringify(labels)}`);
    }
  }
  // The drawn axis carries hour labels too, not only the hidden table.
  await plotOf("Spend").waitFor();
  await page.waitForFunction(() =>
    Array.from(
      document.querySelectorAll('[role="group"][aria-label="Spend"] .upmo-chart__plot svg text'),
      (node) => node.textContent ?? "",
    ).some((text) => /^\d\d:00$/.test(text)),
  );
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
  // The shell and the charts re-measure after the resize; let them settle before looking.
  await page.waitForTimeout(1500);
  if (!(await page.getByRole("group", { name: "Spend", exact: true }).isVisible())) {
    throw new Error("spend chart hidden on a phone");
  }
  if (!(await page.getByRole("table", { name: "Task breakdown" }).isVisible())) {
    throw new Error("task table hidden on a phone");
  }
  for (const name of usagePanels) {
    if (!(await page.getByRole("group", { name, exact: true }).isVisible())) {
      throw new Error(`usage chart ${name} hidden on a phone`);
    }
  }
  if (!(await page.getByRole("table", { name: "Cost split for Daily finance digest" }).isVisible())) {
    throw new Error("the open model split hidden on a phone");
  }
  const phoneTiles = await page
    .locator(".usage-figures .upmo-stat-tile")
    .evaluateAll((tiles) => tiles.map((tile) => Math.round(tile.getBoundingClientRect().left)));
  if (phoneTiles.length !== 6 || new Set(phoneTiles).size !== 2) {
    throw new Error(`the phone did not show the tiles two across: ${JSON.stringify(phoneTiles)}`);
  }
  const phoneLeft = await page
    .locator(".usage-panel")
    .evaluateAll((panels) => panels.map((panel) => Math.round(panel.getBoundingClientRect().left)));
  if (phoneLeft.length !== usagePanels.length || new Set(phoneLeft).size !== 1) {
    throw new Error(`the phone did not show the panels in one column: ${JSON.stringify(phoneLeft)}`);
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
