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
    renderAt("/tokens");
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

describe("live tokens", () => {
  const original = globalThis.fetch;

  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    globalThis.fetch = original;
    cleanup();
  });

  it("lists a token and creates one inside the role", async () => {
    let posted = "";
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/api/tokens") && method === "POST") {
        posted = String(init?.body ?? "");
        return Response.json({ id: "tok", secret: "upmo_pat_secret" }, { status: 201 });
      }
      if (url.endsWith("/api/tokens")) {
        return Response.json([
          {
            id: "tok-1",
            name: "Ship",
            scope: ["task:view"],
            created_at: "2026-10-01T00:00:00Z",
            expires_at: "2026-12-30T00:00:00Z",
            last_used_at: null,
          },
        ]);
      }
      if (url.endsWith("/api/session")) return Response.json({ user_id: "ada" });
      if (url.endsWith("/api/members")) {
        return Response.json([{ id: "ada", role: { id: "role-owner" } }]);
      }
      if (url.endsWith("/api/roles")) {
        return Response.json([
          { id: "role-owner", permissions: ["task:view", "task:delete", "members:manage"] },
        ]);
      }
      return new Response("missing", { status: 404 });
    }) as typeof fetch;

    renderAt("/tokens");
    expect(await screen.findByText("Ship")).toBeTruthy();
    expect(screen.getByText("task:view")).toBeTruthy();
    expect(screen.queryByTestId("token-hint")).toBeNull();
    act(() => {
      screen.getByRole("button", { name: "Create token" }).click();
    });
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("group", { name: "Task" })).toBeTruthy();
    expect(within(dialog).getByRole("checkbox", { name: "Task view" })).toBeTruthy();
    expect(within(dialog).queryByRole("checkbox", { name: "Task delete" })).toBeNull();
    expect(within(dialog).queryByRole("group", { name: "Chat" })).toBeNull();
    expect(within(dialog).getByText(Intl.DateTimeFormat().resolvedOptions().timeZone)).toBeTruthy();
    fireEvent.change(within(dialog).getByLabelText("Expiry"), { target: { value: "15 Jan 2027" } });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Nightly" } });
    act(() => {
      within(dialog).getByRole("checkbox", { name: "Task view" }).click();
    });
    act(() => {
      within(dialog).getByRole("button", { name: "Create token" }).click();
    });
    expect(await within(dialog).findByTestId("token-secret")).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Copy" })).toBeTruthy();
    expect(posted).toContain("Nightly");
    expect(posted).toContain("2027-01-15");
    expect(posted).toContain("task:view");
    expect(posted).not.toContain("task:delete");
  });
});
