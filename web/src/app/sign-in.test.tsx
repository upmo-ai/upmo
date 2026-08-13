import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../App";
import { AppProviders } from "../theme";
import * as passkeys from "./passkeys";

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
      return new Response("", { status: 404 });
    }),
  );
}

async function submitPassword() {
  fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
  act(() => {
    screen.getByRole("button", { name: /^Log in$/ }).click();
  });
}

describe("sign in", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("logs in through the password form when the server accepts it", async () => {
    const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(
      async (input) => {
        if (String(input).includes("/api/sign-in/password")) {
          return new Response(JSON.stringify({ offer_passkey: true }), {
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
    expect(await screen.findByRole("status")).toHaveProperty("textContent", "Email or password is wrong.");
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();
  });

  it("puts Log in under the password and keeps the passkey as the other choice", async () => {
    renderAt("/login");
    expect((await screen.findByRole("link", { name: "Upmo" })).getAttribute("href")).toBe("/login");
    const password = screen.getByLabelText("Password");
    const logIn = screen.getByRole("button", { name: /^Log in$/ });
    const passkey = screen.getByRole("button", { name: "Log in with a passkey" });
    expect(passkey.querySelector("svg")).toBeTruthy();
    expect(password.compareDocumentPosition(logIn) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(logIn.compareDocumentPosition(passkey) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Use a password instead" })).toBeNull();
    expect(screen.getByRole("link", { name: "Terms of use" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Privacy policy" })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();

    act(() => {
      passkey.click();
    });
    expect(await screen.findByRole("status")).toHaveProperty(
      "textContent",
      "The passkey prompt was cancelled. Try again.",
    );
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();

    stubPassword(true);
    await submitPassword();
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Upmo" }).getAttribute("href")).toBe("/");
    });
    expect(screen.getByTestId("theme-switch")).toBeTruthy();
    expect(screen.getByText("0.1.0")).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Log out" }).click();
    });
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();

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
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();
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

  it("offers a passkey only after a password sign-in, and only once", async () => {
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
      screen.getByRole("button", { name: "Log out" }).click();
    });
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    await submitPassword();
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await expect(
      screen.findByRole("region", { name: "Add a passkey for next time" }, { timeout: 300 }),
    ).rejects.toThrow(/Add a passkey for next time/);
  });

  it("adds a passkey when the offer is accepted", async () => {
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
      within(screen.getByRole("navigation", { name: "Sidebar" }))
        .getByRole("link", { name: "Settings" })
        .click();
    });
    expect(await screen.findByText("This device")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Add a passkey for next time" })).toBeNull();
  });

  it("autofill re-arms; a test fails if the re-arm is removed", async () => {
    const conditionalSpy = vi.spyOn(passkeys, "conditionalPasskey").mockResolvedValue(false);

    vi.stubGlobal(
      "fetch",
      vi.fn<() => Promise<Response>>(async () => new Response("{}", { status: 401 })),
    );

    renderAt("/login");
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    expect(conditionalSpy).toHaveBeenCalledTimes(1);

    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "wrong password" } });
    act(() => {
      screen.getByRole("button", { name: /^Log in$/ }).click();
    });
    expect(await screen.findByRole("status")).toHaveProperty("textContent", "Email or password is wrong.");
    expect(conditionalSpy).toHaveBeenCalledTimes(2);

    const passkeyBtn = screen.getByRole("button", { name: "Log in with a passkey" });
    act(() => {
      passkeyBtn.click();
    });
    expect(await screen.findByRole("status")).toHaveProperty(
      "textContent",
      "The passkey prompt was cancelled. Try again.",
    );
    expect(conditionalSpy).toHaveBeenCalledTimes(3);
  });
});
