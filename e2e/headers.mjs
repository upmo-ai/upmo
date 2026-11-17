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

// The dev server (pnpm dev) runs the dev policy; `vite preview` of a build runs
// the production one. Set CSP_MODE=production when WEB_URL is a preview server.
const production = process.env.CSP_MODE === "production";

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

function fail(message) {
  throw new Error(`UPM-282 ${message}`);
}

function directive(policy, name) {
  return policy
    .split(";")
    .map((part) => part.trim())
    .find((part) => part.startsWith(`${name} `))
    ?.slice(name.length + 1);
}

const violations = [];

try {
  const context = await browser.newContext({
    storageState: storagePath,
    viewport: { width: 1280, height: 900 },
    colorScheme: "light",
    reducedMotion: "reduce",
  });
  // The artifact screens read sample data until the artifacts backend lands:
  // the tasks list is answered 404, which is how the web knows to use them.
  await context.route(/\/api\/tasks$/, (route) =>
    route.fulfill({ status: 404, contentType: "application/json", body: '{"error":"not found"}' }),
  );

  // A blocked load is observed in the page, with the directive and the URL.
  await context.exposeFunction("reportViolation", (violation) => violations.push(violation));
  await context.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (event) => {
      window.reportViolation(`${event.violatedDirective} blocked ${event.blockedURI || "(inline)"}`);
    });
  });

  const page = await context.newPage();
  const document = await page.goto(new URL("/", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  const headers = document?.headers() ?? {};
  const policy = headers["content-security-policy"];
  if (!policy) fail("the document has no Content-Security-Policy");
  if (headers["content-security-policy-report-only"] !== undefined) {
    fail("the document has a Content-Security-Policy-Report-Only");
  }
  const script = directive(policy, "script-src");
  if (production && script !== "'self'") fail(`production script-src is ${script}`);
  if (!production && script !== "'self' 'unsafe-inline'") fail(`dev script-src is ${script}`);
  if (headers["x-frame-options"] !== "DENY") fail(`X-Frame-Options is ${headers["x-frame-options"]}`);
  if (headers["x-content-type-options"] !== "nosniff") fail("the document is not nosniff");
  if (!headers["permissions-policy"]?.includes("publickey-credentials-get=(self)")) {
    fail(`Permissions-Policy is ${headers["permissions-policy"]}`);
  }
  if (headers["strict-transport-security"] !== undefined) fail("plain HTTP document sent HSTS");

  await signInWithPassword(page, url);

  // The shell rendered under the policy.
  await page.getByRole("link", { name: "Tasks", exact: true }).waitFor();

  await page.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.getByRole("link", { name: "Daily finance digest" }).click();
  await page.getByRole("heading", { level: 1, name: "Daily finance digest" }).waitFor();
  await page.getByRole("tab", { name: "Artifacts" }).click();
  const list = page.getByRole("table", { name: "Artifacts" });
  await list.waitFor();

  // An image over data: still loads.
  await list.getByRole("button", { name: "spend-chart.png", exact: true }).click();
  const picture = page.getByRole("region", { name: "spend-chart.png" }).getByRole("img", { name: "spend-chart.png" });
  await picture.waitFor();
  if (!(await picture.evaluate((img) => img.complete && img.naturalWidth > 0))) {
    fail("the image did not load");
  }

  // A code artifact still mounts Monaco, its worker included.
  await list.getByRole("button", { name: "over_threshold.py", exact: true }).click();
  const code = page.getByRole("region", { name: "over_threshold.py" });
  await code.locator(".monaco-editor").waitFor();
  await code.locator(".view-lines").getByText("def over_threshold", { exact: false }).waitFor({ timeout: 15_000 });

  // The HTML preview still renders in its empty-sandbox iframe.
  await list.getByRole("button", { name: "digest-preview.html", exact: true }).click();
  const frameElement = page.getByTitle("digest-preview.html preview");
  await frameElement.waitFor();
  const frame = await (await frameElement.elementHandle())?.contentFrame();
  if (!frame) fail("the HTML preview has no frame");
  await frame.getByRole("heading", { name: "Finance digest" }).waitFor();

  // Give a late worker or font load a moment to report.
  await page.waitForTimeout(1000);
  if (violations.length > 0) {
    fail(`securitypolicyviolation: ${[...new Set(violations)].join("; ")}`);
  }
} catch (err) {
  if (violations.length > 0) {
    console.error(`securitypolicyviolation before the failure: ${[...new Set(violations)].join("; ")}`);
  }
  throw err;
} finally {
  await browser.close();
}
