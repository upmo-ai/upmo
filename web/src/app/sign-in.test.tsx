import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

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
  });

  it("stays outside the shell and both actions enter the app", async () => {
    renderAt("/sign-in");
    expect(await screen.findByRole("img", { name: "Upmo" })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();

    act(() => {
      screen.getByRole("button", { name: "Sign in with a passkey" }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    expect(screen.getByTestId("theme-switch")).toBeTruthy();
    expect(screen.getByText("0.1.0")).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Sign out" }).click();
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
    expect(await screen.findByRole("button", { name: "Sign in with a passkey" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Sign in with a passkey" }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await expect(
      screen.findByRole("region", { name: "Add a passkey for next time" }, { timeout: 300 }),
    ).rejects.toThrow(/Add a passkey for next time/);

    act(() => {
      screen.getByRole("button", { name: "Sign out" }).click();
    });
    expect(await screen.findByRole("button", { name: "Use a password instead" })).toBeTruthy();
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
      screen.getByRole("button", { name: "Sign out" }).click();
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
