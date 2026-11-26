import { createRequire } from "node:module";
import { signInWithPassword } from "./session.mjs";

const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  // No saved state: a fresh password sign-in, so an earlier check's step-up on
  // the shared session cannot make this one fresh already.
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  // A password session on a passkey account: grant writes need the step-up prompt.
  const stepUps = [];
  page.on("response", (response) => {
    if (response.request().method() === "POST" && new URL(response.url()).pathname === "/api/passkeys/step-up/finish") {
      stepUps.push(response.status());
    }
  });
  await signInWithPassword(page, url);

  // Connect GitHub through the wizard. Against the live brain the rules routes
  // do not exist yet, so the page must say so instead of inventing rules.
  await page.goto(new URL("/connectors/new", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Connect a tool" }).waitFor();
  for (let step = 0; step < 3; step += 1) {
    await page.getByRole("button", { name: "Continue" }).click();
  }
  await page.getByRole("button", { name: "Test connection", exact: true }).click();
  await page.getByText("Connection verified").waitFor();
  const saved = page.waitForResponse(
    (response) => response.request().method() === "PUT" && new URL(response.url()).pathname === "/api/connector-grants/github" && response.ok(),
  );
  await page.getByRole("button", { name: "Connect tool" }).click();
  await saved;
  if (stepUps.length !== 1 || stepUps[0] !== 204) {
    throw new Error(`Connect tool ran the step-up prompt ${stepUps.length} time(s) (${stepUps.join(", ")}), want once with 204`);
  }
  const description = page.getByText("Repositories, issues and pull requests", { exact: true });
  await description.waitFor();
  await page.getByText("Loading rules").waitFor({ state: "detached", timeout: 20000 });

  await page.setViewportSize({ width: 360, height: 800 });
  await page.getByRole("heading", { level: 1, name: "Connectors" }).waitFor();
  await page.waitForTimeout(1000);
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  if (overflow > 0) {
    throw new Error(`connectors page scrolls sideways on a phone by ${overflow}px`);
  }

  // Leave the connector as it was found. Scope to the GitHub card: the wizard
  // connects GitHub, and the Disconnect step must always run against it.
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByTestId("connector-card-github").getByRole("button", { name: "Disconnect" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Disconnect connector" }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page.getByTestId("connector-card-github").waitFor({ state: "detached" });
  // The step-up is fresh for ten minutes, so the gated delete needed no second prompt.
  if (stepUps.length !== 1) {
    throw new Error(`Disconnect ran the step-up prompt again: ${stepUps.length} step-ups, want 1`);
  }

  console.log("connectors check passed");
} finally {
  await browser.close();
}
