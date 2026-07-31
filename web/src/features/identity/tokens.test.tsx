import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi } from "../../app/api";
import { AppProviders } from "../../theme";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

describe("API tokens", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("shows the secret only while the create dialog is open", async () => {
    renderAt("/settings/tokens");
    expect(await screen.findByRole("heading", { level: 1, name: "API tokens" })).toBeTruthy();
    expect(await screen.findByRole("button", { name: "Create token" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Create token" }).click();
    });
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Ship token" } });
    act(() => {
      within(dialog).getByRole("checkbox", { name: "Task view" }).click();
    });
    act(() => {
      within(dialog).getByRole("button", { name: "Create token" }).click();
    });

    const secretNode = await within(dialog).findByTestId("token-secret");
    const secret = secretNode.textContent ?? "";
    expect(secret.startsWith("upmo_pat_")).toBe(true);
    expect(within(dialog).getByText("You will not see this token again")).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Copy" })).toBeTruthy();

    act(() => {
      within(dialog).getByRole("button", { name: "Close" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    expect(document.body.textContent?.includes(secret)).toBe(false);
    expect(JSON.stringify(localStorage).includes(secret)).toBe(false);
    const hint = screen.getByTestId("token-hint").textContent ?? "";
    expect(hint.startsWith("upmo_pat_")).toBe(true);
    expect(secret.startsWith(hint)).toBe(true);
    expect(hint).not.toBe(secret);
    expect(screen.getByText("Ship token")).toBeTruthy();
    expect(screen.getByText("task:view")).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Revoke Ship token" }).click();
    });
    const revoke = await screen.findByRole("dialog");
    expect(within(revoke).getByText("Revoke the Ship token?")).toBeTruthy();
  });

  it("keeps the secret out of the list returned by the stub", async () => {
    const api = createStubApi();
    const created = await api.tokens.create({
      name: "Ship",
      scopes: ["task:view"],
      days: 30,
    });
    expect(created.secret.startsWith("upmo_pat_")).toBe(true);
    expect(JSON.stringify(created.token).includes(created.secret)).toBe(false);
    const listed = await api.tokens.list();
    expect(JSON.stringify(listed).includes(created.secret)).toBe(false);
    expect(listed[0]?.hint.startsWith("upmo_pat_")).toBe(true);
    created.token.name = "Mutated";
    expect((await api.tokens.list())[0]?.name).toBe("Ship");

    await api.me.viewAs("viewer");
    await expect(
      api.tokens.create({ name: "Nope", scopes: ["task:edit"], days: 90 }),
    ).rejects.toThrow("That scope is not allowed.");
    const viewOnly = await api.tokens.create({
      name: "Read",
      scopes: ["folder:view"],
      days: 365,
    });
    expect(viewOnly.secret.startsWith("upmo_pat_")).toBe(true);
    await expect(
      api.tokens.create({ name: "Empty", scopes: [], days: 30 }),
    ).rejects.toThrow("Choose at least one scope.");
  });
});
