import { createRequire } from "node:module";
import { signInWithPassword, storagePath } from "./session.mjs";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 and pg here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");
const { Client } = require("pg");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

// The dev database of the compose stack, reached on the compose network.
const databaseUrl = "postgres://upmo:upmo@postgres:5432/upmo";

// What e2e/seed-tasks.sql gives these flows: one workspace account that lists
// the models the fake adapter scripts, and the routes over it.
//   e2e-main   plain-1, then fake-ok
//   e2e-slow   fake-slow
//   e2e-spare  plain-2
const accountId = "e2e00000-0000-7000-8000-00000000f001";
const agentName = "e2e-composer agent";
const otherAgentName = "e2e-composer other agent";

const reply = "this reply is scripted so the browser checks can read it whole.";

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});
const db = new Client({ connectionString: databaseUrl });
await db.connect();

// Left behind by a run that stopped half way, or by this one when it ends.
async function dropAgents(page) {
  const listed = await page.request.get(new URL("/api/agents", url).href);
  if (!listed.ok()) {
    throw new Error(`listing agents answered ${listed.status()}`);
  }
  for (const agent of await listed.json()) {
    if (agent.name === agentName || agent.name === otherAgentName) {
      const gone = await page.request.delete(new URL(`/api/agents/${agent.id}`, url).href);
      if (!gone.ok() && gone.status() !== 404) {
        throw new Error(`deleting agent ${agent.id} answered ${gone.status()}`);
      }
    }
  }
}

