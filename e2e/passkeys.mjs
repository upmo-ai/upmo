import { createRequire } from "node:module";
import { openAccountItem, savePasskey } from "./session.mjs";

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
  let phase = "starting";

  // The address throttle answers 429 and a denied attempt still costs a token,
  // so the first 429 ends the run instead of leaving a wait to time out.
  let rateLimited;
  const limited = new Promise((_, reject) => {
    rateLimited = reject;
  });
  limited.catch(() => {});
  const within = (promise) => Promise.race([promise, limited]);

  // Every passkey options request spends a token from the address throttle.
  let signInOptionsRequests = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && new URL(request.url()).pathname === "/api/passkeys/sign-in/options") {
      signInOptionsRequests += 1;
    }
  });

  page.on("console", (msg) => notes.push(`console ${msg.type()}: ${msg.text()}`));
  page.on("response", (response) => {
    if (!response.url().includes("/api/")) return;
    const request = response.request();
    notes.push(`${response.status()} ${request.method()} ${response.url()}`);
    if (response.status() === 429) {
      const retry = response.headers()["retry-after"];
      rateLimited(
        new Error(
          `${request.method()} ${new URL(response.url()).pathname} answered 429 (Retry-After ${retry ?? "unset"}s). ` +
            "The address throttle is spent; e2e/ensure-brain.sh empties it before each run in an upmo-e2e-* project only.",
        ),
      );
    }
  });
  const fail = async (err) => {
    const text = await page.locator("body").innerText().catch(() => "");
    throw new Error(
      `passkey check failed while ${phase}: ${err instanceof Error ? err.message : String(err)}\n${text}\n${notes.join("\n")}`,
    );
  };
  // A response to wait for, created before the click that causes it. The catch
  // keeps an unused wait from becoming an unhandled rejection after a failure.
  const expectResponse = (match) => {
    const response = page.waitForResponse(match);
    response.catch(() => {});
    return response;
  };
  const isApi = (response, method, path) =>
    response.request().method() === method && new URL(response.url()).pathname === path;

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

  const home = page.getByRole("heading", { level: 1, name: "Home" });
  const offer = page.getByRole("region", { name: "Add a passkey for next time" });
  const passkeyItems = page.getByRole("list", { name: "Passkeys" }).getByRole("listitem");
  const setPassword = page.getByRole("button", { name: "Set password" });
  const changePassword = page.getByRole("button", { name: "Change password" });

  // Sign in with the password from the log in page. The server decides whether
  // to offer a passkey; the page shows the offer once Home has rendered, so the
  // session read that fills it has to be in before the offer can be judged.
  const logInWithPassword = async (wantOffer) => {
    await page.getByLabel("Email").fill("ada@example.com");
    await page.getByLabel("Password", { exact: true }).fill("correct horse battery");
    const answered = expectResponse((response) => isApi(response, "POST", "/api/sign-in/password"));
    const read = expectResponse((response) => isApi(response, "GET", "/api/session") && response.ok());
    await page.getByRole("button", { name: "Log in", exact: true }).click();
    const response = await within(answered);
    if (!response.ok()) {
      throw new Error(`password sign-in answered ${response.status()}`);
    }
    const body = await response.json();
    if (body.offer_passkey !== wantOffer) {
      throw new Error(
        `password sign-in said offer_passkey ${body.offer_passkey}, want ${wantOffer}. ` +
          "The e2e person keeps passkey_offered_at between runs; e2e/ensure-brain.sh resets it in an upmo-e2e-* project only, never in the default project.",
      );
    }
    await within(home.waitFor({ timeout: 15_000 }));
    await within(read);
  };

  // A wrong password leaves the autofill request the page already has in
  // place. A second options request would spend another throttle token.
  const wrongPasswordAddsNoOptionsRequest = async () => {
    await page.getByLabel("Email").waitFor();
    await page.waitForLoadState("networkidle", { timeout: 10_000 });
    const before = signInOptionsRequests;
    if (before === 0) {
      throw new Error("the log in page did not ask for passkey options, so the check below would prove nothing");
    }
    await page.getByLabel("Email").fill("ada@example.com");
    await page.getByLabel("Password", { exact: true }).fill("wrong horse battery");
    const answered = expectResponse((response) => isApi(response, "POST", "/api/sign-in/password"));
    await page.getByRole("button", { name: "Log in", exact: true }).click();
    const response = await within(answered);
    if (response.status() !== 401) {
      throw new Error(`wrong password answered ${response.status()}, want 401`);
    }
    await within(page.getByText("Email or password is wrong.").waitFor());
    // A re-armed ceremony posts within a few milliseconds of the message.
    await page.waitForTimeout(1_000);
    if (signInOptionsRequests !== before) {
      throw new Error(
        `a wrong password sent ${signInOptionsRequests - before} extra POST /api/passkeys/sign-in/options request(s)`,
      );
    }
  };

  try {
    phase = "creating the passkey from the sign-in link";
    await page.goto(link, { waitUntil: "domcontentloaded", timeout: 30_000 });
    const create = page.getByRole("button", { name: "Create your passkey" });
    await within(create.waitFor());
    // Strict Mode remounts the button; noWaitAfter keeps the click from retrying
    // after the page leaves this screen.
    await create.click({ noWaitAfter: true });
    await within(home.waitFor({ timeout: 15_000 }));

    phase = "ending this session from the Security page";
    await openAccountItem(page, "Security");
    await within(page.getByRole("heading", { level: 2, name: "Where you're logged in" }).waitFor());
    await within(passkeyItems.first().waitFor());
    const ended = expectResponse(
      (response) =>
        response.request().method() === "DELETE" && /^\/api\/sessions\/[^/]+$/.test(new URL(response.url()).pathname),
    );
    await page.getByRole("list", { name: "Sessions" }).getByRole("button", { name: "Log out" }).click();
    const gone = await within(ended);
    if (!gone.ok()) {
      throw new Error(`ending the session answered ${gone.status()}`);
    }
    await within(page.getByRole("heading", { level: 2, name: "Where you're logged in" }).waitFor({ state: "hidden" }));

    phase = "signing in with the passkey from the log in page";
    const finished = expectResponse((response) => isApi(response, "POST", "/api/passkeys/sign-in/finish"));
    await page.goto(new URL("/login", link).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
    const finish = await within(finished);
    if (!finish.ok()) {
      throw new Error(`passkey sign-in answered ${finish.status()}`);
    }
    await within(home.waitFor({ timeout: 15_000 }));

    phase = "setting a password";
    await openAccountItem(page, "Security");
    await within(passkeyItems.first().waitFor());
    // The Password section follows the list, so either button means it has rendered.
    await within(setPassword.or(changePassword).waitFor());
    if (await setPassword.isVisible()) {
      await setPassword.click();
      await page.getByLabel("New password").fill("correct horse battery");
      await page.getByRole("dialog").getByRole("button", { name: "Set password" }).click();
    }
    await within(changePassword.waitFor());

    // Later checks sign in with the password and step up with this passkey.
    await savePasskey(client, authenticator.authenticatorId);

    phase = "logging in with the password and getting the passkey offer";
    await client.send("WebAuthn.removeVirtualAuthenticator", {
      authenticatorId: authenticator.authenticatorId,
    });
    await openAccountItem(page, "Log out");
    await wrongPasswordAddsNoOptionsRequest();
    await logInWithPassword(true);
    await within(offer.waitFor({ timeout: 10_000 }));

    phase = "logging in with the password again and finding no offer";
    await openAccountItem(page, "Log out");
    await logInWithPassword(false);
    if (await offer.count()) {
      throw new Error("the passkey offer showed a second time");
    }
  } catch (err) {
    await fail(err);
  }
  await context.storageState({ path: "/tmp/upmo-e2e-state.json" });
  console.log("passkey check passed");
} finally {
  await browser.close();
}
