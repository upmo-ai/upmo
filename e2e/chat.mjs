import { createRequire } from "node:module";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

// Password set by e2e/passkeys.mjs on the bootstrap person.
const email = "ada@example.com";
const password = "correct horse battery";

// The chats e2e/seed-tasks.sql gives the person, newest first. The brain
// derives each title from the chat's first user line.
const closeTitle = "What should the October close check first?";
const pricesTitle = "Summarize the supplier prices";

const viewport = { width: 1280, height: 900 };

async function until(label, read, timeout = 5000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await read();
    if (last.ok) return last;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`${label}: ${JSON.stringify(last)}`);
}

// The composer, hint included, sits inside the viewport, near its foot because
// the page fills the height, and the document does not scroll. The layout
// settles after the sidebar and fonts load, so poll.
async function expectComposerOnScreen(page, label) {
  await until(`${label} composer`, () =>
    page.evaluate((footGap) => {
      const composer = document.querySelector(".chat-composer-wrap .upmo-composer");
      if (!composer) return { ok: false, reason: "composer missing" };
      const box = composer.getBoundingClientRect();
      const scroller = document.scrollingElement;
      const inside = box.top >= 0 && box.bottom <= window.innerHeight;
      const fits = scroller.scrollHeight <= window.innerHeight;
      const filled = window.innerHeight - box.bottom <= footGap;
      return {
        ok: inside && fits && filled,
        top: Math.round(box.top),
        bottom: Math.round(box.bottom),
        innerHeight: window.innerHeight,
        scrollHeight: scroller.scrollHeight,
      };
    }, 64),
  );
}

// On a viewport too short for the chat page's floor, the route's main scrolls.
// Scroll the composer into view the way a user reaches it, then check the whole
// composer, Send included, is inside the viewport. Only main may have scrolled:
// a clipped (overflow hidden) ancestor that had to scroll would mean the user
// has no way to get there.
async function expectComposerReachable(page, label) {
  const before = await until(`${label} scrolls`, () =>
    page.evaluate(() => {
      const main = document.querySelector(".upmo-app-frame__main");
      return { ok: main.scrollHeight > main.clientHeight, scrollHeight: main.scrollHeight, clientHeight: main.clientHeight };
    }),
  );
  if (!before.ok) throw new Error(`${label}: main does not scroll`);
  await page.evaluate(() => {
    document.querySelector(".chat-composer-wrap").scrollIntoView({ block: "end" });
  });
  await until(`${label} composer reachable`, () =>
    page.evaluate(() => {
      const composer = document.querySelector(".chat-composer-wrap .upmo-composer");
      const send = document.querySelector(".chat-composer-wrap button[aria-label='Send']");
      const box = composer.getBoundingClientRect();
      const sendBox = send.getBoundingClientRect();
      const main = document.querySelector(".upmo-app-frame__main");
      const clipped = [];
      for (let el = composer.parentElement; el && el !== main; el = el.parentElement) {
        if (el.scrollTop !== 0) clipped.push(el.className);
      }
      const inside = (b) => b.top >= 0 && b.bottom <= window.innerHeight;
      return {
        ok: inside(box) && inside(sendBox) && clipped.length === 0,
        composer: [Math.round(box.top), Math.round(box.bottom)],
        send: [Math.round(sendBox.top), Math.round(sendBox.bottom)],
        innerHeight: window.innerHeight,
        clipped,
      };
    }),
  );
}

function messageLog(page) {
  return page.getByRole("log", { name: "Messages" });
}

// Sends a line and waits for the stored line itself. The brain answers 202 and
// writes no assistant line yet, so the wait ends there: no thinking line stays
// behind and the log is never busy for good.
async function send(page, text) {
  const draft = page.getByRole("textbox", { name: "Message" });
  await draft.fill(text);
  await draft.press("Enter");
  await messageLog(page).getByText(text, { exact: true }).waitFor();
  await until(`no pending after ${text}`, () =>
    messageLog(page).evaluate((log) => ({
      ok: log.getAttribute("aria-busy") === "false" && !log.textContent.includes("Thinking"),
      busy: log.getAttribute("aria-busy"),
    })),
  );
}

