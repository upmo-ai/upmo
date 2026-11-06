import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../App";
import { AppProviders } from "../theme";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

function stubPassword(offer: boolean | (() => boolean)) {
  vi.stubGlobal(
    "fetch",
    vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
      if (String(input).includes("/api/sign-in/password")) {
        const offerPasskey = typeof offer === "function" ? offer() : offer;
        return new Response(JSON.stringify({ offer_passkey: offerPasskey }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      if (String(input).includes("/api/session")) {
        return new Response(JSON.stringify({ sign_in_method: "password", passkey_count: 0 }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return new Response("", { status: 404 });
    }),
  );
}

const offerName = "Add a passkey for next time";

async function logOut() {
  act(() => {
    screen.getByRole("button", { name: /Sam Rivera/i }).click();
  });
  act(() => {
    screen.getByRole("menuitem", { name: "Log out" }).click();
  });
  expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
}

async function submitPassword() {
  fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
  act(() => {
    screen.getByRole("button", { name: /^Log in$/ }).click();
  });
}

type Get = { mediation?: string; signal?: AbortSignal };

// A browser that offers autofill. The conditional prompt stays open until it is
// aborted; the modal prompt answers with a credential. firstGetEnds makes the
// first conditional prompt end by itself, as when the person dismisses it.
function stubPasskeyBrowser(opts: { firstGetEnds?: boolean } = {}) {
  const events: string[] = [];
  const gets: Get[] = [];
  let options = 0;
  let challenge = 0;
  vi.stubGlobal("PublicKeyCredential", {
    isConditionalMediationAvailable: async () => true,
    parseRequestOptionsFromJSON: (value: unknown) => value,
  });
  const { platform, userAgent } = navigator;
  vi.stubGlobal("navigator", {
    platform,
    userAgent,
    credentials: {
      get: (request: Get) =>
        new Promise((resolve, reject) => {
          gets.push(request);
          events.push(`get ${request.mediation === "conditional" ? "conditional" : "modal"}`);
          if (request.mediation !== "conditional") {
            resolve({ toJSON: () => ({ id: "cred-1" }) });
            return;
          }
          if (opts.firstGetEnds && gets.length === 1) {
            resolve(null);
            return;
          }
          request.signal?.addEventListener("abort", () => {
            events.push("abort");
            reject(new DOMException("aborted", "AbortError"));
          });
        }),
    },
  });
  vi.stubGlobal(
    "fetch",
    vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
      const url = String(input);
      if (url.includes("/api/passkeys/sign-in/options")) {
        options += 1;
        challenge = options;
        events.push(`options ${options}`);
        return new Response(JSON.stringify({ publicKey: {} }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      if (url.includes("/api/passkeys/sign-in/finish")) {
        events.push(`finish ${challenge}`);
        return new Response(null, { status: 204 });
      }
      if (url.includes("/api/sign-in/password")) {
        return new Response("{}", { status: 401 });
      }
      return new Response("", { status: 404 });
    }),
  );
  return { events, gets, optionsRequests: () => options };
}

describe("sign in", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("passkey offer: logs in through the password form when the server accepts it", async () => {
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(
      async (input) => {
        if (String(input).includes("/api/sign-in/password")) {
          return new Response(JSON.stringify({ offer_passkey: true }), {
            status: 200,
            headers: { "content-type": "application/json" },
          });
        }
        if (String(input).includes("/api/session")) {
          return new Response(JSON.stringify({ sign_in_method: "password", passkey_count: 0 }), {
            status: 200,
            headers: { "content-type": "application/json" },
          });
        }
        return new Response("", { status: 404 });
      },
    );
    vi.stubGlobal("fetch", fetchMock);
    renderAt("/login");
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
    act(() => {
      screen.getByRole("button", { name: /^Log in$/ }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    expect(await screen.findByRole("region", { name: "Add a passkey for next time" })).toBeTruthy();
    const call = fetchMock.mock.calls.find((item) => String(item[0]).includes("/api/sign-in/password"));
    const body = JSON.parse(String(call?.[1]?.body)) as { email: string; password: string };
    expect(body).toEqual({ email: "ada@example.com", password: "correct horse battery" });
  });

  it("stays on the page when the password is wrong", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<() => Promise<Response>>(async () => new Response("{}", { status: 401 })),
    );
    renderAt("/login");
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "not the password" } });
    act(() => {
      screen.getByRole("button", { name: /^Log in$/ }).click();
    });
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Email or password is wrong.");
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
  });

  it("puts Log in under the password and keeps the passkey as the other choice", async () => {
    renderAt("/login");
    const brand = await screen.findByRole("link", { name: "Upmo" });
    expect(brand.getAttribute("href")).toBe("/login");
    expect(brand.querySelector('svg[aria-hidden="true"]')).toBeTruthy();
    expect(brand.querySelector("img")).toBeNull();
    expect(screen.getAllByRole("link")[0]?.textContent).toBe("Skip to content");
    const password = screen.getByLabelText("Password");
    const logIn = screen.getByRole("button", { name: /^Log in$/ });
    const passkey = screen.getByRole("button", { name: "Log in with a passkey" });
    expect(passkey.querySelector("svg")).toBeTruthy();
    expect(password.compareDocumentPosition(logIn) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(logIn.compareDocumentPosition(passkey) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Use a password instead" })).toBeNull();
    expect(screen.getByRole("link", { name: "Terms of use" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Privacy policy" })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();

    act(() => {
      passkey.click();
    });
    expect(await screen.findByRole("alert")).toHaveProperty(
      "textContent",
      "The passkey prompt was cancelled. Try again.",
    );
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();

    stubPassword(true);
    await submitPassword();
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Upmo" }).getAttribute("href")).toBe("/");
    });
    expect(screen.getByTestId("theme-switch")).toBeTruthy();
    expect(screen.getByText("v0.1.0")).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: /Sam Rivera/i }).click();
    });
    act(() => {
      screen.getByRole("menuitem", { name: "Log out" }).click();
    });
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();

    await submitPassword();
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
  });

  it("uses the same agreement on the one-time link page", async () => {
    renderAt("/login/link");
    const create = await screen.findByRole("button", { name: "Create your passkey" });
    expect(create.querySelector("svg")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 1, name: "Create your passkey" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Terms of use" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Privacy policy" })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
  });

  it("keeps the one-time link page when the session is empty", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<(input: RequestInfo | URL) => Promise<Response>>(async (input) => {
        if (String(input).includes("/api/session")) {
          return new Response(JSON.stringify({ error: "unauthorised" }), {
            status: 401,
            headers: { "content-type": "application/json" },
          });
        }
        return new Response("", { status: 404 });
      }),
    );
    renderAt("/login/link");
    expect(await screen.findByRole("heading", { level: 1, name: "Create your passkey" })).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Create your passkey" })).toBeTruthy();
    });
    expect(screen.queryByRole("button", { name: /^Log in$/ })).toBeNull();
  });

  it("sends the logo home when a live session exists", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<() => Promise<Response>>(async () => new Response("{}", { status: 200 })),
    );
    renderAt("/terms");
    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Upmo" }).getAttribute("href")).toBe("/");
    });
  });

  it("opens the terms and the privacy policy from the agreement", async () => {
    renderAt("/login");
    const terms = await screen.findByRole("link", { name: "Terms of use" });
    act(() => {
      terms.click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Terms of use" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Your account" })).toBeTruthy();
    expect(screen.getByText(/Upmo does not claim ownership/)).toBeTruthy();
    act(() => {
      screen.getByRole("link", { name: "Back to log in" }).click();
    });
    const privacy = await screen.findByRole("link", { name: "Privacy policy" });
    act(() => {
      privacy.click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Privacy policy" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "How you log in" })).toBeTruthy();
    expect(screen.getByText(/does not sell personal information/)).toBeTruthy();
  });

  it("passkey offer: shows only after a password sign-in, and only once", async () => {
    let posts = 0;
    stubPassword(() => {
      posts += 1;
      return posts === 1;
    });
    renderAt("/login");
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Add a passkey for next time" })).toBeNull();
    await submitPassword();
    expect(await screen.findByRole("region", { name: "Add a passkey for next time" })).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Not now" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: "Add a passkey for next time" })).toBeNull();
    });

    act(() => {
      screen.getByRole("button", { name: /Sam Rivera/i }).click();
    });
    act(() => {
      screen.getByRole("menuitem", { name: "Log out" }).click();
    });
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    await submitPassword();
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await expect(
      screen.findByRole("region", { name: "Add a passkey for next time" }, { timeout: 300 }),
    ).rejects.toThrow(/Add a passkey for next time/);
  });

  it("passkey offer: dismissed, a reload keeps it hidden", async () => {
    stubPassword(true);
    renderAt("/login");
    await submitPassword();
    const offer = await screen.findByRole("region", { name: offerName });
    act(() => {
      within(offer).getByRole("button", { name: "Not now" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: offerName })).toBeNull();
    });

    cleanup();
    renderAt("/");
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await expect(screen.findByRole("region", { name: offerName }, { timeout: 300 })).rejects.toThrow(
      /Add a passkey for next time/,
    );
  });

  it("passkey offer: dismissed, signing in again with offer_passkey true shows it again", async () => {
    stubPassword(true);
    renderAt("/login");
    await submitPassword();
    const offer = await screen.findByRole("region", { name: offerName });
    act(() => {
      within(offer).getByRole("button", { name: "Not now" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: offerName })).toBeNull();
    });

    await logOut();
    await submitPassword();
    expect(await screen.findByRole("region", { name: offerName })).toBeTruthy();
  });

  it("passkey offer: dismissed, signing in again with offer_passkey false stays hidden", async () => {
    let posts = 0;
    stubPassword(() => {
      posts += 1;
      return posts === 1;
    });
    renderAt("/login");
    await submitPassword();
    const offer = await screen.findByRole("region", { name: offerName });
    act(() => {
      within(offer).getByRole("button", { name: "Not now" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: offerName })).toBeNull();
    });

    await logOut();
    await submitPassword();
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await expect(screen.findByRole("region", { name: offerName }, { timeout: 300 })).rejects.toThrow(
      /Add a passkey for next time/,
    );
  });

  it("passkey offer: adds a passkey when the offer is accepted", async () => {
    stubPassword(true);
    renderAt("/login");
    await submitPassword();
    const offer = await screen.findByRole("region", { name: "Add a passkey for next time" });
    act(() => {
      within(offer).getByRole("button", { name: "Add passkey" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("region", { name: "Add a passkey for next time" })).toBeNull();
    });

    act(() => {
      screen.getByRole("button", { name: /Sam Rivera/i }).click();
    });
    act(() => {
      screen.getByRole("menuitem", { name: "Security" }).click();
    });
    expect(await screen.findByText("This device")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Add a passkey for next time" })).toBeNull();
  });

  it("keeps the autofill request alive after a wrong password", async () => {
    const browser = stubPasskeyBrowser();
    renderAt("/login");
    await waitFor(() => {
      expect(browser.gets).toHaveLength(1);
    });
    expect(browser.optionsRequests()).toBe(1);

    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "wrong password" } });
    act(() => {
      screen.getByRole("button", { name: /^Log in$/ }).click();
    });
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Email or password is wrong.");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(browser.optionsRequests()).toBe(1);
    expect(browser.gets).toHaveLength(1);
    expect(browser.gets[0]?.signal?.aborted).toBe(false);
  });

  it("asks for the autofill again after a wrong password once the first request has ended", async () => {
    const browser = stubPasskeyBrowser({ firstGetEnds: true });
    renderAt("/login");
    await waitFor(() => {
      expect(browser.gets).toHaveLength(1);
    });
    await waitFor(() => {
      expect(browser.optionsRequests()).toBe(1);
    });
    await new Promise((resolve) => setTimeout(resolve, 50));

    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "wrong password" } });
    act(() => {
      screen.getByRole("button", { name: /^Log in$/ }).click();
    });
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Email or password is wrong.");
    await waitFor(() => {
      expect(browser.gets).toHaveLength(2);
    });
    expect(browser.optionsRequests()).toBe(2);
  });

  it("ends the autofill request before the passkey button asks for its own and finishes on that one", async () => {
    const browser = stubPasskeyBrowser();
    renderAt("/login");
    await waitFor(() => {
      expect(browser.gets).toHaveLength(1);
    });

    act(() => {
      screen.getByRole("button", { name: "Log in with a passkey" }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    expect(browser.events).toEqual(["options 1", "get conditional", "abort", "options 2", "get modal", "finish 2"]);
  });

  it("says how long to wait when the server answers 429 with Retry-After", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<() => Promise<Response>>(
        async () => new Response("{}", { status: 429, headers: { "retry-after": "12" } }),
      ),
    );
    renderAt("/login");
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
    act(() => {
      screen.getByRole("button", { name: /^Log in$/ }).click();
    });
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Try again in 12 seconds.");
  });

  it("keeps the plain message when a 429 names no wait", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<() => Promise<Response>>(async () => new Response("{}", { status: 429 })),
    );
    renderAt("/login");
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
    act(() => {
      screen.getByRole("button", { name: /^Log in$/ }).click();
    });
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Try again.");
  });
});
