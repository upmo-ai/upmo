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

  // Connect GitHub through the wizard. Everything after this stays inside the
  // app, so the rules the page keeps are not lost to a reload.
  await page.goto(new URL("/connectors/new", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  await page.getByRole("heading", { level: 1, name: "Connect a tool" }).waitFor();
  for (let step = 0; step < 3; step += 1) {
    await page.getByRole("button", { name: "Continue" }).click();
  }
  await page.getByRole("button", { name: "Test connection" }).click();
  await page.getByText("Connection verified").waitFor();
  await page.getByRole("button", { name: "Connect tool" }).click();
  await page.getByTestId("connector-card-github").waitFor();

  // The current rule is shown for the connector.
  const rules = page.getByRole("group", { name: "GitHub rules" });
  await rules.waitFor();
  for (const text of ["Read: Allow", "Change: Ask first", "Delete: Ask first"]) {
    if (!(await rules.getByText(text, { exact: true }).isVisible())) {
      throw new Error(`rules summary did not show ${text}`);
    }
  }

  // Read and change can be set. Delete has no control.
  await page.getByRole("button", { name: "Edit rules for GitHub" }).click();
  const dialog = page.getByRole("dialog", { name: "GitHub rules" });
  await dialog.waitFor();
  if ((await dialog.getByRole("radiogroup").count()) !== 2) {
    throw new Error("the rules editor did not offer exactly the read and change controls");
  }
  await dialog.getByText("Deleting always asks first. This rule is fixed and cannot be changed.").waitFor();
  if (!(await dialog.getByRole("button", { name: "Save rules" }).isDisabled())) {
    throw new Error("Save rules was enabled before any change");
  }
  await dialog.getByRole("radiogroup", { name: "Read rule" }).getByText("Block", { exact: true }).click();
  await dialog.getByRole("radiogroup", { name: "Change rule" }).getByText("Allow", { exact: true }).click();
  await dialog.getByRole("button", { name: "Save rules" }).click();
  await dialog.waitFor({ state: "hidden" });
  await rules.getByText("Read: Block", { exact: true }).waitFor();
  await rules.getByText("Change: Allow", { exact: true }).waitFor();
  await rules.getByText("Delete: Ask first", { exact: true }).waitFor();

  // The saved rules are what the editor opens with.
  await page.getByRole("button", { name: "Edit rules for GitHub" }).click();
  await dialog.waitFor();
  if (!(await dialog.getByRole("radiogroup", { name: "Read rule" }).getByRole("radio", { name: "Block" }).isChecked())) {
    throw new Error("the editor did not open on the saved read rule");
  }
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await dialog.waitFor({ state: "hidden" });

  // A global connector shows its rules read only, with a Global marker.
  const globalRules = page.getByRole("group", { name: "Google Drive rules" });
  await globalRules.waitFor();
  await globalRules.getByText("Global", { exact: true }).waitFor();
  if ((await page.getByRole("button", { name: "Edit rules for Google Drive" }).count()) !== 0) {
    throw new Error("a global connector offered a rules editor");
  }
  if ((await page.getByTestId("connector-card-google-drive").getByRole("button").count()) !== 0) {
    throw new Error("a global connector offered edit or disconnect");
  }

  await page.setViewportSize({ width: 360, height: 800 });
  await page.getByTestId("connector-card-github").waitFor();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  if (overflow > 0) {
    throw new Error(`connectors page scrolls sideways on a phone by ${overflow}px`);
  }

  // Leave the connector as it was found.
  await page.getByTestId("connector-card-github").getByRole("button", { name: "Disconnect" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Disconnect connector" }).click();
  await page.getByTestId("connector-card-github").waitFor({ state: "detached" });

  console.log("connectors check passed");
} finally {
  await browser.close();
}
