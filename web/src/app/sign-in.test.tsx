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
    renderAt("/sign-in");
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery" } });
    act(() => {
      screen.getByRole("button", { name: "Log in", exact: true }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    expect(await screen.findByRole("region", { name: "Add a passkey for next time" })).toBeTruthy();
    const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body)) as { email: string; password: string };
    expect(body).toEqual({ email: "ada@example.com", password: "correct horse battery" });
  });

  it("stays on the page when the password is wrong", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<() => Promise<Response>>(async () => new Response("{}", { status: 401 })),
    );
    renderAt("/sign-in");
    fireEvent.change(await screen.findByLabelText("Email"), { target: { value: "ada@example.com" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "not the password" } });
    act(() => {
      screen.getByRole("button", { name: "Log in", exact: true }).click();
    });
    expect(await screen.findByRole("status")).toHaveProperty("textContent", "Email or password is wrong.");
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();
  });

  it("stays outside the shell and a password enters the app", async () => {
    renderAt("/sign-in");
    expect(await screen.findByRole("img", { name: "Upmo" })).toBeTruthy();
    expect(screen.getByLabelText("Email")).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();

    act(() => {
      screen.getByRole("button", { name: "Log in with a passkey" }).click();
    });
    expect(await screen.findByRole("status")).toHaveProperty(
      "textContent",
      "The passkey prompt was cancelled. Try again.",
    );
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();

    act(() => {
      screen.getByRole("button", { name: "Use a password instead" }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    expect(screen.getByTestId("theme-switch")).toBeTruthy();
    expect(screen.getByText("0.1.0")).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Log out" }).click();
    });
    expect(await screen.findByRole("button", { name: "Use a password instead" })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();

    act(() => {
      screen.getByRole("button", { name: "Use a password instead" }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
  });

  it("offers a passkey only after a password sign-in, and only once", async () => {
    renderAt("/sign-in");
    expect(await screen.findByRole("button", { name: "Log in with a passkey" })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Add a passkey for next time" })).toBeNull();
    act(() => {
      screen.getByRole("button", { name: "Use a password instead" }).click();
    });
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
    expect(await screen.findByRole("button", { name: "Use a password instead" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Use a password instead" }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await expect(
      screen.findByRole("region", { name: "Add a passkey for next time" }, { timeout: 300 }),
    ).rejects.toThrow(/Add a passkey for next time/);
  });

  it("adds a passkey when the offer is accepted", async () => {
    renderAt("/sign-in");
    expect(await screen.findByRole("button", { name: "Use a password instead" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Use a password instead" }).click();
    });
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
});
