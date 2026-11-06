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

// The shell slides the sidebar away after a resize; measure once the tile
// has stopped moving.
async function settleBox(locator) {
  const deadline = Date.now() + 5000;
  let last = "";
  while (Date.now() < deadline) {
    const box = JSON.stringify(await locator.boundingBox());
    if (box === last) return;
    last = box;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`box kept moving: ${last}`);
}

async function expectBadgesFullyVisible(page) {
  await page.setViewportSize({ width: 360, height: 800 });
  const badges = page.locator(".task-tile [data-testid='task-status']");
  await badges.first().waitFor();
  await settleBox(page.locator(".task-tile").first());
  const results = await badges.evaluateAll((nodes) =>
    nodes.map((el) => {
      const label = el.querySelector(".mantine-Badge-label") ?? el;
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

async function signIn(page) {
  await signInWithPassword(page, url);
}

function headerStatus(page) {
  return page.locator(".task-header .upmo-status-badge");
}

// The phase rail drops its aria-current marker once the task is Done, so the
// badge and the marker are read together in one page evaluation. Reading them
// with separate locator calls can see the marker, lose it to the Done render,
// and then wait on a marker that never comes back.
//
// Resolves with the badge text and the current phase once the task is Done or
// its phase differs from `from` (null accepts any phase).
async function awaitPhase(page, from) {
  const seen = await page.waitForFunction(
    (before) => {
      const badge = document.querySelector(".task-header .upmo-status-badge");
      const marker = document.querySelector("[aria-current='step']");
      const status = (badge?.innerText ?? "").trim();
      const phase = (marker?.innerText ?? "").trim();
      if (status === "Done" || (phase !== "" && phase !== before)) return { status, phase };
      return null;
    },
    from,
    { timeout: 5000 },
  );
  return seen.jsonValue();
}

// Checking is the last step a person can ask for. Upmo marks the task Done
// itself, so Next phase turns off there and says why.
async function walkToChecking(page) {
  let seen = await awaitPhase(page, null);
  for (let step = 0; step < 4; step += 1) {
    if (seen.phase === "Checking") break;
    await page.getByRole("button", { name: "Next phase" }).click();
    try {
      seen = await awaitPhase(page, seen.phase);
    } catch {
      throw new Error(`phase stayed ${seen.phase}`);
    }
  }
  if (seen.phase !== "Checking") throw new Error(`walk ended on ${seen.phase}, expected Checking`);
  await settleText(headerStatus(page), "Checking");
  if (await page.getByRole("button", { name: "Next phase" }).isEnabled()) {
    throw new Error("next phase was enabled at Checking");
  }
  const note = (await page.getByTestId("next-phase-note").innerText()).trim();
  if (!note.includes("Checking is the last step")) {
    throw new Error(`no explanation for the disabled next phase: ${JSON.stringify(note)}`);
  }
}

async function taskFlow(page, { checkBadges, theme }) {
  await signIn(page);
  if (theme) {
    await page.locator(`html[data-theme="${theme}"]`).waitFor();
  }
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  await page.getByRole("link", { name: "Daily finance digest" }).waitFor();
  await settleText(page.getByTestId("task-count"), "5 tasks");
  if (checkBadges) await expectBadgesFullyVisible(page);

  await chooseFilter(page, "Needs approval");
  await settleText(page.getByTestId("task-count"), "1 task");
  await page.getByRole("link", { name: "Daily finance digest" }).waitFor();
  if ((await page.getByRole("link", { name: "Mail triage" }).count()) !== 0) {
    throw new Error("filter kept a working task");
  }

  await page.getByRole("link", { name: "Daily finance digest" }).click();
  await page.getByRole("heading", { level: 1, name: "Daily finance digest" }).waitFor();
  const card = page.getByRole("group", { name: "Send the daily finance digest" });
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
  await settleText(headerStatus(page), "Planning");
  if (await next.isDisabled()) throw new Error("next phase stayed disabled after approve");

  await walkToChecking(page);
  await page.getByRole("tab", { name: "Result" }).click();
  await settleText(page.getByTestId("task-result"), pendingResult);
}

async function newTaskFlow(page) {
  await signIn(page);
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  await page.getByRole("button", { name: "New task" }).click();
  const dialog = page.getByRole("dialog", { name: "New task" });
  await dialog.waitFor();
  await dialog.getByText("0 of 200 characters").waitFor();
  if (await dialog.getByRole("button", { name: "Create task" }).isEnabled()) {
    throw new Error("create was enabled without a goal");
  }

  await dialog.getByLabel("Goal").fill("x".repeat(201));
  await dialog.getByText("Keep the goal to 200 characters or fewer.").waitFor();
  if (await dialog.getByRole("button", { name: "Create task" }).isEnabled()) {
    throw new Error("create was enabled with a goal over 200 characters");
  }
  await dialog.getByLabel("Goal").fill("Tidy the shared drive");
  await dialog.getByText("21 of 200 characters").waitFor();

  if ((await dialog.getByLabel("Repository").count()) !== 0) {
    throw new Error("the repository field showed before Advanced was opened");
  }
  await dialog.getByRole("button", { name: "Advanced" }).click();
  await dialog.getByLabel("Repository").fill("not a repo");
  await dialog.getByText("Use owner/name or an https URL.", { exact: true }).waitFor();
  await dialog.getByLabel("Repository").fill("upmo-ai/upmo");
  await dialog.getByRole("button", { name: "Create task" }).click();

  await page.getByRole("heading", { level: 1, name: "Tidy the shared drive" }).waitFor();
  await settleText(headerStatus(page), "Preparing");
  const header = await page.locator(".task-header").innerText();
  for (const invented of ["gpt-5.2", "Run 14", "€2.10"]) {
    if (header.includes(invented)) throw new Error(`new task header shows ${invented}: ${header}`);
  }

  await page.getByLabel("Message this task").fill("Skip the drafts folder");
  await page.getByRole("button", { name: "Send message" }).click();
  await page
    .getByRole("list", { name: "Messages" })
    .getByText("Skip the drafts folder")
    .waitFor();
  await page.waitForFunction(
    () => document.querySelector("textarea[aria-label='Message this task']")?.value === "",
  );
}

// The task page renders the preparing block from the payload: the ordered steps
// while the task prepares, a failed probe with its message, then a summary.
async function preparingFlow(page) {
  await signIn(page);
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();

  await page.getByRole("link", { name: "Fix the flaky login test" }).click();
  await page.getByRole("heading", { level: 1, name: "Fix the flaky login test" }).waitFor();
  const failed = page.getByRole("list", { name: "Preparation steps" });
  await failed.waitFor();
  const order = await failed.locator(".task-prepare-step-name").allInnerTexts();
  if (order.join(",") !== "Clone,Toolchain,Probe") {
    throw new Error(`steps out of order: ${order.join(",")}`);
  }
  const states = await failed.locator(".task-prepare-step-state").allInnerTexts();
  if (states.join(",") !== "Done,Done,Failed") {
    throw new Error(`unexpected step states: ${states.join(",")}`);
  }
  await failed.getByRole("alert").getByText("The work directory is not writable.").waitFor();
  const settings = page.getByRole("region", { name: "Runtime settings" });
  for (const shown of ["Kata", "upmo-ai/upmo", "node 24", "4 GB", "30 min"]) {
    await settings.getByText(shown, { exact: true }).waitFor();
  }

  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("link", { name: "Mail triage" }).first().click();
  await page.getByRole("heading", { level: 1, name: "Mail triage" }).waitFor();
  const running = page.getByRole("list", { name: "Preparation steps" });
  await running.waitFor();
  const progress = await running.locator(".task-prepare-step-state").allInnerTexts();
  if (progress.join(",") !== "Done,Running,Waiting") {
    throw new Error(`unexpected step states: ${progress.join(",")}`);
  }

  await page.getByRole("button", { name: "Next phase" }).click();
  await running.waitFor({ state: "hidden" });
  await page.getByRole("heading", { level: 2, name: "Prepared" }).waitFor();
  const summary = await page.getByTestId("preparing-summary").innerText();
  if (summary !== "The workspace was ready in 5.4 s.") {
    throw new Error(`unexpected preparing summary: ${summary}`);
  }
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
  await taskFlow(page, { checkBadges: true, theme: "light" });

  await page.getByTestId("theme-switch").getByText("Dark", { exact: true }).click();
  await page.locator("html[data-theme='dark']").waitFor();
  await taskFlow(page, { checkBadges: false, theme: "dark" });
  await newTaskFlow(page);
  await preparingFlow(page);

  console.log("task flow passed");
} finally {
  await browser.close();
}
