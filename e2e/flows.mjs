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

// The tasks e2e/seed-tasks.sql puts in the e2e person's list.
const titles = {
  mail: "Sort the morning mail",
  plan: "Plan the launch email",
  approval: "Email the supplier statements",
  failed: "Close the week for Kodeeo Limited",
  parent: "Close the books for October",
  done: "Reconcile the accounts",
  draft: "Draft the October summary",
};
const seeded = Object.keys(titles).length;

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

async function openTasks(page) {
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
}

async function openTask(page, title) {
  await openTasks(page);
  await page.getByRole("link", { name: title }).click();
  await page.getByRole("heading", { level: 1, name: title }).waitFor();
}

async function expectNoPlaceholderText(page, what) {
  const body = await page.locator("body").innerText();
  if (/\bundefined\b|\bnull\b|NaN/.test(body)) {
    throw new Error(`${what} shows a missing value: ${body}`);
  }
}

// Read only: the list, its filters and what each seeded task shows. It runs in
// both themes, so it must leave the tasks as it found them.
//
// Approvals (the approval card and Approve) belong to the WP2 approvals
// ticket and the preparing panel to the runtime ticket; both stay on sample
// data until then, so this flow does not cover them.
async function taskFlow(page, { checkBadges, theme }) {
  await signIn(page);
  if (theme) {
    await page.locator(`html[data-theme="${theme}"]`).waitFor();
  }
  await openTasks(page);
  await page.getByRole("link", { name: titles.mail }).waitFor();
  await settleText(page.getByTestId("task-count"), `${seeded} tasks`);
  if ((await page.getByRole("link", { name: "Daily finance digest", exact: true }).count()) !== 0) {
    throw new Error("the list shows a sample task");
  }
  if (checkBadges) await expectBadgesFullyVisible(page);

  await chooseFilter(page, "Underway");
  await settleText(page.getByTestId("task-count"), "4 tasks");
  for (const title of [titles.mail, titles.plan, titles.parent, titles.draft]) {
    await page.getByRole("link", { name: title }).waitFor();
  }
  if ((await page.getByRole("link", { name: titles.approval }).count()) !== 0) {
    throw new Error("the Underway filter kept a task that needs approval");
  }
  await chooseFilter(page, "Needs approval");
  await settleText(page.getByTestId("task-count"), "1 task");
  await page.getByRole("link", { name: titles.approval }).waitFor();
  await chooseFilter(page, "Done");
  await settleText(page.getByTestId("task-count"), "1 task");
  await page.getByRole("link", { name: titles.done }).waitFor();
  await chooseFilter(page, "Failed");
  await settleText(page.getByTestId("task-count"), "1 task");
  await page.getByRole("link", { name: titles.failed }).waitFor();
  await chooseFilter(page, "All");
  await settleText(page.getByTestId("task-count"), `${seeded} tasks`);

  // A task read by its own id: events, with the chat line among them.
  await page.getByRole("link", { name: titles.mail }).click();
  await page.getByRole("heading", { level: 1, name: titles.mail }).waitFor();
  await settleText(headerStatus(page), "Working");
  await page
    .getByRole("list", { name: "Messages" })
    .getByText("Read the support mailbox first.")
    .waitFor();
  await page.getByText("All events", { exact: true }).click();
  const activity = page.getByRole("region", { name: "Activity" });
  await activity.getByText("4 events").waitFor();
  const lines = (await activity.locator(".upmo-eventstream__msg").allInnerTexts()).map((line) => line.trim());
  const expected = ["Task opened", "Planning", "Moved on to Working", "You: Read the support mailbox first."];
  if (lines.join("|") !== expected.join("|")) {
    throw new Error(`events ${JSON.stringify(lines)}, expected ${JSON.stringify(expected)}`);
  }
  await page.getByRole("tab", { name: "Result" }).click();
  await settleText(page.getByTestId("task-result"), pendingResult);
  await expectNoPlaceholderText(page, "the Working task");

  // Waiting for approval: nothing to advance.
  await openTask(page, titles.approval);
  await settleText(headerStatus(page), "Needs approval");
  if (await page.getByRole("button", { name: "Next phase" }).isEnabled()) {
    throw new Error("next phase was enabled while waiting");
  }
  await expectNoPlaceholderText(page, "the Needs approval task");

  // Failed, with its cause.
  await openTask(page, titles.failed);
  await settleText(headerStatus(page), "Failed");
  if (await page.getByRole("button", { name: "Next phase" }).isEnabled()) {
    throw new Error("next phase was enabled on a failed task");
  }
  await expectNoPlaceholderText(page, "the Failed task");

  // A parent lists its two children with their own status.
  await openTask(page, titles.parent);
  const subtasks = page.getByRole("list", { name: "Subtasks" });
  await subtasks.waitFor();
  const rows = (await subtasks.locator("li").allInnerTexts()).map((row) => row.replace(/\s+/g, " ").trim());
  if (
    rows.length !== 2 ||
    !rows.some((row) => row.includes(titles.done) && row.includes("Done")) ||
    !rows.some((row) => row.includes(titles.draft) && row.includes("Working"))
  ) {
    throw new Error(`unexpected subtasks: ${JSON.stringify(rows)}`);
  }
  await expectNoPlaceholderText(page, "the parent task");

  // A finished subtask shows the result it stored.
  await openTask(page, titles.done);
  await settleText(headerStatus(page), "Done");
  await page.getByRole("tab", { name: "Result" }).click();
  const result = await page.getByTestId("task-result").innerText();
  for (const stored of ["The accounts reconcile.", "All 31 ledger entries matched the bank statement."]) {
    if (!result.includes(stored)) throw new Error(`result ${JSON.stringify(result)} misses ${stored}`);
  }
  await expectNoPlaceholderText(page, "the Done task");
}

