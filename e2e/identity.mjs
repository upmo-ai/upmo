import { createRequire } from "node:module";
import { openAccountItem, storagePath } from "./session.mjs";

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
  await page.goto(pageUrl("/login"), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("button", { name: "Log in", exact: true }).waitFor();
  await page.getByRole("link", { name: "Terms of use" }).waitFor();
  await page.getByRole("link", { name: "Privacy policy" }).waitFor();
  if ((await page.getByRole("button", { name: "Use a password instead" }).count()) !== 0) {
    throw new Error("the password form still offers another password button");
  }
  await page.goto(pageUrl("/"), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Home" }).waitFor();

  await openAccountItem(page, "Security");
  await page.getByRole("heading", { level: 1, name: "Security" }).waitFor();
  await page.getByRole("button", { name: "Management", exact: true }).click();
  await page.getByRole("link", { name: "Members", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Members" }).waitFor();
  await page.getByText("ada@example.com").waitFor();

  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("link", { name: "Daily finance digest" }).click();
  await page.getByRole("heading", { level: 1, name: "Daily finance digest" }).waitFor();
  await page.getByRole("button", { name: "Share" }).click();
  const share = page.getByRole("dialog");
  // The design-system Select opens a listbox in a portal outside the dialog.
  await share.getByLabel("Person", { exact: true }).click();
  await page.getByRole("option", { name: "Noor Ali (noor@aster.example)" }).click();
  await share.getByLabel("Level", { exact: true }).click();
  await page.getByRole("option", { name: "Editor" }).click();
  await share.getByRole("button", { name: "Add person" }).click();
  await share.getByText("Noor Ali", { exact: true }).waitFor();
  // The owner sees the role next to each person, can widen who may open
  // the task, and the choice is kept when the dialog is opened again.
  const noorRole = share.getByRole("combobox", { name: "Access for Noor Ali" });
  await noorRole.waitFor();
  if ((await noorRole.inputValue()) !== "Editor") {
    throw new Error("the chosen level was not granted");
  }
  await share.getByRole("button", { name: "Remove access for Noor Ali" }).waitFor();
  await share.getByRole("radio", { name: "Everyone" }).check();
  await share.getByRole("button", { name: "Close" }).click();
  await share.waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "Share" }).click();
  await share.getByRole("radio", { name: "Everyone" }).waitFor();
  if (!(await share.getByRole("radio", { name: "Everyone" }).isChecked())) {
    throw new Error("the visibility was not kept");
  }
  await share.getByRole("button", { name: "Close" }).click();
  await share.waitFor({ state: "hidden" });

  await openAccountItem(page, "API tokens");
  await page.getByRole("heading", { level: 1, name: "API tokens" }).waitFor();
  await page.getByRole("button", { name: "Create token" }).click();
  const create = page.getByRole("dialog");
  await create.getByLabel("Name").fill("Ship");
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
  const hint = page.getByTestId("token-hint");
  if ((await hint.count()) !== 0) {
    const text = (await hint.innerText()).trim();
    if (!secret.startsWith(text) || text === secret) {
      throw new Error(`hint ${JSON.stringify(text)} is not a prefix of the secret`);
    }
  } else {
    await page.getByText("Ship", { exact: true }).waitFor();
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
  const loggedOut = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const outPage = await loggedOut.newPage();
  await expectFits(outPage, "/login", (current) =>
    current.getByRole("button", { name: "Log in with a passkey" }).waitFor(),
  );
  await loggedOut.close();

  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  await expectFits(page, "/", (current) => current.getByRole("heading", { level: 1, name: "Home" }).waitFor());
  await expectFits(page, "/tasks", (current) =>
    current.getByRole("link", { name: "Daily finance digest" }).waitFor(),
  );
  await expectFits(page, "/tasks/aster-digest", (current) =>
    current.getByRole("button", { name: "Share" }).waitFor(),
  );
  await expectFits(page, "/settings", (current) =>
    current.getByRole("button", { name: "Change password" }).waitFor(),
  );
  await expectFits(page, "/admin", (current) =>
    current.getByText("ada@example.com").waitFor(),
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
