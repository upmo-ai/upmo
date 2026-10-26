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

  // Phone landscape with the offer showing: the page keeps its floor and main
  // scrolls to the composer.
  await page.setViewportSize({ width: 700, height: 340 });
  await expectComposerReachable(page, "700x340 with the passkey offer");

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

  console.log("chat check passed");
} finally {
  await browser.close();
}