// The thread, not the page, holds the history: it overflows, it sits at the
// newest message, and the main region has nothing to scroll.
async function expectThreadAtNewest(page, label) {
  await until(`${label} thread`, () =>
    messageLog(page).evaluate((log) => {
      const main = document.querySelector(".upmo-app-frame__main");
      const left = log.scrollHeight - log.scrollTop - log.clientHeight;
      return {
        ok: log.scrollHeight > log.clientHeight && left <= 2 && main.scrollHeight <= main.clientHeight,
        left: Math.round(left),
        logScroll: [log.scrollHeight, log.clientHeight],
        mainScroll: [main.scrollHeight, main.clientHeight],
      };
    }),
  );
}

// At the newest position the last message clears the foot of the list by its
// bottom padding (24px) and the scrollbar by the inline-end padding (8px), so
// the composer below and the "Jump to newest" pill are not drawn over it. The
// message's own bottom margin comes on top, so 24px is the floor.
async function expectLastMessageClear(page, label) {
  await until(`${label} last message`, () =>
    messageLog(page).evaluate((log) => {
      const messages = log.querySelectorAll(".upmo-chat-msg");
      const last = messages[messages.length - 1];
      if (!last) return { ok: false, reason: "no message" };
      const box = last.getBoundingClientRect();
      const view = log.getBoundingClientRect();
      const below = view.top + log.clientHeight - box.bottom;
      const aside = view.left + log.clientWidth - box.right;
      // The point just inside the message's lower edge is the message itself,
      // not the composer or another control drawn over it.
      const hit = document.elementFromPoint(box.left + box.width / 2, box.bottom - 2);
      return {
        ok: below >= 24 && aside >= 8 && Boolean(hit && last.contains(hit)),
        below: Math.round(below),
        aside: Math.round(aside),
        hit: hit ? hit.className : null,
      };
    }),
  );
}

// main carries the page inset (Mantine reads --app-shell-padding). The chat
// page fills the height and drops that padding, then puts it back, so its inset
// must equal the one of any other route at the same viewport.
async function mainInset(page) {
  return page.evaluate(() => {
    const style = getComputedStyle(document.querySelector(".upmo-app-frame__main"));
    return {
      top: style.paddingTop,
      right: style.paddingRight,
      bottom: style.paddingBottom,
      left: style.paddingLeft,
    };
  });
}