let page;
try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  page = await context.newPage();
  await signInWithPassword(page, url);

  const log = page.getByRole("log", { name: "Messages" });
  const pill = page.getByRole("button", { name: /Agent, route and model:/ });

  // The posts the page makes for a line and for a retry. The chat has no id in
  // its address, so the path is matched by shape.
  const isSend = (request) =>
    request.method() === "POST" && /^\/api\/chats\/[^/]+$/.test(new URL(request.url()).pathname);
  const isRetry = (request) =>
    request.method() === "POST" && /^\/api\/chats\/[^/]+\/lines\/[^/]+\/retry$/.test(new URL(request.url()).pathname);

  // A blank chat of its own, so no flow reads another's lines.
  async function newChat() {
    await page.goto(new URL("/chat", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
    await page.getByRole("heading", { level: 1, name: "Chat" }).waitFor();
    await page.getByRole("heading", { level: 2 }).first().waitFor();
    await page
      .getByRole("navigation", { name: "Chat list" })
      .getByRole("button", { name: "New chat", exact: true })
      .click();
    await page.getByRole("heading", { level: 2, name: "New chat" }).waitFor();
    await page.getByText("Write a message to start this chat.").waitFor();
  }

  // The picks outlive a page load, so every flow sets all three.
  async function pick({ agent = "Auto", route = "Auto", model = "Auto" }) {
    for (const [group, option] of [
      ["Agent", agent],
      ["Route", route],
      ["Model", model],
    ]) {
      if ((await pill.getAttribute("aria-expanded")) !== "true") {
        await pill.click();
      }
      await page.getByRole("menuitem", { name: group, exact: true }).hover();
      await page.getByRole("menuitemradio", { name: option, exact: true }).click();
    }
    if ((await pill.getAttribute("aria-expanded")) === "true") {
      await page.keyboard.press("Escape");
    }
  }

  async function write(text) {
    const draft = page.getByRole("textbox", { name: "Message" });
    await draft.fill(text);
    await draft.press("Enter");
  }

  async function sendLine(text) {
    const posted = page.waitForRequest(isSend);
    await write(text);
    return (await posted).postDataJSON();
  }

  function expectPins(body, model, label) {
    if (body.pin?.account_id !== accountId || body.pin?.model !== model) {
      throw new Error(`${label}: pin was ${JSON.stringify(body.pin)}, expected ${model} on the workspace account`);
    }
  }

  // The ledger is the only record of which link answered and where it stood.
  // The row is written as the call ends, a little after its words reach the
  // page, so it is read until it is there.
  async function ledgerSince(since, model) {
    const deadline = Date.now() + 5000;
    for (;;) {
      const { rows } = await db.query(
        "SELECT route, link_position FROM gateway.ledger WHERE at >= $1 AND model = $2 ORDER BY at",
        [since, model],
      );
      if (rows.length > 0 || Date.now() > deadline) {
        return rows;
      }
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }
  async function dbNow() {
    const { rows } = await db.query("SELECT now() AS at");
    return rows[0].at;
  }

  await dropAgents(page);
  async function makeAgent(name) {
    const made = await page.request.post(new URL("/api/agents", url).href, {
      data: {
        name,
        description: "Picked by the composer check",
        prompt_overlay: "",
        route: "",
        model_provider: "",
        model_id: "",
        enabled: true,
      },
    });
    if (!made.ok()) {
      throw new Error(`creating agent ${name} answered ${made.status()}`);
    }
    return (await made.json()).id;
  }
  const agentId = await makeAgent(agentName);
  const otherAgentId = await makeAgent(otherAgentName);

  // 1. The three picks go out with the line, and the line survives a reload.
  await newChat();
  await pick({ agent: agentName, route: "e2e-main", model: "fake-ok" });
  const first = "Composer check: the picks go with the line";
  const body = await sendLine(first);
  if (body.body !== first || body.agent_id !== agentId || body.route !== "e2e-main") {
    throw new Error(`flow 1: the line was posted as ${JSON.stringify(body)}`);
  }
  expectPins(body, "fake-ok", "flow 1");
  await log.getByText(first, { exact: true }).waitFor();
  await log.getByText(/^fake-ok says: /).waitFor();
  await page.reload({ waitUntil: "domcontentloaded", timeout: 30_000 });
  await log.getByText(first, { exact: true }).waitFor();
  await log.getByText(/^fake-ok says: /).waitFor();

  // 2. A pinned model goes first, ahead of the route's own first hop.
  await newChat();
  await pick({ route: "e2e-main", model: "fake-ok" });
  const start = await dbNow();
  await sendLine("Composer check: pin goes first");
  await log.getByText(/^fake-ok says: /).waitFor();
  const pinned = await ledgerSince(start, "fake-ok");
  if (pinned.length !== 1 || pinned[0].route !== "e2e-main" || pinned[0].link_position !== 0) {
    throw new Error(`flow 2: the ledger holds ${JSON.stringify(pinned)} for fake-ok, expected one row at position 0`);
  }
  const { rows: passed } = await db.query("SELECT 1 FROM gateway.ledger WHERE at >= $1 AND model = 'plain-1'", [start]);
  if (passed.length !== 0) {
    throw new Error(`flow 2: the route's first hop was called too: ${JSON.stringify(passed)}`);
  }

  // 3. A slow reply keeps streaming while the person is elsewhere.
  await newChat();
  await pick({ route: "e2e-slow", model: "fake-slow" });
  await sendLine("Composer check: slow reply");
  await log.getByText(/fake-slow says:/).waitFor();
  if ((await log.innerText()).includes("whole.")) {
    throw new Error("flow 3: the slow reply was whole before the page was left");
  }
  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  await page.getByRole("link", { name: "Chat", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Chat" }).waitFor();
  await log.getByText(`fake-slow says: ${reply}`, { exact: true }).waitFor({ timeout: 15_000 });

  // 4. A model that does not answer is named with the one tried next, in
  // words, with nothing from the provider.
  await newChat();
  await pick({ route: "e2e-slow", model: "fake-fail-ratelimit" });
  // A notice stays only until the next model's first word, a fraction of a
  // second, so the page records each one as it is drawn.
  await page.evaluate(() => {
    window.heardNotices = new Set();
    const grab = () => document.querySelectorAll(".chat-notice").forEach((n) => window.heardNotices.add(n.textContent));
    new MutationObserver(grab).observe(document.body, { childList: true, subtree: true, characterData: true });
  });
  await sendLine("Composer check: failure notice");
  await log.getByText(`fake-slow says: ${reply}`, { exact: true }).waitFor({ timeout: 15_000 });
  const notices = await page.evaluate(() => [...window.heardNotices]);
  // The pinned model is tried once more before the route's own, so one notice
  // names it twice; the one that matters names the next model.
  if (!notices.includes("fake-fail-ratelimit didn't answer (rate limited), trying fake-slow")) {
    throw new Error(`flow 4: no notice names the failed and the next model: ${JSON.stringify(notices)}`);
  }
  // Every notice is the page's fixed wording (noticeText over chat/domain
  // Reason) around model ids of the seeded set, and nothing else.
  const models = "(?:fake-ok|fake-slow|fake-fail-ratelimit|fake-fail-after-2|plain-1|plain-2)";
  const reasons =
    "(?:rate limited|not reachable|key rejected|model unavailable|timed out|stopped)";
  const wording = new RegExp(`^${models} didn't answer \\(${reasons}\\)(?:, trying ${models})?$`);
  for (const text of notices) {
    if (!wording.test(text)) {
      throw new Error(`flow 4: a notice is not the fixed wording over seeded models: ${JSON.stringify(text)}`);
    }
  }

  // 5. Retry asks again on the picks as they are now.
  await newChat();
  await pick({ agent: agentName, route: "e2e-main", model: "fake-fail-after-2" });
  const failing = "Composer check: retry on new picks";
  const sent = await sendLine(failing);
  expectPins(sent, "fake-fail-after-2", "flow 5");
  if (sent.agent_id !== agentId) {
    throw new Error(`flow 5: the line went out with agent ${sent.agent_id}, expected the first agent`);
  }
  // The user line has its own Retry (a branch); this one is the failed reply's.
  const retry = log.locator(".chat-failed").getByRole("button", { name: "Retry", exact: true });
  await retry.waitFor({ timeout: 10_000 });
  if (/fake stream/i.test(await log.innerText())) {
    throw new Error("flow 5: the failed reply shows provider text");
  }
  await pick({ agent: otherAgentName, route: "e2e-spare", model: "fake-ok" });
  const retried = page.waitForRequest(isRetry);
  await retry.click();
  const again = (await retried).postDataJSON();
  if (again.route !== "e2e-spare" || again.agent_id !== otherAgentId) {
    throw new Error(`flow 5: the retry was posted as ${JSON.stringify(again)}`);
  }
  expectPins(again, "fake-ok", "flow 5 retry");
  await log.getByText(/^fake-ok says: /).waitFor({ timeout: 10_000 });

  // 6. The Model menu groups models under their provider, filters them, and
  // takes a pick from the keyboard. Auto stays whatever is typed.
  await newChat();
  await pick({});
  await pill.click();
  await page.getByRole("menuitem", { name: "Model", exact: true }).hover();
  const providerGroup = page.getByRole("group", { name: "e2e", exact: true });
  await providerGroup.getByRole("menuitemradio", { name: "fake-ok", exact: true }).waitFor();
  const filter = page.getByLabel("Filter Model", { exact: true });
  await filter.fill("fake-slow");
  await providerGroup.getByRole("menuitemradio", { name: "fake-slow", exact: true }).waitFor();
  await page.getByRole("menuitemradio", { name: "Auto", exact: true }).waitFor();
  if ((await page.getByRole("menuitemradio", { name: "fake-ok", exact: true }).count()) !== 0) {
    throw new Error("flow 6: the filter left fake-ok in the list");
  }
  // Auto is pinned on top, so fake-slow is the second option.
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  const activeName = await filter.evaluate(
    (input) => document.getElementById(input.getAttribute("aria-activedescendant") ?? "")?.getAttribute("aria-label") ?? null,
  );
  if (activeName !== "fake-slow") {
    throw new Error(`flow 6: the filter's active option is ${JSON.stringify(activeName)}, expected fake-slow`);
  }
  await page.keyboard.press("Enter");
  if ((await pill.getAttribute("aria-expanded")) === "true") {
    await page.keyboard.press("Escape");
  }
  await page.getByRole("button", { name: /Agent, route and model: .*fake-slow$/ }).waitFor();

  console.log("composer flow passed");
} finally {
  if (page) {
    await dropAgents(page);
  }
  await db.end();
  await browser.close();
}
