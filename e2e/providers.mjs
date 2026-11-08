import { createRequire } from "node:module";
import { signInWithPassword, storagePath } from "./session.mjs";

const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

const name = "e2e-fake";

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

  const card = () => page.locator('[data-testid^="provider-card-"]').filter({ hasText: name });
  const heading = (text) => page.getByRole("heading", { level: 1, name: text });
  const button = (text) => page.getByRole("button", { name: text, exact: true });

  async function openProviders() {
    await page.goto(new URL("/providers", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
    await heading("Providers").waitFor();
    // The list has loaded when the loading line is gone.
    await page.getByText("Loading providers").waitFor({ state: "detached", timeout: 20_000 });
  }

  // Walks the wizard for the built in test provider as far as the review step.
  async function fillWizard(accountName, models) {
    await page.getByRole("link", { name: "Add provider" }).first().click();
    await heading("Connect a provider").waitFor();
    await button("Test provider").click();
    await button("Continue").click();
    await page.getByLabel(/Provider display name/).fill(accountName);
    await button("Continue").click();
    for (const model of models) {
      await page.getByLabel("Add Custom Model Identifier").fill(model);
      await page.getByLabel("Add Custom Model Identifier").press("Enter");
    }
    await button("Continue").click();
    await page.getByText("Review Configuration").waitFor();
  }

  async function overflow() {
    return page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  }

  // The seed clears the person's own accounts, so the list starts without ours.
  await openProviders();
  if ((await card().count()) !== 0) {
    throw new Error(`${name} is already in the list: e2e/seed-tasks.sql did not clear the provider accounts`);
  }

  // Add an account on the local plan with no key, and two models.
  await fillWizard(name, ["alpha-1", "beta-2"]);
  await button("Test connection").click();
  await page.getByText("Connection verified").waitFor();
  await button("Connect provider").click();
  await heading("Providers").waitFor();
  await card().waitFor();
  await card().getByText("Not connected", { exact: true }).waitFor();
  await card().getByText("alpha-1", { exact: true }).waitFor();
  await card().getByText("beta-2", { exact: true }).waitFor();
  await card().getByText("Test adapter", { exact: true }).waitFor();

  // The same name again shows the brain's sentence in the form and keeps it.
  await fillWizard(name, ["dup-1"]);
  await button("Test connection").click();
  await page.getByText("Connection verified").waitFor();
  await button("Connect provider").click();
  await page.getByText("You already have a provider with that name.").waitFor();
  if (!page.url().endsWith("/providers/new")) {
    throw new Error(`a refused account left the form: ${page.url()}`);
  }
  await button("Connect provider").waitFor();

  // Phones: neither the form nor the list scrolls sideways.
  await page.setViewportSize({ width: 360, height: 800 });
  await page.waitForTimeout(500);
  const formOverflow = await overflow();
  if (formOverflow > 0) {
    throw new Error(`the provider form scrolls sideways on a phone by ${formOverflow}px`);
  }
  await openProviders();
  await card().waitFor();
  await page.waitForTimeout(500);
  const listOverflow = await overflow();
  if (listOverflow > 0) {
    throw new Error(`the providers page scrolls sideways on a phone by ${listOverflow}px`);
  }
  await page.setViewportSize({ width: 1280, height: 800 });

  // Test the saved account: the brain stores Connected, and the list shows it.
  await card().getByRole("button", { name: "Edit" }).click();
  await heading(`Edit ${name}`).waitFor();
  if ((await page.getByLabel(/^API Key/).inputValue()) !== "") {
    throw new Error("the key field was filled on edit");
  }
  await button("Continue").click();
  await button("Continue").click();
  await page.getByText("Review Configuration").waitFor();
  await button("Test connection").click();
  await page.getByText("Connection verified").waitFor();
  await page.getByRole("link", { name: "Cancel" }).click();
  await heading("Providers").waitFor();
  await card().getByText("Connected", { exact: true }).waitFor();

  // Change the models, save, and find them saved after a reload.
  await card().getByRole("button", { name: "Edit" }).click();
  await heading(`Edit ${name}`).waitFor();
  await button("Continue").click();
  await page.getByText(/Models & Pricing for/).waitFor();
  // A custom model leaves the table once it is unchecked, so the click is not followed by a state check.
  await page.getByRole("checkbox", { name: "Select alpha-1" }).click({ force: true });
  await page.getByRole("checkbox", { name: "Select alpha-1" }).waitFor({ state: "detached" });
  await page.getByLabel("Add Custom Model Identifier").fill("gamma-3");
  await page.getByLabel("Add Custom Model Identifier").press("Enter");
  await button("Continue").click();
  await page.getByText("Review Configuration").waitFor();
  await button("Test connection").click();
  await page.getByText("Connection verified").waitFor();
  await button("Save changes").click();
  await heading("Providers").waitFor();
  await card().getByText("gamma-3", { exact: true }).waitFor();

  await openProviders();
  await card().getByText("beta-2", { exact: true }).waitFor();
  await card().getByText("gamma-3", { exact: true }).waitFor();
  if ((await card().getByText("alpha-1", { exact: true }).count()) !== 0) {
    throw new Error("a model that was removed is still listed after a reload");
  }
  // The status the brain stored from the test survived the save.
  await card().getByText("Connected", { exact: true }).waitFor();

  // The key field is still empty on a later edit.
  await card().getByRole("button", { name: "Edit" }).click();
  await heading(`Edit ${name}`).waitFor();
  if ((await page.getByLabel(/^API Key/).inputValue()) !== "") {
    throw new Error("the key field was filled on a later edit");
  }
  await page.getByRole("link", { name: "Cancel" }).click();
  await heading("Providers").waitFor();

  // Remove it, and find it gone after a reload.
  await card().getByRole("button", { name: "Disconnect" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Disconnect provider" }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await card().waitFor({ state: "detached" });
  await openProviders();
  if ((await card().count()) !== 0) {
    throw new Error("a removed account is back after a reload");
  }

  console.log("providers check passed");
} finally {
  await browser.close();
}
