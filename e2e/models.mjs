import { createRequire } from "node:module";
import { signInWithPassword, storagePath } from "./session.mjs";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 here. NODE_PATH does not apply to ESM imports.
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

  // An admin finds the page in the sidebar and reads both tables.
  await page.getByRole("link", { name: "Models and routes", exact: true }).click();
  await page.getByRole("heading", { level: 1, name: "Models and routes" }).waitFor();
  const routes = page.getByRole("table", { name: "Routes", exact: true });
  await routes.waitFor();
  await routes.getByRole("cell", { name: "agent-dispatch", exact: true }).waitFor();

  await routes.getByRole("button", { name: "Show 3 hops for chat", exact: true }).click();
  const hops = page.getByRole("table", { name: "Hops for chat", exact: true });
  await hops.getByRole("cell", { name: "claude-3-7-sonnet", exact: true }).waitFor();
  await routes.getByRole("button", { name: "Hide 3 hops for chat", exact: true }).click();
  await hops.waitFor({ state: "detached" });

  const gates = page.getByRole("table", { name: "Decision gates", exact: true });
  await gates.waitFor();
  const dispatch = gates.getByRole("row").filter({ has: page.getByRole("cell", { name: "agent-dispatch", exact: true }) });
  await dispatch.getByText("Meets bar", { exact: true }).waitFor();
  const tier = gates.getByRole("row").filter({ has: page.getByRole("cell", { name: "model-tier", exact: true }) });
  await tier.getByText("Below bar", { exact: true }).waitFor();

  // Narrow screens scroll the tables inside their own region, not the page.
  await page.setViewportSize({ width: 360, height: 800 });
  await page.getByRole("heading", { level: 1, name: "Models and routes" }).waitFor();
  const widths = await page.evaluate(() => ({
    viewport: window.innerWidth,
    scroll: document.documentElement.scrollWidth,
  }));
  if (widths.scroll > widths.viewport) {
    throw new Error(`models page scrolls sideways: ${JSON.stringify(widths)}`);
  }
  await page.setViewportSize({ width: 1280, height: 800 });

  console.log("models check passed");
} finally {
  await browser.close();
}
