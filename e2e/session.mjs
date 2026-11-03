// Password set by e2e/passkeys.mjs on the bootstrap person.
const email = "ada@example.com";
const password = "correct horse battery";

export const storagePath = "/tmp/upmo-e2e-state.json";

export async function signInWithPassword(page, url) {
  await page.goto(new URL("/", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  const home = page.getByRole("heading", { level: 1, name: "Home" });
  const emailField = page.getByLabel("Email");
  // The app has not rendered at domcontentloaded: wait for whichever screen it settles on.
  try {
    await home.or(emailField).waitFor({ timeout: 15_000 });
  } catch (err) {
    const text = await page.locator("body").innerText().catch(() => "");
    throw new Error(`${err instanceof Error ? err.message : String(err)}\n${text.slice(0, 500)}`);
  }
  if (await home.isVisible()) {
    return;
  }
  await emailField.fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  const answered = page.waitForResponse((response) => response.url().endsWith("/api/sign-in/password"));
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  const response = await answered;
  if (response.status() === 429) {
    const retry = response.headers()["retry-after"];
    throw new Error(
      `password sign-in answered 429 (Retry-After ${retry ?? "unset"}s). ` +
        "The address throttle is spent; e2e/ensure-brain.sh empties it before each run in an upmo-e2e-* project.",
    );
  }
  if (!response.ok()) {
    throw new Error(`password sign-in answered ${response.status()}`);
  }
  try {
    await home.waitFor({ timeout: 15_000 });
  } catch (err) {
    const text = await page.locator("body").innerText().catch(() => "");
    throw new Error(`${err instanceof Error ? err.message : String(err)}\n${text.slice(0, 500)}`);
  }
}

// Personal settings and Log out live in the account menu at the foot of the sidebar.
export async function openAccountItem(page, name) {
  await page.getByRole("button", { name: /^User account: / }).click();
  await page.getByRole("menuitem", { name, exact: true }).click();
}
