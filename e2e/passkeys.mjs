import { createRequire } from "node:module";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const link = process.env.UPMO_LINK;
if (!link || !link.startsWith("http")) {
  throw new Error("UPMO_LINK is not set");
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
  const notes = [];
  page.on("console", (msg) => notes.push(`console ${msg.type()}: ${msg.text()}`));
  page.on("response", (response) => {
    if (response.url().includes("/api/")) notes.push(`${response.status()} ${response.url()}`);
  });
  const fail = async (err) => {
    const text = await page.locator("body").innerText().catch(() => "");
    throw new Error(`${err instanceof Error ? err.message : String(err)}\n${text}\n${notes.join("\n")}`);
  };
  const client = await context.newCDPSession(page);
  await client.send("WebAuthn.enable");
  const authenticator = await client.send("WebAuthn.addVirtualAuthenticator", {
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

  await page.goto(link, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("button", { name: "Create your passkey" }).click();
  try {
    await page.getByRole("heading", { level: 1, name: "Home" }).waitFor({ timeout: 15_000 });
  } catch (err) {
    await fail(err);
  }

  await page.getByRole("link", { name: "Settings", exact: true }).click();
  try {
    await page.getByRole("heading", { level: 2, name: "Where you're logged in" }).waitFor();
    await page.getByRole("list", { name: "Passkeys" }).getByRole("listitem").first().waitFor();
    await page.getByRole("list", { name: "Sessions" }).getByRole("button", { name: "Log out" }).click();
    await page.getByRole("heading", { level: 2, name: "Where you're logged in" }).waitFor({ state: "hidden" });

    const signIn = new URL("/login", link);
    await page.goto(signIn.href, { waitUntil: "domcontentloaded", timeout: 30_000 });
    await page.getByRole("heading", { level: 1, name: "Home" }).waitFor({ timeout: 15_000 });

    await page.getByRole("link", { name: "Settings", exact: true }).click();
    await page.getByRole("button", { name: "Set password" }).click();
    await page.getByLabel("New password").fill("correct horse battery");
    await page.getByRole("dialog").getByRole("button", { name: "Set password" }).click();
    await page.getByRole("button", { name: "Change password" }).waitFor();

    await client.send("WebAuthn.removeVirtualAuthenticator", {
      authenticatorId: authenticator.authenticatorId,
    });
    await page.locator(".shell-sign-out").click();
    await page.getByLabel("Email").fill("ada@example.com");
    await page.getByLabel("Password").fill("correct horse battery");
    await page.getByRole("button", { name: "Log in", exact: true }).click();
    await page.getByRole("heading", { level: 1, name: "Home" }).waitFor({ timeout: 15_000 });
    await page.getByRole("region", { name: "Add a passkey for next time" }).waitFor();

    await page.locator(".shell-sign-out").click();
    await page.getByLabel("Email").fill("ada@example.com");
    await page.getByLabel("Password").fill("correct horse battery");
    await page.getByRole("button", { name: "Log in", exact: true }).click();
    await page.getByRole("heading", { level: 1, name: "Home" }).waitFor({ timeout: 15_000 });
    await page.getByRole("region", { name: "Add a passkey for next time" }).waitFor({ state: "hidden", timeout: 2_000 });
  } catch (err) {
    await fail(err);
  }
  console.log("passkey check passed");
} finally {
  await browser.close();
}
