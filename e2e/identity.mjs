import { createRequire } from "node:module";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

function pageUrl(path) {
  return new URL(path, url).href;
}

async function expectFits(page, path, ready) {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto(pageUrl(path), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await ready(page);
  const result = await page.evaluate(() => {
    const viewport = window.innerWidth;
    const scroll = document.documentElement.scrollWidth;
    if (scroll <= viewport) return { ok: true, viewport, scroll };
    let worst = null;
    for (const el of document.body.querySelectorAll("*")) {
      const right = el.getBoundingClientRect().right;
      if (!worst || right > worst.right) {
        const text = (el.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 80);
        worst = { right: Math.round(right), tag: el.tagName, className: String(el.className), text };
      }
    }
    return { ok: false, viewport, scroll, worst };
  });
  if (!result.ok) {
    throw new Error(`${path} scrolls sideways: ${JSON.stringify(result)}`);
  }
}

async function identityFlow(page) {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(pageUrl("/sign-in"), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("button", { name: "Sign in with a passkey" }).click();
  await page.getByRole("heading", { level: 1, name: "Home" }).waitFor();

  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Security" }).waitFor();
  await page.getByRole("link", { name: "Members", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Members" }).waitFor();
  await page.getByLabel("Email").fill("new.person@aster.example");
  await page.getByLabel("Invite role").selectOption("member");
  await page.getByRole("button", { name: "Invite" }).click();
  await page.getByText("new.person@aster.example").waitFor();

  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("link", { name: "Daily finance digest" }).click();
  await page.getByRole("heading", { level: 1, name: "Daily finance digest" }).waitFor();
  await page.getByRole("button", { name: "Share" }).click();
  const share = page.getByRole("dialog");
  await share.getByLabel("Person").selectOption({ label: "new.person (new.person@aster.example)" });
  await share.getByLabel("Level").selectOption("viewer");
  await share.getByRole("button", { name: "Add person" }).click();
  await share.getByText("new.person", { exact: true }).waitFor();
  await share.getByRole("button", { name: "Close" }).click();
  await share.waitFor({ state: "hidden" });

  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page.getByRole("link", { name: "API tokens", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "API tokens" }).waitFor();
  await page.getByRole("button", { name: "Create token" }).click();
  const create = page.getByRole("dialog");
  await create.getByLabel("Name").fill("Ship");
  await create.getByLabel("Expiry").selectOption("90");
  await create.getByRole("checkbox", { name: "Task view" }).check();
  await create.getByRole("button", { name: "Create token" }).click();
  const secret = (await create.getByTestId("token-secret").innerText()).trim();
  if (!secret.startsWith("upmo_pat_")) {
    throw new Error(`token secret was ${JSON.stringify(secret)}`);
  }
  await create.getByText("You will not see this token again", { exact: true }).waitFor();
  await create.getByRole("button", { name: "Copy" }).click();
  await create.getByRole("button", { name: "Close" }).click();
  await create.waitFor({ state: "hidden" });
  if ((await page.getByText(secret, { exact: true }).count()) !== 0) {
    throw new Error("token secret stayed on the page");
  }
  const hint = (await page.getByTestId("token-hint").innerText()).trim();
  if (!secret.startsWith(hint) || hint === secret) {
    throw new Error(`hint ${JSON.stringify(hint)} is not a prefix of the secret`);
  }
  await page.getByRole("button", { name: "Revoke Ship" }).click();
  const revoke = page.getByRole("dialog");
  await revoke.getByText("Revoke the Ship?").waitFor();
  await revoke.getByRole("button", { name: "Revoke token" }).click();
  await page.getByText("Ship", { exact: true }).waitFor({ state: "hidden" });
  await page.getByText("No API tokens yet.").waitFor();
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

  await expectFits(page, "/sign-in", (current) =>
    current.getByRole("button", { name: "Sign in with a passkey" }).waitFor(),
  );
  await expectFits(page, "/", (current) => current.getByRole("heading", { level: 1, name: "Home" }).waitFor());
  await expectFits(page, "/tasks", (current) =>
    current.getByRole("link", { name: "Daily finance digest" }).waitFor(),
  );
  await expectFits(page, "/tasks/aster-digest", (current) =>
    current.getByRole("button", { name: "Share" }).waitFor(),
  );
  await expectFits(page, "/settings", (current) =>
    current.getByRole("button", { name: "Remove Laptop" }).waitFor(),
  );
  await expectFits(page, "/settings/members", (current) =>
    current.getByText("Ada Okonkwo", { exact: true }).waitFor(),
  );
  await expectFits(page, "/settings/tokens", (current) =>
    current.getByRole("button", { name: "Create token" }).waitFor(),
  );
  await expectFits(page, "/settings/connectors", (current) =>
    current.getByRole("heading", { level: 1, name: "Connectors" }).waitFor(),
  );

  await identityFlow(page);
  console.log("identity check passed");
} finally {
  await browser.close();
}
