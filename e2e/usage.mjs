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

// The page reads the gateway's ledger through GET /api/usage, so this check
// sends one real chat reply and finds its tokens on the page. The route
// e2e-main and the model fake-ok are the ones e2e/seed-tasks.sql gives the
// composer check. The fake adapter answers "fake-ok says: <12 words>" and
// counts 1 input token and one output token for each word it sent: 14.
const replyInput = 1;
const replyOutput = 14;
const memberEmail = "usage.member@example.com";
const memberName = "Usage Member";
const csvHeader =
  "range,time_zone,group,group_id,group_name,requests,errors,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,total_tokens,spend_micro_usd,estimated,unpriced_calls,list_price_micro_usd";

function pageUrl(path) {
  return new URL(path, url).href;
}

const count = new Intl.NumberFormat("en-GB");

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

async function until(label, read, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await read();
    if (last.ok) return last;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`${label}: ${JSON.stringify(last)}`);
}

function sum(buckets, key) {
  return buckets.reduce((total, bucket) => total + bucket[key], 0);
}

async function readJSON(page, path) {
  const response = await page.request.get(pageUrl(path));
  if (!response.ok()) {
    throw new Error(`${path} answered ${response.status()}`);
  }
  return response.json();
}

async function tileValue(page, label) {
  const tile = page.locator(".upmo-stat-tile").filter({
    has: page.locator(".upmo-stat-tile__label", { hasText: new RegExp(`^${label}$`) }),
  });
  return tile.locator(".upmo-stat-tile__value").innerText();
}

