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

// The share flow runs against a seeded task, on the live brain.
const shareTaskId = "e2e00000-0000-7000-8000-000000000001";
const teamName = "Sharing";
const partnerEmail = "rowan.share@example.com";
const partnerName = "Rowan Hale";

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

// Sharing needs a team the owner belongs to and a second person to share with.
// Both are made through the live API with the signed-in session, and both are
// idempotent: a rerun finds them where the last run left them.
async function shareSetup(page) {
  const read = async (path) => (await page.request.get(pageUrl(path))).json();

  const members = await read("/api/members");
  const ada = members.find((person) => person.email === "ada@example.com");
  if (!ada) throw new Error("ada is not in the member list");

  const teams = await read("/api/teams");
  let team = teams.find((item) => item.name === teamName);
  if (!team) {
    const made = await page.request.post(pageUrl("/api/teams"), { data: { name: teamName } });
    if (!made.ok()) throw new Error(`team create answered ${made.status()}`);
    team = { id: (await made.json()).id, name: teamName };
  }
  // A team is not its members: put ada on it, again if she already is.
  const added = await page.request.post(pageUrl(`/api/teams/${team.id}/members`), {
    data: { user_id: ada.id },
  });
  if (!added.ok()) throw new Error(`adding ada to the team answered ${added.status()}`);

  let partner = members.find((person) => person.email === partnerEmail);
  if (!partner) {
    const roles = await read("/api/roles");
    const role = roles.find((item) => item.system === "member");
    if (!role) throw new Error("no member role to invite with");
    const invited = await page.request.post(pageUrl("/api/members"), {
      data: { email: partnerEmail, name: partnerName, role_id: role.id },
    });
    if (invited.ok()) {
      partner = await invited.json();
    } else if (invited.status() === 409) {
      // A rerun's partner is already a member, so the invite is refused.
      partner = (await read("/api/members")).find((person) => person.email === partnerEmail);
    }
  }
  if (!partner) throw new Error("the partner could not be found or invited");
  return { ada };
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

  const { ada } = await shareSetup(page);
  // The members list was read before the setup added people and a team, and the
  // app keeps what it read. Start from fresh data.
  await page.reload({ waitUntil: "domcontentloaded" });

  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("link", { name: "Sort the morning mail" }).click();
  await page.getByRole("heading", { level: 1, name: "Sort the morning mail" }).waitFor();
  await page.getByRole("button", { name: "Share" }).click();
  // Named dialogs: the share dialog and a removal confirm can be open together.
  const share = page.getByRole("dialog", { name: "Share", exact: true });
  await share.getByText("Ada", { exact: true }).waitFor();
  // The owner comes first and cannot be removed.
  if ((await share.getByRole("button", { name: "Remove access for Ada" }).count()) !== 0) {
    throw new Error("the owner row can be removed");
  }
  // The design-system Select opens a listbox in a portal outside the dialog.
  await share.getByLabel("Person", { exact: true }).click();
  await page
    .getByRole("option", { name: `${partnerName} (${partnerEmail})`, exact: true })
    .click();
  await share.getByLabel("Level", { exact: true }).click();
  await page.getByRole("option", { name: "Viewer", exact: true }).click();
  await share.getByRole("button", { name: "Add person" }).click();
  const partnerRole = share.getByRole("combobox", { name: `Access for ${partnerName}` });
  await partnerRole.waitFor();
  if ((await partnerRole.inputValue()) !== "Viewer") {
    throw new Error("the chosen level was not granted");
  }
  // The choice is saved first and the radio follows the saved value, so wait for it.
  const team = share.getByRole("radio", { name: "Team" });
  // The Team radio waits for the roster and the teams, and for any save in flight.
  for (let attempt = 0; attempt < 100 && (await team.isDisabled()); attempt += 1) {
    await page.waitForTimeout(100);
  }
  await team.click();
  for (let attempt = 0; attempt < 50 && !(await team.isChecked()); attempt += 1) {
    await page.waitForTimeout(100);
  }
  if (!(await team.isChecked())) {
    throw new Error("the visibility choice was not applied");
  }
  // With one team there is nothing to pick and the brain names it. With more
  // than one the picker shows; choose the team this run made.
  const picked = share.getByRole("combobox", { name: "Shared team" });
  if ((await picked.count()) !== 0) {
    await picked.click();
    await page.getByRole("option", { name: teamName, exact: true }).click();
  }
  await share.getByRole("button", { name: "Close" }).click();
  await share.waitFor({ state: "hidden" });

  // A reload reads the saved share back: the visibility, the person and the level.
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { level: 1, name: "Sort the morning mail" }).waitFor();
  await page.getByRole("button", { name: "Share" }).click();
  await share.getByText(partnerName, { exact: true }).waitFor();
  if (!(await share.getByRole("radio", { name: "Team" }).isChecked())) {
    throw new Error("the visibility was not kept");
  }
  if ((await partnerRole.inputValue()) !== "Viewer") {
    throw new Error("the granted level was not kept");
  }

  // The level widens to Editor, then the access is removed again.
  await partnerRole.click();
  await page.getByRole("option", { name: "Editor", exact: true }).click();
  for (let attempt = 0; attempt < 50 && (await partnerRole.inputValue()) !== "Editor"; attempt += 1) {
    await page.waitForTimeout(100);
  }
  if ((await partnerRole.inputValue()) !== "Editor") {
    throw new Error("the level was not changed");
  }
  await share.getByRole("button", { name: `Remove access for ${partnerName}` }).click();
  const confirm = page.getByRole("dialog", { name: `Remove ${partnerName}'s access?` });
  await confirm.getByRole("button", { name: "Remove access", exact: true }).click();
  await share.getByText(partnerName, { exact: true }).waitFor({ state: "hidden" });
  await share.getByRole("button", { name: "Close" }).click();
  await share.waitFor({ state: "hidden" });

  // Back to private with only the owner, so the next run starts clean.
  const put = await page.request.put(pageUrl(`/api/tasks/${shareTaskId}/share`), {
    data: {
      visibility: "private",
      members: [{ subject_type: "person", subject_id: ada.id, role: "owner" }],
    },
  });
  if (!put.ok()) throw new Error(`putting the share back answered ${put.status()}`);

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
    current.getByRole("link", { name: "Sort the morning mail" }).waitFor(),
  );
  await expectFits(page, `/tasks/${shareTaskId}`, (current) =>
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
