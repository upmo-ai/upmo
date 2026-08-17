// Password set by e2e/passkeys.mjs on the bootstrap person.
const email = "ada@example.com";
const password = "correct horse battery";

export const storagePath = "/tmp/upmo-e2e-state.json";

export async function signInWithPassword(page, url) {
  await page.goto(new URL("/", url).href, { waitUntil: "domcontentloaded", timeout: 30_000 });
  const home = page.getByRole("heading", { level: 1, name: "Home" });
  if (await home.isVisible().catch(() => false)) {
    return;
  }
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  try {
    await home.waitFor({ timeout: 15_000 });
  } catch (err) {
    const text = await page.locator("body").innerText().catch(() => "");
    throw new Error(`${err instanceof Error ? err.message : String(err)}\n${text.slice(0, 500)}`);
  }
}
