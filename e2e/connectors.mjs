import { createRequire } from "node:module";
import { signInWithPassword, storagePath } from "./session.mjs";

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
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 800 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
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
  await page.getByRole("button", { name: "Connect tool" }).click();
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

  // Leave the connector as it was found.
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.waitForTimeout(1000);
  // The stub keeps the connector in memory only. If the page was reloaded by now
  // the list is empty and there is nothing left to disconnect.
  const disconnect = page.getByRole("button", { name: "Disconnect" });
  const emptyList = page.getByText("Connect your tools", { exact: true });
  await disconnect.or(emptyList).first().waitFor({ timeout: 15_000 });
  if (!(await emptyList.isVisible())) {
    await disconnect.click();
    await page.getByRole("dialog").getByRole("button", { name: "Disconnect connector" }).click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page.getByRole("button", { name: "Disconnect", exact: true }).waitFor({ state: "detached" });
  }

  console.log("connectors check passed");
} finally {
  await browser.close();
}
