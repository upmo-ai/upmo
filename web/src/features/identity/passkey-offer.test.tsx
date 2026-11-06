import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createStubApi, passkeyOfferStorageKey } from "../../app/api";
import { AppProviders } from "../../theme";

const offerName = "Add a passkey for next time";

function renderHome() {
  const history = createMemoryHistory({ initialEntries: ["/"] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

// The brain's /api/session answer. passkey_count is left out when the brain
// does not send it.
function stubSession(body: { sign_in_method?: string; passkey_count?: number }) {
  vi.stubGlobal(
    "fetch",
    vi.fn<(input: RequestInfo | URL) => Promise<Response>>(async (input) => {
      if (String(input).includes("/api/session")) {
        return new Response(JSON.stringify({ user_id: "sam", has_password: true, ...body }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return new Response("{}", { status: 200 });
    }),
  );
}

// Waits for Home, then gives the offer a moment to appear. True when it did.
async function offerAppears() {
  expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
  try {
    await screen.findByRole("region", { name: offerName }, { timeout: 300 });
    return true;
  } catch {
    return false;
  }
}

describe("passkey offer", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("passkey offer: a one-time link sign-in shows no offer", async () => {
    stubSession({ sign_in_method: "link", passkey_count: 0 });
    renderHome();
    expect(await offerAppears()).toBe(false);
  });

  it("passkey offer: a one-time link sign-in shows no offer when the session sends no passkey count", async () => {
    stubSession({ sign_in_method: "link" });
    renderHome();
    expect(await offerAppears()).toBe(false);
  });

  it("passkey offer: a password sign-in without a passkey shows the offer", async () => {
    stubSession({ sign_in_method: "password", passkey_count: 0 });
    renderHome();
    expect(await screen.findByRole("region", { name: offerName })).toBeTruthy();
  });

  it("passkey offer: a password sign-in with a passkey shows no offer", async () => {
    stubSession({ sign_in_method: "password", passkey_count: 1 });
    renderHome();
    expect(await offerAppears()).toBe(false);
  });

  it("passkey offer: a passkey sign-in shows no offer", async () => {
    stubSession({ sign_in_method: "passkey", passkey_count: 0 });
    renderHome();
    expect(await offerAppears()).toBe(false);
  });

  it("passkey offer: Not now hides it and it stays hidden after the api is created again", async () => {
    stubSession({ sign_in_method: "password", passkey_count: 0 });
    renderHome();
    const offer = await screen.findByRole("region", { name: offerName });
    act(() => {
      within(offer).getByRole("button", { name: "Not now" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: offerName })).toBeNull();
    });
    expect(localStorage.getItem(passkeyOfferStorageKey("sam"))).not.toBeNull();

    cleanup();
    renderHome();
    expect(await offerAppears()).toBe(false);
  });

  it("passkey offer: a successful Add passkey stays dismissed after the api is created again", async () => {
    stubSession({ sign_in_method: "password", passkey_count: 0 });
    renderHome();
    const offer = await screen.findByRole("region", { name: offerName });
    act(() => {
      within(offer).getByRole("button", { name: "Add passkey" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: offerName })).toBeNull();
    });

    cleanup();
    renderHome();
    expect(await offerAppears()).toBe(false);
  });

  it("passkey offer: the dismissal is kept per member", async () => {
    const first = createStubApi();
    await first.me.dismissPasskeyOffer();
    expect((await first.me.current()).offerDismissed).toBe(true);

    const again = createStubApi();
    expect((await again.me.current()).offerDismissed).toBe(true);
    expect((await again.me.viewAs("member")).offerDismissed).toBe(false);
    expect((await again.me.viewAs("admin")).offerDismissed).toBe(true);
  });

  it("passkey offer: a dismissal survives a reload of the session", async () => {
    stubSession({ sign_in_method: "password", passkey_count: 0 });
    const api = createStubApi();
    await api.me.dismissPasskeyOffer();
    expect((await api.session.get()).offerDismissed).toBe(true);
    expect((await api.me.current()).offerDismissed).toBe(true);
    expect(localStorage.getItem(passkeyOfferStorageKey("sam"))).not.toBeNull();
  });

  it("passkey offer: a new sign-in clears the stored dismissal for that member only", async () => {
    const api = createStubApi();
    await api.me.dismissPasskeyOffer();
    await api.me.viewAs("member");
    await api.me.dismissPasskeyOffer();
    await api.me.viewAs("admin");

    expect((await api.me.signIn("password")).offerDismissed).toBe(false);
    expect(localStorage.getItem(passkeyOfferStorageKey("sam"))).toBeNull();
    expect((await api.me.viewAs("member")).offerDismissed).toBe(true);
  });

  it("passkey offer: a new sign-in clears the in-page dismissal when storage is blocked", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const api = createStubApi();
    await api.me.dismissPasskeyOffer();
    expect((await api.me.current()).offerDismissed).toBe(true);
    expect((await api.me.signIn("password")).offerDismissed).toBe(false);
  });

  it("passkey offer: the passkey count is unknown when the session sends none, and the offer still shows", async () => {
    stubSession({ sign_in_method: "password" });
    const api = createStubApi();
    expect((await api.session.get()).passkeyCount).toBeUndefined();
    expect((await api.me.signIn("password")).passkeyCount).toBeUndefined();
    await api.passkeys.add("This device");
    expect((await api.session.get()).passkeyCount).toBeUndefined();

    cleanup();
    localStorage.clear();
    renderHome();
    expect(await screen.findByRole("region", { name: offerName })).toBeTruthy();
  });

  it("passkey offer: the passkey count is read from the session when it is sent", async () => {
    stubSession({ sign_in_method: "password", passkey_count: 2 });
    const api = createStubApi();
    expect((await api.session.get()).passkeyCount).toBe(2);
  });

  it("passkey offer: storage that throws does not break the app", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    stubSession({ sign_in_method: "password", passkey_count: 0 });
    renderHome();
    const offer = await screen.findByRole("region", { name: offerName });
    act(() => {
      within(offer).getByRole("button", { name: "Not now" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: offerName })).toBeNull();
    });
    expect(screen.getByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
  });
});
