import { createRequire } from "node:module";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.55.1 here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

function channels(color) {
  const match =
    /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\s*\)$/.exec(
      color.trim(),
    );
  if (!match) {
    throw new Error(`expected a computed color, got ${color}`);
  }
  return {
    r: Number(match[1]),
    g: Number(match[2]),
    b: Number(match[3]),
    a: match[4] === undefined ? 1 : Number(match[4]),
  };
}

function sameColor(left, right) {
  const a = channels(left);
  const b = channels(right);
  return a.r === b.r && a.g === b.g && a.b === b.b && a.a === b.a;
}

function colorsMatch(left, right) {
  try {
    return sameColor(left, right);
  } catch {
    return false;
  }
}

function isTransparent(color) {
  if (color === "transparent") return true;
  try {
    return channels(color).a === 0;
  } catch {
    return false;
  }
}

// --surface-000 is read by painting it onto a probe. getPropertyValue returns
// the specified token, which is not comparable to a computed background-color.
async function readSurface(page) {
  return page.evaluate(() => {
    const probe = document.createElement("div");
    probe.style.backgroundColor = "var(--surface-000)";
    document.body.appendChild(probe);
    const token = getComputedStyle(probe).backgroundColor;
    probe.remove();

    const transparent = (value) =>
      value === "transparent" || value === "rgba(0, 0, 0, 0)";
    // Canvas background: the root element, or the body when the root is transparent.
    const root = getComputedStyle(document.documentElement).backgroundColor;
    const body = getComputedStyle(document.body).backgroundColor;
    const pageBackground = transparent(root) ? body : root;

    return {
      theme: document.documentElement.getAttribute("data-theme"),
      token,
      pageBackground,
    };
  });
}

async function settle(page, theme) {
  const deadline = Date.now() + 15_000;
  let last;
  while (Date.now() < deadline) {
    last = await readSurface(page);
    if (
      last.theme === theme &&
      !isTransparent(last.token) &&
      !isTransparent(last.pageBackground) &&
      colorsMatch(last.pageBackground, last.token)
    ) {
      return last;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`${theme} theme did not settle: ${JSON.stringify(last)}`);
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  const context = await browser.newContext({
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  const page = await context.newPage();
  // Vite keeps a websocket open, so networkidle does not settle.
  await page.goto(url, { waitUntil: "domcontentloaded", timeout: 30_000 });
  try {
    await page.getByRole("heading", { level: 1, name: "Home" }).waitFor();
  } catch (error) {
    const text = await page.locator("body").innerText().catch(() => "");
    throw new Error(`${error.message}\n${text.slice(0, 500)}`);
  }

  const themeSwitch = page.getByTestId("theme-switch");
  await themeSwitch.waitFor();
  for (const name of ["Light", "Dark", "System"]) {
    await themeSwitch.getByText(name, { exact: true }).waitFor();
  }

  // The native radio is visually hidden and sits outside the viewport.
  // The visible label is what a person clicks.
  const choose = (name) => themeSwitch.getByText(name, { exact: true }).click();

  await choose("Light");
  const light = await settle(page, "light");

  await choose("Dark");
  const dark = await settle(page, "dark");
  if (sameColor(light.token, dark.token)) {
    throw new Error(
      `dark --surface-000 ${dark.token} matches the light value; the dark token did not change`,
    );
  }

  console.log(`light --surface-000 ${light.token}`);
  console.log(`dark --surface-000 ${dark.token}`);
  console.log("theme check passed");
} finally {
  await browser.close();
}
