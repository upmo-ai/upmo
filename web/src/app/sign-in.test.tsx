import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen } from "@testing-library/react";
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
});