// Changes the seeded tasks, so it runs once, after the read only flows. What it
// writes must survive a reload: that is the brain keeping it.
async function changeFlow(page) {
  await signIn(page);

  await openTask(page, titles.mail);
  await page.getByLabel("Message this task").fill("Skip the drafts folder");
  await page.getByRole("button", { name: "Send message" }).click();
  const messages = page.getByRole("list", { name: "Messages" });
  await messages.getByText("Skip the drafts folder").waitFor();
  await page.waitForFunction(
    () => document.querySelector("textarea[aria-label='Message this task']")?.value === "",
  );
  await page.reload();
  await page.getByRole("heading", { level: 1, name: titles.mail }).waitFor();
  await messages.getByText("Read the support mailbox first.").waitFor();
  await messages.getByText("Skip the drafts folder").waitFor();

  // Next phase on a Planning task moves it to Working, and the brain keeps it.
  await openTask(page, titles.plan);
  await settleText(headerStatus(page), "Planning");
  await page.getByRole("button", { name: "Next phase" }).click();
  const seen = await awaitPhase(page, "Planning");
  if (seen.phase !== "Working" || seen.status !== "Working") {
    throw new Error(`advance showed ${JSON.stringify(seen)}, expected Working`);
  }
  await page.reload();
  await page.getByRole("heading", { level: 1, name: titles.plan }).waitFor();
  await settleText(headerStatus(page), "Working");
  await page.getByText("All events", { exact: true }).click();
  const advanced = page.getByRole("region", { name: "Activity" }).locator(".upmo-eventstream__msg");
  await advanced.last().waitFor();
  const last = (await advanced.last().innerText()).trim();
  if (last !== "Moved on to Working") throw new Error(`last event is ${JSON.stringify(last)}`);

  // The list and its filters follow what the brain now holds.
  await openTasks(page);
  await settleText(page.getByTestId("task-count"), `${seeded} tasks`);
  await chooseFilter(page, "Underway");
  await settleText(page.getByTestId("task-count"), "4 tasks");
}

// A task that names a repository needs the brain's sandbox service, and the
// e2e brain cannot reach one: it answers 503, so the dialog must show the
// refusal and stay open. Everything before that is the dialog's own checking.
async function newTaskFlow(page) {
  await signIn(page);
  await openTasks(page);
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

  await dialog
    .getByRole("alert")
    .getByText("This task needs a sandbox and none is set up yet.", { exact: false })
    .waitFor();
  if (!(await dialog.isVisible())) throw new Error("the dialog closed on a refused task");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await dialog.waitFor({ state: "hidden" });
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
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
  await changeFlow(page);
  await newTaskFlow(page);

  console.log("task flow passed");
} finally {
  await browser.close();
}
