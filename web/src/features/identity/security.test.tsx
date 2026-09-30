import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient } from "../../app/api";
import { AppProviders } from "../../theme";
import { lastPasskeyReason, lastPasswordReason } from "./rules";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

describe("security", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("refuses to remove the last passkey or the last password", async () => {
    renderAt("/settings");
    expect(await screen.findByRole("heading", { level: 1, name: "Security" })).toBeTruthy();
    expect(await screen.findByText("Laptop")).toBeTruthy();
    expect(screen.getByText(/Added/)).toBeTruthy();
    expect(screen.getByText(/Last used/)).toBeTruthy();

    const passkeyItem = screen.getByText("Laptop").closest("li")!;
    const remove = within(passkeyItem).getByRole("button", { name: "Remove" }) as HTMLButtonElement;
    expect(remove.disabled).toBe(true);
    expect(screen.getByText(lastPasskeyReason)).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Set password" }).click();
    });
    const setDialog = await screen.findByRole("dialog");
    fireEvent.change(within(setDialog).getByLabelText("New password"), {
      target: { value: "correct horse battery" },
    });
    act(() => {
      within(setDialog).getByRole("button", { name: "Set password" }).click();
    });
    await waitFor(() => {
      expect((within(passkeyItem).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(
        false,
      );
    });
    expect(screen.queryByText(lastPasskeyReason)).toBeNull();

    act(() => {
      within(passkeyItem).getByRole("button", { name: "Remove" }).click();
    });
    const removeDialog = await screen.findByRole("dialog");
    expect(within(removeDialog).getByText("Remove the Laptop passkey?")).toBeTruthy();
    act(() => {
      within(removeDialog).getByRole("button", { name: "Remove passkey" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByText("Laptop")).toBeNull();
    });

    const removePassword = screen.getByRole("button", { name: "Remove password" }) as HTMLButtonElement;
    expect(removePassword.disabled).toBe(true);
    expect(screen.getByText(lastPasswordReason)).toBeTruthy();
  });

  it("adds a passkey after the confirm step and renames it", async () => {
    renderAt("/settings");
    expect(await screen.findByText("Laptop")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Add passkey" }).click();
    });
    expect(screen.getByText("Confirm this passkey to finish adding it.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Passkey name"), { target: { value: "Phone" } });
    act(() => {
      screen.getByRole("button", { name: "Confirm passkey" }).click();
    });
    expect(await screen.findByText("Phone")).toBeTruthy();

    const phoneItem = (await screen.findByText("Phone")).closest("li")!;
    act(() => {
      within(phoneItem).getByRole("button", { name: "Edit" }).click();
    });
    fireEvent.change(screen.getByLabelText("New name for Phone"), { target: { value: "Pocket" } });
    act(() => {
      screen.getByRole("button", { name: "Save name" }).click();
    });
    expect(await screen.findByText("Pocket")).toBeTruthy();
  });

  it("returns passkey copies and keeps the last way to sign in", async () => {
    const api = createStubApi();
    const listed = await api.passkeys.list();
    listed[0]!.name = "Mutated";
    expect((await api.passkeys.list())[0]?.name).toBe("Laptop");
    await expect(api.passkeys.remove("pk-laptop")).rejects.toThrow(lastPasskeyReason);
    await api.password.set();
    await api.passkeys.remove("pk-laptop");
    await expect(api.password.remove()).rejects.toThrow(lastPasswordReason);
  });

  it("a passkey rename refetches passkeys only", async () => {
    let passkeyListCalls = 0;
    let sessionCalls = 0;

    const base = createStubApi();
    const client: ApiClient = {
      ...base,
      session: {
        async get() {
          sessionCalls += 1;
          return base.session.get();
        },
      },
      me: {
        ...base.me,
        async current() {
          sessionCalls += 1;
          return base.me.current();
        },
      },
      passkeys: {
        ...base.passkeys,
        async list() {
          passkeyListCalls += 1;
          return base.passkeys.list();
        },
      },
    };

    const history = createMemoryHistory({ initialEntries: ["/settings"] });
    render(
      <AppProviders>
        <App history={history} client={client} />
      </AppProviders>,
    );

    expect(await screen.findByText("Laptop")).toBeTruthy();
    const initialPasskeyListCalls = passkeyListCalls;
    const initialSessionCalls = sessionCalls;

    act(() => {
      screen.getByRole("button", { name: "Edit" }).click();
    });
    fireEvent.change(screen.getByLabelText("New name for Laptop"), { target: { value: "Desktop" } });
    act(() => {
      screen.getByRole("button", { name: "Save name" }).click();
    });

    expect(await screen.findByText("Desktop")).toBeTruthy();
    expect(passkeyListCalls).toBe(initialPasskeyListCalls + 1);
    expect(sessionCalls).toBe(initialSessionCalls);
  });
});
