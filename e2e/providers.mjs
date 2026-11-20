import { createRequire } from "node:module";
import { signInWithPassword, storagePath } from "./session.mjs";

const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

const name = "e2e-fake";
const routeName = "e2e-providers-route";
// The workspace account e2e/seed-tasks.sql gives every person.
const houseAccount = "e2e00000-0000-7000-8000-00000000f001";
// A member without settings:manage, invited by the owner through the live API.
const memberEmail = "morgan.prices@example.com";
const memberName = "Morgan Price";

function pageUrl(path) {
  return new URL(path, url).href;
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

let page;
let routeId;

async function dropRoute(at) {
  if (!routeId) return;
  const gone = await at.request.delete(pageUrl(`/api/gateway/routes/${routeId}`));
  if (!gone.ok() && gone.status() !== 404) throw new Error(`deleting the route answered ${gone.status()}`);
  routeId = undefined;
}

try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  page = await context.newPage();
  await signInWithPassword(page, url);

  const card = () => page.locator('[data-testid^="provider-card-"]').filter({ hasText: name });
  const heading = (text) => page.getByRole("heading", { level: 1, name: text });
  const button = (text) => page.getByRole("button", { name: text, exact: true });

  async function openProviders() {
    await page.goto(pageUrl("/settings/providers"), { waitUntil: "domcontentloaded", timeout: 30_000 });
    await heading("Providers").waitFor();
    // The list has loaded when the loading line is gone.
    await page.getByText("Loading providers").waitFor({ state: "detached", timeout: 20_000 });
  }

  // Walks the wizard for the built in test provider through a passing test to
  // the models the provider listed.
  async function testToModels(accountName) {
    await page.getByRole("link", { name: "Add provider" }).first().click();
    await heading("Connect a provider").waitFor();
    await button("Test provider").click();
    await button("Continue").click();
    await page.getByLabel(/Provider display name/).fill(accountName);
    await button("Continue").click();
    await page.getByText("Connection Verification").waitFor();
    await button("Test connection").click();
    await page.getByText("Connection verified").waitFor();
    await page.getByText(/^The provider listed \d+ models?\.$/).waitFor();
    await button("Continue").click();
    await page.getByText(/Models & Pricing for/).waitFor();
  }

  // The models the chat composer's Model menu offers under one provider.
  async function pickerModels(provider) {
    await page.goto(pageUrl("/chat"), { waitUntil: "domcontentloaded", timeout: 30_000 });
    await heading("Chat").waitFor();
    const pill = page.getByRole("button", { name: /Agent, route and model:/ });
    await pill.click();
    await page.getByRole("menuitem", { name: "Model", exact: true }).hover();
    // Auto is always offered, so the list has rendered once it is there.
    await page.getByRole("menuitemradio", { name: "Auto", exact: true }).first().waitFor();
    const group = page.getByRole("group", { name: provider, exact: true });
    const names = (await group.count()) === 0 ? [] : await group.getByRole("menuitemradio").allInnerTexts();
    await page.keyboard.press("Escape");
    return names.map((text) => text.split("\n")[0].trim()).sort();
  }

  // A viewport change relayouts over the next rendered frames, so the
  // sideways measure is read once it has settled: it is polled until three
  // passes in a row read the same value, within an overall timeout.
  async function settledOverflow() {
    const measure = () =>
      page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    const seen = [];
    const deadline = Date.now() + 10_000;
    for (;;) {
      seen.push(await measure());
      const [a, b, c] = seen.slice(-3);
      if (a === b && b === c) return a;
      if (Date.now() > deadline) {
        throw new Error(`the sideways measure never settled; last readings: ${seen.join(", ")}`);
      }
      await page.waitForTimeout(50);
    }
  }

  // The old address opens the page where it lives now.
  await page.goto(pageUrl("/providers"), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await heading("Providers").waitFor();
  if (new URL(page.url()).pathname !== "/settings/providers") {
    throw new Error(`/providers did not redirect to /settings/providers: ${page.url()}`);
  }

  // The seed clears the person's own accounts, so the list starts without ours.
  await openProviders();
  if ((await card().count()) !== 0) {
    throw new Error(`${name} is already in the list: e2e/seed-tasks.sql did not clear the provider accounts`);
  }

  // Add an account: test, tick two of the models the provider listed, save.
  await testToModels(name);
  await page.getByRole("checkbox", { name: "Select fake-ok" }).check({ force: true });
  await page.getByRole("checkbox", { name: "Select fake-slow" }).check({ force: true });
  await page.getByText("Selected (2):").waitFor();
  await button("Connect provider").click();
  await heading("Providers").waitFor();
  await card().waitFor();
  await card().getByText("fake-ok", { exact: true }).waitFor();
  await card().getByText("fake-slow", { exact: true }).waitFor();
  await card().getByText("Test adapter", { exact: true }).waitFor();
  // The form tests the saved account once, so the brain stores it as Connected.
  await card().getByText("Connected", { exact: true }).waitFor();

  // Both models are in the chat picker, under the account's provider.
  const offered = await pickerModels("fake");
  if (JSON.stringify(offered) !== JSON.stringify(["fake-ok", "fake-slow"])) {
    throw new Error(`the chat picker offers ${JSON.stringify(offered)} for the new account, want fake-ok and fake-slow`);
  }

  // The same name again shows the brain's sentence in the form and keeps it.
  await openProviders();
  await testToModels(name);
  await page.getByRole("checkbox", { name: "Select fake-ok" }).check({ force: true });
  await button("Connect provider").click();
  await page.getByText("You already have a provider with that name.").waitFor();
  if (!page.url().endsWith("/settings/providers/new")) {
    throw new Error(`a refused account left the form: ${page.url()}`);
  }

  // Phones: neither the form nor the list scrolls sideways.
  await page.setViewportSize({ width: 360, height: 800 });
  const formOverflow = await settledOverflow();
  if (formOverflow > 0) {
    throw new Error(`the provider form scrolls sideways on a phone by ${formOverflow}px`);
  }
  await openProviders();
  await card().waitFor();
  const listOverflow = await settledOverflow();
  if (listOverflow > 0) {
    throw new Error(`the providers page scrolls sideways on a phone by ${listOverflow}px`);
  }
  await page.setViewportSize({ width: 1280, height: 800 });

  // The key field is empty on edit.
  await card().getByRole("button", { name: "Edit" }).click();
  await heading(`Edit ${name}`).waitFor();
  if ((await page.getByLabel(/^API Key/).inputValue()) !== "") {
    throw new Error("the key field was filled on edit");
  }
  await page.getByRole("link", { name: "Cancel" }).click();
  await heading("Providers").waitFor();

  // An owner sets a price and sees it as theirs.
  await page.getByRole("link", { name: `Prices for ${name}` }).click();
  await heading(`Prices for ${name}`).waitFor();
  const priceRow = page.getByTestId("price-row-fake-ok");
  await priceRow.waitFor();
  await page.getByRole("button", { name: "Set price for fake-ok" }).click();
  const priceDialog = page.getByRole("dialog");
  await priceDialog.getByLabel("Input (USD per 1M tokens)").fill("2.5");
  await priceDialog.getByLabel("Output (USD per 1M tokens)").fill("10");
  await priceDialog.getByRole("button", { name: "Save price" }).click();
  await priceDialog.waitFor({ state: "hidden" });
  await priceRow.getByText("Your price").first().waitFor();
  await priceRow.getByText("$2.50", { exact: true }).waitFor();
  await priceRow.getByText(/^Set by .+ on /).waitFor();
  // It is stored: a reload shows it again.
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByTestId("price-row-fake-ok").getByText("$2.50", { exact: true }).waitFor();

  // A member, who lacks settings:manage, sees the prices of an account they
  // can use and no control to change them.
  const roles = await (await page.request.get(pageUrl("/api/roles"))).json();
  const memberRole = roles.find((role) => role.system === "member");
  if (!memberRole) throw new Error("no member role to invite with");
  let token;
  const invited = await page.request.post(pageUrl("/api/members"), {
    data: { email: memberEmail, name: memberName, role_id: memberRole.id },
  });
  if (invited.ok()) {
    token = (await invited.json()).token;
  } else if (invited.status() === 409) {
    // A rerun's member exists: a fresh invite link if they never joined, a reset link if they did.
    const members = await (await page.request.get(pageUrl("/api/members"))).json();
    const member = members.find((person) => person.email === memberEmail);
    if (!member) throw new Error("the member could not be found after a refused invite");
    for (const kind of ["invite-link", "reset-link"]) {
      const linked = await page.request.post(pageUrl(`/api/members/${member.id}/${kind}`));
      if (linked.ok()) {
        token = (await linked.json()).token;
        break;
      }
    }
  } else {
    throw new Error(`inviting the member answered ${invited.status()}`);
  }
  if (!token) throw new Error("no sign-in link for the member");

  const memberContext = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const member = await memberContext.newPage();
  const cdp = await memberContext.newCDPSession(member);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
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
  await member.goto(pageUrl(`/login/link#${token}`), { waitUntil: "domcontentloaded", timeout: 30_000 });
  await member.getByRole("button", { name: "Create your passkey" }).click({ noWaitAfter: true });
  await member.getByRole("heading", { level: 1, name: "Home" }).waitFor({ timeout: 15_000 });
  await member.goto(pageUrl(`/settings/providers/${houseAccount}/prices`), {
    waitUntil: "domcontentloaded",
    timeout: 30_000,
  });
  await member.getByRole("heading", { level: 1, name: /^Prices for / }).waitFor();
  const memberRow = member.locator('[data-testid^="price-row-"]').first();
  await memberRow.waitFor();
  if ((await member.getByRole("button", { name: /^Set price for / }).count()) !== 0) {
    throw new Error("a member sees the control to set a price");
  }
  // The brain refuses the member as well.
  const refused = await member.request.put(pageUrl(`/api/providers/${houseAccount}/overrides`), {
    data: { model: "fake-ok", input_micro_usd_per_mtok: 1 },
  });
  if (refused.status() !== 403) {
    throw new Error(`a member's price override answered ${refused.status()}, want 403`);
  }
  await memberContext.close();

  // The owner does see the control on the same account.
  await page.goto(pageUrl(`/settings/providers/${houseAccount}/prices`), { waitUntil: "domcontentloaded" });
  await page.getByRole("button", { name: /^Set price for / }).first().waitFor();

  // A route over the account's model, so turning it off has a use to show.
  const accountId = (await (await page.request.get(pageUrl("/api/providers"))).json()).find(
    (account) => account.name === name,
  )?.id;
  if (!accountId) throw new Error(`${name} is not in GET /api/providers`);
  const madeRoute = await page.request.post(pageUrl("/api/gateway/routes"), {
    data: { name: routeName, capability: "chat", hops: [{ account_id: accountId, model: "fake-ok" }] },
  });
  if (!madeRoute.ok()) {
    throw new Error(`creating the route answered ${madeRoute.status()}: ${await madeRoute.text()}`);
  }
  routeId = (await madeRoute.json()).id;

  // Turning the account off names what uses it, and its models leave the picker.
  await openProviders();
  await page.getByRole("switch", { name: `${name} on` }).click({ force: true });
  const offDialog = page.getByRole("dialog", { name: `Turn off ${name}` });
  await offDialog.getByText(`Route ${routeName}`).waitFor();
  await offDialog.getByRole("button", { name: "Turn off", exact: true }).click();
  await offDialog.waitFor({ state: "hidden" });
  await card().locator("xpath=..").getByText("Turned off", { exact: true }).waitFor();
  const afterOff = await pickerModels("fake");
  if (afterOff.length !== 0) {
    throw new Error(`the chat picker still offers ${JSON.stringify(afterOff)} from an account that is off`);
  }

  // Remove it, and find it gone after a reload. The route goes first: an
  // account a route uses cannot be removed.
  await dropRoute(page);
  await openProviders();
  await card().getByRole("button", { name: "Disconnect" }).click();
  await page.getByRole("dialog", { name: `Disconnect ${name}` }).getByRole("button", { name: "Disconnect provider" }).click();
  await card().waitFor({ state: "detached" });
  await openProviders();
  if ((await card().count()) !== 0) {
    throw new Error("a removed account is back after a reload");
  }

  console.log("providers check passed");
} finally {
  if (page) await dropRoute(page).catch(() => {});
  await browser.close();
}