// One chat reply through the real entry: the chat page, the composer picks and
// the gateway. The ledger row is written as the call ends, a little after its
// words reach the page, so the caller reads the API until it is there.
async function sendChatReply(page) {
  const log = page.getByRole("log", { name: "Messages" });
  const pill = page.getByRole("button", { name: /Agent, route and model:/ });
  await page.goto(pageUrl("/chat"), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Chat" }).waitFor();
  await page.getByRole("heading", { level: 2 }).first().waitFor();
  await page
    .getByRole("navigation", { name: "Chat list" })
    .getByRole("button", { name: "New chat", exact: true })
    .click();
  await page.getByRole("heading", { level: 2, name: "New chat" }).waitFor();
  for (const [group, option] of [
    ["Agent", "Auto"],
    ["Route", "e2e-main"],
    ["Model", "fake-ok"],
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
  const draft = page.getByRole("textbox", { name: "Message" });
  await draft.fill("Usage check: one reply to count");
  await draft.press("Enter");
  await log.getByText(/^fake-ok says: /).waitFor({ timeout: 15_000 });
}

// The member is invited once, then signs in with a passkey made from a fresh
// link. Both are idempotent: a rerun finds the person and issues a new link.
async function memberLink(page) {
  const members = await readJSON(page, "/api/members");
  let member = members.find((person) => person.email === memberEmail);
  if (!member) {
    const roles = await readJSON(page, "/api/roles");
    const role = roles.find((item) => item.system === "member");
    if (!role) throw new Error("no member role to invite with");
    const invited = await page.request.post(pageUrl("/api/members"), {
      data: { email: memberEmail, name: memberName, role_id: role.id },
    });
    if (!invited.ok()) throw new Error(`inviting the member answered ${invited.status()}`);
    member = await invited.json();
  }
  for (const kind of ["invite-link", "reset-link"]) {
    const made = await page.request.post(pageUrl(`/api/members/${member.id}/${kind}`));
    if (made.ok()) {
      const { token } = await made.json();
      return pageUrl(`/login/link#${token}`);
    }
  }
  throw new Error("neither an invite link nor a reset link could be made for the member");
}

try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
    acceptDownloads: true,
  });
  const page = await context.newPage();
  await signInWithPassword(page, url);

  // The ledger before the reply, as the API reads it.
  const before = await readJSON(page, "/api/usage?range=7");
  const requestsBefore = sum(before.buckets, "requests");
  const inputBefore = sum(before.buckets, "input");
  const outputBefore = sum(before.buckets, "output");

  await sendChatReply(page);
  const after = await until("the reply reaching the ledger", async () => {
    const usage = await readJSON(page, "/api/usage?range=7");
    return { ok: sum(usage.buckets, "requests") > requestsBefore, usage };
  }).then((result) => result.usage);
  if (sum(after.buckets, "requests") !== requestsBefore + 1) {
    throw new Error(`one reply added ${sum(after.buckets, "requests") - requestsBefore} requests`);
  }
  if (sum(after.buckets, "input") - inputBefore !== replyInput || sum(after.buckets, "output") - outputBefore !== replyOutput) {
    throw new Error(
      `the reply counted ${sum(after.buckets, "input") - inputBefore} input and ${sum(after.buckets, "output") - outputBefore} output tokens, want ${replyInput} and ${replyOutput}`,
    );
  }

  // Wiring: the page shows what the API holds for that reply.
  await page.goto(pageUrl("/usage"), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Usage" }).waitFor();
  await page.getByText("Last 7 days. Figures come from the ledger.").waitFor();
  await page.getByRole("heading", { level: 2, name: "Kind of work" }).waitFor();
  const tileLabels = (await page.locator(".upmo-stat-tile__label").allInnerTexts()).filter((label) => label !== "Not billed");
  if (tileLabels.join("|") !== "Spend|Requests|Error rate|Input tokens|Output tokens|Cache hit rate") {
    throw new Error(`usage tiles were ${JSON.stringify(tileLabels)}`);
  }
  const shown = {
    requests: await tileValue(page, "Requests"),
    input: await tileValue(page, "Input tokens"),
    output: await tileValue(page, "Output tokens"),
    spend: await tileValue(page, "Spend"),
  };
  if (shown.requests !== count.format(sum(after.buckets, "requests"))) {
    throw new Error(`the Requests tile read ${shown.requests}, the ledger holds ${sum(after.buckets, "requests")}`);
  }
  if (shown.input !== count.format(sum(after.buckets, "input"))) {
    throw new Error(`the Input tokens tile read ${shown.input}, the ledger holds ${sum(after.buckets, "input")}`);
  }
  if (shown.output !== count.format(sum(after.buckets, "output"))) {
    throw new Error(`the Output tokens tile read ${shown.output}, the ledger holds ${sum(after.buckets, "output")}`);
  }
  // Cost: an unknown spend is Unknown on the page and never a zero.
  if (after.spend_micro_usd === null) {
    if (shown.spend !== "Unknown") {
      throw new Error(`the ledger has no known cost and the Spend tile read ${JSON.stringify(shown.spend)}`);
    }
  } else if (shown.spend === "Unknown" || !shown.spend.startsWith("US$")) {
    throw new Error(`the ledger holds ${after.spend_micro_usd} micro-USD and the Spend tile read ${JSON.stringify(shown.spend)}`);
  }
  const chat = after.sources.find((source) => source.source === "chat");
  if (!chat || chat.requests < 1) {
    throw new Error(`the reply is not in the chat kind of work: ${JSON.stringify(after.sources)}`);
  }
  const chatRow = page.getByRole("table", { name: "Kind of work" }).getByRole("row", { name: /^Chat/ });
  const chatCells = await chatRow.getByRole("cell").allInnerTexts();
  if (chatCells[1] !== count.format(chat.requests) || chatCells[2] !== count.format(chat.tokens)) {
    throw new Error(`the Chat row read ${JSON.stringify(chatCells)}, the ledger holds ${JSON.stringify(chat)}`);
  }
  if (chat.spend_micro_usd === null && chatCells[3] !== "Unknown") {
    throw new Error(`the Chat row has no known cost and read ${JSON.stringify(chatCells[3])}`);
  }
  if (chat.spend_micro_usd === 0 && chatCells[3] !== "US$0.00") {
    throw new Error(`the Chat row spent a real zero and read ${JSON.stringify(chatCells[3])}`);
  }

  // Limits: with none set there is no bar; a person limit shows used of limit.
  const me = await readJSON(page, "/api/session");
  const setLimit = async (amount) => {
    const put = await page.request.put(pageUrl("/api/usage/limits"), {
      data: { kind: "person", subject_id: me.user_id, amount_micro_usd: amount },
    });
    if (!put.ok()) throw new Error(`setting the limit answered ${put.status()}`);
  };
  await setLimit(null);
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { level: 2, name: "Monthly limits" }).waitFor();
  const limits = await readJSON(page, "/api/usage/limits");
  if (limits.own.limit_micro_usd !== null || limits.own.workspace_limit_micro_usd !== null) {
    throw new Error(`the e2e workspace already has limits: ${JSON.stringify(limits.own)}`);
  }
  await page.getByText("No monthly spend limit is set.").waitFor();
  if ((await page.getByRole("progressbar").count()) !== 0) {
    throw new Error("a limit bar was drawn with no limit set");
  }
  try {
    await setLimit(50_000_000);
    await page.reload({ waitUntil: "domcontentloaded" });
    const bar = page.getByRole("progressbar", { name: "Your limit" });
    await bar.waitFor({ state: "attached" });
    const figures = await page.locator(".upmo-usage-meter__figures").first().innerText();
    if (!/\/\s*US\$50\.00$/.test(figures)) {
      throw new Error(`the limit bar read ${JSON.stringify(figures)}, want the used amount of US$50.00`);
    }
  } finally {
    await setLimit(null);
  }
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByText("No monthly spend limit is set.").waitFor();

  // Ranges: the URL keeps the range, and Today is labelled by the hour the server cut.
  await page.getByText("30 days", { exact: true }).click();
  await page.getByText("Last 30 days. Figures come from the ledger.").waitFor();
  await page.waitForURL((current) => current.searchParams.get("range") === "30");
  await page.getByRole("radiogroup", { name: "Range" }).getByText("Today", { exact: true }).click();
  await page.getByText("Today. Figures come from the ledger.").waitFor();
  await page.waitForURL((current) => current.searchParams.get("range") === "today");
  const hourLabels = await page
    .getByRole("table", { name: "Spend", exact: true })
    .getByRole("rowheader")
    .allInnerTexts();
  if (hourLabels.length === 0 || !hourLabels.every((label) => /^\d\d:00$/.test(label))) {
    throw new Error(`today did not show hourly labels: ${JSON.stringify(hourLabels)}`);
  }
  await page.getByText("7 days", { exact: true }).click();
  await page.getByText("Last 7 days. Figures come from the ledger.").waitFor();

  // The charts draw from the ledger's rows.
  for (const name of ["Spend", "Cache hit rate", "Tokens per model", "Token consumption", "Requests and error rate"]) {
    await page.getByRole("heading", { level: 2, name, exact: true }).waitFor();
    if (!(await page.getByRole("group", { name, exact: true }).isVisible())) {
      throw new Error(`usage chart ${name} was not visible`);
    }
  }

  // The Workspace tab: an admin groups the workspace and exports the server's CSV.
  const tablist = page.getByRole("tablist", { name: "Whose usage" });
  await tablist.getByRole("tab", { name: "Workspace" }).click();
  const workspace = page.getByRole("table", { name: "Workspace usage" });
  await workspace.waitFor();
  const people = await readJSON(page, "/api/usage/workspace?range=7&group=person");
  if (!people.rows.some((row) => row.requests >= 1)) {
    throw new Error(`the workspace has no person with a request: ${JSON.stringify(people.rows)}`);
  }
  for (const row of people.rows) {
    await workspace.getByRole("cell", { name: row.name, exact: true }).first().waitFor();
  }
  await page.getByRole("combobox", { name: "Group by" }).click();
  await page.getByRole("option", { name: "Kind of work", exact: true }).click();
  await page.getByRole("heading", { level: 2, name: "Usage by kind of work" }).waitFor();
  await page.getByRole("table", { name: "Workspace usage" }).getByRole("cell", { name: "chat", exact: true }).waitFor();
  await page.getByRole("combobox", { name: "Group by" }).click();
  await page.getByRole("option", { name: "Person", exact: true }).click();
  await page.getByRole("heading", { level: 2, name: "Usage by person" }).waitFor();

  const download = page.waitForEvent("download");
  await page.getByRole("link", { name: "Export CSV" }).click();
  const file = await download;
  if (file.suggestedFilename() !== "usage-workspace-person-7.csv") {
    throw new Error(`the export was named ${file.suggestedFilename()}`);
  }
  const stream = await file.createReadStream();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  const exported = Buffer.concat(chunks).toString("utf8");
  const served = await (await page.request.get(pageUrl("/api/usage/workspace?range=7&group=person&format=csv"))).text();
  if (exported !== served) {
    throw new Error(`the exported file is not the server's CSV:\n${exported}\n---\n${served}`);
  }
  const lines = exported.trim().split("\n");
  if (lines[0] !== csvHeader || lines.length !== people.rows.length + 1) {
    throw new Error(`the CSV was ${JSON.stringify(lines)}`);
  }
  if (!lines.slice(1).some((line) => line.startsWith("7,") && line.includes(",person,"))) {
    throw new Error(`the CSV has no person row for 7 days: ${JSON.stringify(lines)}`);
  }

  // The same page on a phone: no sideways scroll.
  await tablist.getByRole("tab", { name: "Your usage" }).click();
  await page.setViewportSize({ width: 360, height: 800 });
  await page.getByRole("heading", { level: 1, name: "Usage" }).waitFor();
  await page.waitForTimeout(1500);
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  if (overflow > 0) {
    throw new Error(`usage page scrolls sideways on a phone by ${overflow}px`);
  }
  await page.setViewportSize({ width: 1280, height: 800 });

  // Access: a member has no Workspace tab, and the route refuses them with 403.
  const link = await memberLink(page);
  const memberContext = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  try {
    const memberPage = await memberContext.newPage();
    const client = await memberContext.newCDPSession(memberPage);
    await client.send("WebAuthn.enable");
    await client.send("WebAuthn.addVirtualAuthenticator", {
      options: {
        protocol: "ctap2",
        ctap2Version: "ctap2_1",
        transport: "internal",
        hasResidentKey: true,
        hasUserVerification: true,
        isUserVerified: true,
        automaticPresenceSimulation: true,
      },
    });
    await memberPage.goto(link, { waitUntil: "domcontentloaded", timeout: 30_000 });
    const create = memberPage.getByRole("button", { name: "Create your passkey" });
    await create.waitFor();
    await create.click({ noWaitAfter: true });
    await memberPage.getByRole("heading", { level: 1, name: "Home" }).waitFor({ timeout: 15_000 });

    await memberPage.goto(pageUrl("/usage"), { waitUntil: "domcontentloaded", timeout: 30_000 });
    await memberPage.getByRole("heading", { level: 1, name: "Usage" }).waitFor();
    // The limits panel is read with the page, so its heading says the page has settled.
    await memberPage.getByRole("heading", { level: 2, name: "Monthly limits" }).waitFor();
    await memberPage.getByRole("heading", { level: 2, name: "No usage in this range" }).waitFor();
    if ((await memberPage.getByRole("tab", { name: "Workspace" }).count()) !== 0) {
      throw new Error("a member sees the Workspace tab");
    }
    if ((await memberPage.getByRole("link", { name: "Export CSV" }).count()) !== 0) {
      throw new Error("a member sees the CSV export");
    }
    const limitsAsMember = await (await memberPage.request.get(pageUrl("/api/usage/limits"))).json();
    if ("rows" in limitsAsMember) {
      throw new Error("the gateway sent a member the limit rows");
    }
    for (const path of [
      "/api/usage/workspace?range=7&group=person",
      "/api/usage/workspace?range=7&group=person&format=csv",
    ]) {
      const refused = await memberPage.request.get(pageUrl(path));
      if (refused.status() !== 403) {
        throw new Error(`${path} answered ${refused.status()} to a member, want 403`);
      }
    }
    // The member's own usage is still theirs: the reply above was Ada's.
    const own = await memberPage.request.get(pageUrl("/api/usage?range=7"));
    if (!own.ok() || sum((await own.json()).buckets, "requests") !== 0) {
      throw new Error("a member's own usage held someone else's calls");
    }
  } finally {
    await memberContext.close();
  }

  console.log("usage check passed");
} finally {
  await browser.close();
}
