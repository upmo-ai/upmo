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

// The task this flow owns: seeded Working by e2e/seed-tasks.sql and left alone
// by every other flow (flows.mjs only reads it in lists and subtask rows). It
// runs here and is reseeded before the next run.
//
// The task is Working, not Preparing, so its page never polls. A change that
// shows up without a reload can only have arrived on the event stream; the
// posts below go through page.request, which the page never sees.
const taskId = "e2e00000-0000-7000-8000-000000000007";
const taskTitle = "Draft the October summary";
const streamPath = `/api/tasks/${taskId}/stream`;
const taskPath = `/api/tasks/${taskId}`;

function pageUrl(path) {
  return new URL(path, url).href;
}

function isStream(response) {
  return new URL(response.url()).pathname === streamPath && response.status() === 200;
}

async function openTask(page, title) {
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  await page.getByRole("link", { name: title }).click();
  await page.getByRole("heading", { level: 1, name: title }).waitFor();
}

// The badge and the phase rail's marker, read together in one evaluation: the
// marker can be lost and regained across rerenders, so two locator calls can
// each miss it (flows.mjs's awaitPhase does the same).
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
    { timeout: 3000 },
  );
  return seen.jsonValue();
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

  // What the page itself asks for: the stream request and the task's own GETs.
  const asks = [];
  page.on("request", (request) =>
    asks.push({ method: request.method(), path: new URL(request.url()).pathname }),
  );
  const taskReads = () => asks.filter((ask) => ask.method === "GET" && ask.path === taskPath).length;

  // The stream answer is caught by its headers while its body keeps streaming.
  const streamAnswered = page.waitForResponse(isStream, { timeout: 10_000 });
  await openTask(page, taskTitle);
  const stream = await streamAnswered;

  const headers = await stream.headers();
  const contentType = headers["content-type"] ?? "";
  if (!contentType.startsWith("text/event-stream")) {
    throw new Error(`stream answered content-type ${JSON.stringify(contentType)}`);
  }
  if (headers["content-encoding"] !== undefined) {
    throw new Error(`stream arrived compressed: content-encoding ${headers["content-encoding"]}`);
  }

  // While the stream carries the task, the page stays quiet for longer than
  // the 1 s preparing poll would have waited between two reads.
  const opened = taskReads();
  if (opened < 1) {
    throw new Error("the page never read the task, so the request log is unreliable");
  }
  await page.waitForTimeout(1200);
  if (taskReads() !== opened) {
    throw new Error(`the page read the task again while streaming (${taskReads()} reads)`);
  }

  // A chat line posted outside the page: only the stream can show it.
  const line = "Stream check: the summary draft is fine.";
  const posted = await page.request.post(pageUrl(taskPath), { data: { kind: "chat", body: line } });
  if (!posted.ok()) {
    throw new Error(`posting a chat line answered ${posted.status()}`);
  }
  await page
    .getByRole("list", { name: "Messages" })
    .getByText(line)
    .waitFor({ timeout: 3000 });

  // The task moves on outside the page too: the header follows without a reload.
  const advanced = await page.request.post(pageUrl(taskPath), { data: { kind: "advance", turn: "upmo:end" } });
  if (!advanced.ok()) {
    throw new Error(`advancing the task answered ${advanced.status()}`);
  }
  const seen = await awaitPhase(page, "Working");
  if (seen.phase !== "Checking" || seen.status !== "Checking") {
    throw new Error(`advance showed ${JSON.stringify(seen)}, expected Checking`);
  }

  // A second tab with the same session streams the same task: a line posted
  // outside both pages shows up in each.
  const second = await context.newPage();
  const secondStream = second.waitForResponse(isStream, { timeout: 10_000 });
  await second.goto(pageUrl(`/tasks/${taskId}`), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await second.getByRole("heading", { level: 1, name: taskTitle }).waitFor();
  await secondStream;

  const bothTabs = "Stream check: both tabs hear me.";
  const postedAgain = await page.request.post(pageUrl(taskPath), { data: { kind: "chat", body: bothTabs } });
  if (!postedAgain.ok()) {
    throw new Error(`posting a second chat line answered ${postedAgain.status()}`);
  }
  await second
    .getByRole("list", { name: "Messages" })
    .getByText(bothTabs)
    .waitFor({ timeout: 3000 });
  await page
    .getByRole("list", { name: "Messages" })
    .getByText(bothTabs)
    .waitFor({ timeout: 3000 });

  console.log("stream flow passed");
} finally {
  await browser.close();
}