async function expectChatInsetMatchesTasks(page, label) {
  await page.goto(new URL("/tasks", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Tasks" }).waitFor();
  const tasks = await mainInset(page);
  await page.goto(new URL("/chat", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Chat" }).waitFor();
  const chat = await mainInset(page);
  if (tasks.left === "0px" || tasks.right === "0px" || tasks.bottom === "0px") {
    throw new Error(`${label}: the Tasks page has no inset to compare with, ${JSON.stringify(tasks)}`);
  }
  if (JSON.stringify(chat) !== JSON.stringify(tasks)) {
    throw new Error(`${label}: chat inset ${JSON.stringify(chat)}, Tasks inset ${JSON.stringify(tasks)}`);
  }
}

// The chat list drawer: whether it is open, whether a reader or the keyboard
// can reach it, and where it sits on screen.
async function drawer(page) {
  return page.evaluate(() => {
    const list = document.querySelector("#chat-list");
    const box = list.getBoundingClientRect();
    return {
      open: list.getAttribute("data-open") === "true",
      inert: list.hasAttribute("inert"),
      opacity: getComputedStyle(list).opacity,
      left: Math.round(box.left),
      right: Math.round(box.right),
      width: window.innerWidth,
    };
  });
}

const drawerShut = (d) => !d.open && d.inert && d.opacity === "0";
const drawerOpen = (d) => d.open && !d.inert && d.opacity === "1" && d.left >= 0 && d.right <= d.width;

async function expectNoSideScroll(page, label) {
  await until(`${label} side scroll`, () =>
    page.evaluate(() => ({
      ok: document.scrollingElement.scrollWidth <= window.innerWidth,
      scrollWidth: document.scrollingElement.scrollWidth,
      innerWidth: window.innerWidth,
    })),
  );
}

async function openChat(page, name) {
  await page.getByRole("navigation", { name: "Chat list" }).getByRole("button", { name }).click();
  await page.getByRole("heading", { level: 2, name }).waitFor();
}

async function openFreshChatPage(page, firstTitle) {
  await page.goto(new URL("/chat", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Chat" }).waitFor();
  await page.getByRole("heading", { level: 2, name: firstTitle }).waitFor();
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  const context = await browser.newContext({
    viewport,
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();

  // The server offers a passkey once per account, and e2e/passkeys.mjs has
  // used that offer. Sign in through the form as it does, and mark the offer
  // as due in the browser's copy of the response. The request itself is real,
  // so the server still sees a same-origin browser sign-in.
  await page.addInitScript(() => {
    const send = window.fetch.bind(window);
    window.fetch = async (input, init) => {
      const response = await send(input, init);
      const target = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (!response.ok || !target.includes("/api/sign-in/password")) return response;
      const body = await response.clone().json();
      return new Response(JSON.stringify({ ...body, offer_passkey: true }), {
        status: response.status,
        headers: response.headers,
      });
    };
  });

  await page.goto(new URL("/login", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Home" }).waitFor({ timeout: 15_000 });
  const offer = page.getByRole("region", { name: "Add a passkey for next time" });
  await offer.waitFor();

  // Client-side navigation keeps the offer, which lives in page memory.
  await page.getByRole("navigation", { name: "Main" }).getByRole("link", { name: "Chat" }).click();
  await page.getByRole("heading", { level: 1, name: "Chat" }).waitFor();
  await offer.waitFor();
  await expectComposerOnScreen(page, "with the passkey offer");

  // The seeded chat opens first: its lines carry their roles, and the
  // assistant line its model, tokens and cost.
  await messageLog(page).getByText(closeTitle, { exact: true }).waitFor();
  await messageLog(page).getByText("Start with the supplier statements, then reconcile the ledger.").waitFor();
  await messageLog(page).getByText("claude-opus-5-5 · 412 tokens · US$0.0031").waitFor();
  const roles = await messageLog(page).evaluate((log) => ({
    user: log.querySelectorAll(".upmo-chat-msg--user").length,
    assistant: log.querySelectorAll(".upmo-chat-msg--assistant").length,
  }));
  if (roles.user !== 1 || roles.assistant !== 1) {
    throw new Error(`UPM-201: the seeded chat shows the wrong roles, ${JSON.stringify(roles)}`);
  }

  // A long history stays in the thread: the composer keeps its place, the
  // thread follows the newest message, and the page and main do not scroll.
  for (let n = 1; n <= 12; n += 1) {
    await send(page, `History message ${n}`);
  }
  await expectComposerOnScreen(page, "UPM-196 with a long history");
  await expectThreadAtNewest(page, "UPM-196 with a long history");
  await expectLastMessageClear(page, "with a long history");

  // A reader who scrolled up gets "Jump to newest", and sending returns to the
  // newest message.
  await messageLog(page).evaluate((log) => {
    log.scrollTop = 0;
  });
  await page.getByRole("button", { name: "Jump to newest" }).waitFor();
  await send(page, "Back at the newest");
  await expectThreadAtNewest(page, "UPM-196 after sending from further up");
  await expectLastMessageClear(page, "after sending from further up");
  await page.getByRole("button", { name: "Jump to newest" }).waitFor({ state: "hidden" });

  // Phone landscape with the offer showing: the page keeps its floor and main
  // scrolls to the composer.
  await page.setViewportSize({ width: 700, height: 340 });
  await expectComposerReachable(page, "700x340 with the passkey offer and a long history");

  // A 12 line draft grows the composer, which then has to stay reachable.
  // 340px cannot show a composer that tall under the top bar, so use 520px,
  // where it shows whole after scrolling.
  const draft = page.getByRole("textbox", { name: "Message" });
  await page.setViewportSize({ width: 700, height: 520 });
  await draft.fill(Array.from({ length: 12 }, (_, i) => `Line ${i + 1}`).join("\n"));
  await expectComposerReachable(page, "700x520 with a 12 line draft");
  await draft.fill("");
  await page.setViewportSize(viewport);
  await expectComposerOnScreen(page, "back at 1280x900 with the passkey offer");

  await offer.getByRole("button", { name: "Not now" }).click();
  await offer.waitFor({ state: "hidden" });
  await expectComposerOnScreen(page, "after dismissing the passkey offer");

  // The inset matches another route's, wide and narrow. A full load keeps the
  // session cookie; the passkey offer is gone by now and plays no part.
  await expectChatInsetMatchesTasks(page, "1280x900 inset");
  await page.setViewportSize({ width: 600, height: 900 });
  await expectChatInsetMatchesTasks(page, "600x900 inset");
  await page.setViewportSize(viewport);

  // Chats come from the brain. The list shows the seeded set, newest first,
  // each with the preview of its newest line, and the empty one with none.
  await openFreshChatPage(page, closeTitle);
  const list = page.getByRole("navigation", { name: "Chat list" });
  const items = list.locator(".chat-list-scroll ul li button");
  // The list renders as it loads; read it once all three chats are in.
  await items.nth(2).waitFor();
  const titles = await items.allInnerTexts();
  if (titles.length !== 3 || !titles[0].includes(closeTitle) || !titles[1].includes(pricesTitle) || !titles[2].includes("New chat")) {
    throw new Error(`UPM-201: the seeded chats are not listed newest first, ${JSON.stringify(titles)}`);
  }
  // The first chat is the one this script sent lines to above. The gateway runs
  // in this stack, so each line got a failed reply (no route "chat", no text),
  // and the preview is the newest line's text: none. The reply lands after its
  // line, so wait for it. The second chat keeps what was seeded.
  const { tips } = await until("UPM-201 first chat preview", async () => {
    const found = await items.evaluateAll((buttons) => buttons.map((button) => button.getAttribute("title")));
    return { ok: found[0] === null, tips: found };
  });
  if (tips[1] !== "Prices are stable to the year end; lead times are the open question.") {
    throw new Error(`UPM-201: the second chat shows the wrong preview, ${JSON.stringify(tips[1])}`);
  }
  if (tips[2] !== null) {
    throw new Error(`UPM-201: an empty chat shows a preview, ${JSON.stringify(tips[2])}`);
  }

  // Another chat shows its own lines, and keeps the layout guarantees. Its
  // assistant line has no cost, which reads Unknown and never zero.
  await openChat(page, pricesTitle);
  await messageLog(page).getByText("Prices are stable to the year end; lead times are the open question.").waitFor();
  await messageLog(page).getByText("llama3.3:70b · 58 tokens · Unknown").waitFor();
  const pricesText = await messageLog(page).innerText();
  if (/\$0\.00(?!\d)/.test(pricesText)) {
    throw new Error(`UPM-201: a cost shows as zero, ${JSON.stringify(pricesText)}`);
  }
  await expectComposerOnScreen(page, "UPM-201 after opening another chat");

  // A new chat is created with a POST, takes the line, names itself from it
  // and moves to the top of the list, and the stored line survives a reload.
  await list.getByRole("button", { name: "New chat", exact: true }).click();
  await page.getByRole("heading", { level: 2, name: "New chat" }).waitFor();
  await page.getByText("Write a message to start this chat.").waitFor();
  await send(page, "Plan the offsite");
  await page.getByRole("heading", { level: 2, name: "Plan the offsite" }).waitFor();
  const first = list.getByRole("button").filter({ hasText: /\d\d:\d\d$/ }).first();
  const firstText = await first.innerText();
  if (!firstText.includes("Plan the offsite") || (await first.getAttribute("aria-current")) !== "page") {
    throw new Error(`UPM-201: the new chat is not first in the list, ${JSON.stringify(firstText)}`);
  }
  await page.reload({ waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 2, name: "Plan the offsite" }).waitFor();
  await messageLog(page).getByText("Plan the offsite", { exact: true }).waitFor();
  await expectComposerOnScreen(page, "UPM-201 after a new chat's line");

  // Phone width: the list is a drawer that starts shut, opens from the thread
  // header, and shuts again once a chat is picked.
  await page.setViewportSize({ width: 390, height: 800 });
  await openFreshChatPage(page, "Plan the offsite");
  await until("UPM-201 drawer starts shut", async () => {
    const d = await drawer(page);
    return { ok: drawerShut(d), ...d };
  });
  await expectNoSideScroll(page, "UPM-201 at 390x800");
  await expectComposerOnScreen(page, "UPM-201 at 390x800");
  await page.getByRole("button", { name: "Show chats" }).click();
  await until("UPM-201 drawer opens", async () => {
    const d = await drawer(page);
    return { ok: drawerOpen(d), ...d };
  });
  await openChat(page, pricesTitle);
  await until("UPM-201 drawer shuts after a pick", async () => {
    const d = await drawer(page);
    return { ok: drawerShut(d), ...d };
  });
  await messageLog(page).getByText("llama3.3:70b · 58 tokens · Unknown").waitFor();
  await send(page, "Asked on a phone");
  await expectComposerOnScreen(page, "UPM-201 at 390x800 after a line");
  await expectNoSideScroll(page, "UPM-201 at 390x800 after a line");
  await page.setViewportSize(viewport);

  console.log("chat check passed");
} finally {
  await browser.close();
}
