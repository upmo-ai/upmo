import { createRequire } from "node:module";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

const pendingResult = "The final result shows up here when the task is done.";

function pageUrl(path) {
  return new URL(path, url).href;
}

async function settleText(locator, expected) {
  const deadline = Date.now() + 5000;
  let last = "";
  while (Date.now() < deadline) {
    last = (await locator.innerText()).trim();
    if (last === expected) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`text ${JSON.stringify(last)}, expected ${JSON.stringify(expected)}`);
}

function chooseFilter(page, name) {
  return page.getByTestId("task-filter").getByText(name, { exact: true }).click();
}

async function expectBadgesFullyVisible(page) {
  await page.setViewportSize({ width: 360, height: 800 });
  const badges = page.locator(".task-tile [data-testid='task-status']");
  await badges.first().waitFor();
  const results = await badges.evaluateAll((nodes) =>
    nodes.map((el) => {
      const label = el.querySelector(".task-badge-label") ?? el;
      const text = (label.textContent ?? "").replace(/\s+/g, " ").trim();
      const range = document.createRange();
      range.selectNodeContents(label);
      const textBox = range.getBoundingClientRect();
      const clips = (style, axis) => {
        const value = axis === "x" ? style.overflowX : style.overflowY;
        return value === "hidden" || value === "clip" || value === "scroll" || value === "auto";
      };
      let clipped = text.length === 0 || textBox.width < 1 || textBox.height < 1;
      let reason = clipped ? "empty" : "";
      let node = label;
      while (node && node instanceof Element) {
        const style = getComputedStyle(node);
        const box = node.getBoundingClientRect();
        if (
          clips(style, "x") &&
          (textBox.left < box.left - 1 || textBox.right > box.right + 1)
        ) {
          clipped = true;
          reason = "x";
        }
        if (
          clips(style, "y") &&
          (textBox.top < box.top - 1 || textBox.bottom > box.bottom + 1)
        ) {
          clipped = true;
          reason = "y";
        }
        if (node.classList.contains("task-tile")) break;
        node = node.parentElement;
      }
      return { text, clipped, reason };
    }),
  );
  const texts = results.map((result) => result.text);
  if (!texts.includes("Needs approval")) {
    throw new Error(`expected a Needs approval badge, saw ${texts.join(", ")}`);
  }
  const cut = results.filter((result) => result.clipped);
  if (cut.length > 0) {
    throw new Error(`task badge cut off: ${JSON.stringify(cut)}`);
  }
  await page.setViewportSize({ width: 1280, height: 800 });
}

async function signIn(page, label) {
  await page.goto(pageUrl("/sign-in"), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("img", { name: "Upmo" }).waitFor();
  await page.getByRole("button", { name: label }).click();
  await page.getByRole("heading", { level: 1, name: "Home" }).waitFor();
}

async function walkToDone(page) {
  const status = page.getByTestId("task-status");
  for (let step = 0; step < 4; step += 1) {
    if ((await status.innerText()).trim() === "Done") return;
    const before = (await page.locator("[aria-current='step']").innerText()).trim();
    await page.getByRole("button", { name: "Next phase" }).click();
    const deadline = Date.now() + 5000;
    let moved = false;
    while (Date.now() < deadline) {
      const current = (await status.innerText()).trim();
      if (current === "Done") return;
      const marker = page.locator("[aria-current='step']");
      const phase = (await marker.count()) === 0 ? "" : (await marker.innerText()).trim();
      if (phase !== "" && phase !== before) {
        moved = true;
        break;
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    if (!moved) throw new Error(`phase stayed ${before}`);
  }
  await settleText(status, "Done");
}

async function taskFlow(page, { label, checkBadges, theme }) {
  await signIn(page, label);
  if (theme) {
    await page.locator(`html[data-theme="${theme}"]`).waitFor();
  }
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  await page.getByRole("link", { name: "Daily finance digest" }).waitFor();
  await settleText(page.getByTestId("task-count"), "4 tasks");
  if (checkBadges) await expectBadgesFullyVisible(page);

  await chooseFilter(page, "Needs approval");
  await settleText(page.getByTestId("task-count"), "1 task");
  await page.getByRole("link", { name: "Daily finance digest" }).waitFor();
  if ((await page.getByRole("link", { name: "Mail triage" }).count()) !== 0) {
    throw new Error("filter kept a working task");
  }

  await page.getByRole("link", { name: "Daily finance digest" }).click();
  await page.getByRole("heading", { level: 1, name: "Daily finance digest" }).waitFor();
  const card = page.getByRole("alert");
  const cardText = await card.innerText();
  if (!cardText.includes("Approve to continue, deny to let the agent find another way.")) {
    throw new Error(`approval card copy missing: ${cardText}`);
  }
  if (!cardText.includes("Send the daily finance digest")) {
    throw new Error(`approval card does not name the action: ${cardText}`);
  }

  await page.getByRole("tab", { name: "Result" }).click();
  await settleText(page.getByTestId("task-result"), pendingResult);
  await page.getByRole("tab", { name: "Activity" }).click();

  const next = page.getByRole("button", { name: "Next phase" });
  if (await next.isEnabled()) throw new Error("next phase was enabled while waiting");
  await card.getByRole("button", { name: "Approve" }).click();
  await card.waitFor({ state: "hidden" });
  await settleText(page.getByTestId("task-status"), "Working");
  if (await next.isDisabled()) throw new Error("next phase stayed disabled after approve");

  await walkToDone(page);
  await settleText(page.getByTestId("task-status"), "Done");
  await page.getByRole("tab", { name: "Result" }).click();
  const result = (await page.getByTestId("task-result").innerText()).trim();
  if (result === pendingResult || result.length === 0) {
    throw new Error(`result after Done was ${JSON.stringify(result)}`);
  }
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await taskFlow(page, { label: "Use a password instead", checkBadges: true, theme: "light" });

  await page.getByTestId("theme-switch").getByText("Dark", { exact: true }).click();
  await page.locator("html[data-theme='dark']").waitFor();
  await taskFlow(page, { label: "Use a password instead", checkBadges: false, theme: "dark" });

  console.log("task flow passed");
} finally {
  await browser.close();
}
