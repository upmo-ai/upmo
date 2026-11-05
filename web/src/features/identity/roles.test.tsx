import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

const roles = [
  { id: "role-people", name: "People lead", system: "", permissions: ["roles:manage", "task:view"], members: 1 },
  { id: "role-member", name: "Member", system: "member", permissions: ["task:view", "task:create"], members: 1 },
];

// The live roles are answered last, after the session and the members, so the
// live role names only reach the table once the actor's own role is resolvable.
function live(userId: string) {
  let release: () => void = () => {};
  const actorServed = new Promise<void>((resolve) => {
    release = resolve;
  });
  const served = new Set<string>();
  const fetcher = (async (input: RequestInfo | URL) => {
    const url = String(input);
    const path = url.slice(url.indexOf("/api/"));
    served.add(path);
    if (served.has("/api/session") && served.has("/api/members")) release();
    if (path === "/api/roles") {
      await actorServed;
      return Response.json(roles);
    }
    if (path === "/api/session") return Response.json({ user_id: userId, has_password: true, sign_in_method: "passkey" });
    if (path === "/api/passkeys") return Response.json([{ id: "passkey" }]);
    if (path === "/api/members") {
      return Response.json([
        {
          id: "cy",
          name: "Cy",
          email: "cy@example.com",
          invited: false,
          teams: [],
          role: { id: "role-people", name: "People lead", system: "" },
        },
        {
          id: "bob",
          name: "Bob",
          email: "bob@example.com",
          invited: false,
          teams: [],
          role: { id: "role-member", name: "Member", system: "member" },
        },
      ]);
    }
    return new Response("missing", { status: 404 });
  }) as typeof fetch;
  return { fetcher };
}

async function settled() {
  const table = await screen.findByRole("table", { name: "Roles" });
  await within(table).findByText("People lead");
}

describe("roles page", () => {
  const original = globalThis.fetch;

  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    globalThis.fetch = original;
    cleanup();
  });

  it("offers role management to a person whose own role holds roles:manage", async () => {
    const { fetcher } = live("cy");
    globalThis.fetch = fetcher;
    renderAt("/admin/roles");
    await settled();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "New role" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Duplicate People lead" })).toBeTruthy();
    });
  });

  it("locks the permissions the person's own role does not hold", async () => {
    const { fetcher } = live("cy");
    globalThis.fetch = fetcher;
    renderAt("/admin/roles");
    await settled();

    fireEvent.click(await screen.findByRole("button", { name: "New role" }));

    await screen.findByLabelText("task:view");
    await waitFor(() => {
      expect((screen.getByLabelText("task:view") as HTMLInputElement).disabled).toBe(false);
      expect((screen.getByLabelText("task:create") as HTMLInputElement).disabled).toBe(true);
      expect((screen.getByLabelText("members:manage") as HTMLInputElement).disabled).toBe(true);
    });
  });

  it("hides role management from a person whose own role lacks roles:manage", async () => {
    const { fetcher } = live("bob");
    globalThis.fetch = fetcher;
    renderAt("/admin/roles");
    await settled();

    expect(screen.getByRole("table", { name: "Roles" })).toBeTruthy();
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "New role" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Duplicate People lead" })).toBeNull();
    });
  });
});
