import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient } from "../../app/api";
import { AppProviders } from "../../theme";
import { lastOwnerReason } from "./rules";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

function viewAs(name: "Admin" | "Member" | "Viewer") {
  act(() => {
    within(screen.getByRole("group", { name: "View as" })).getByRole("button", { name }).click();
  });
}

describe("members", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("hides invite, role, and remove controls for a member and a viewer", async () => {
    renderAt("/settings/members");
    expect(await screen.findByRole("heading", { level: 1, name: "Members" })).toBeTruthy();
    expect(await screen.findByRole("button", { name: "Invite" })).toBeTruthy();
    expect(screen.getByText("ada@aster.example")).toBeTruthy();
    expect(screen.getAllByText("Finance").length).toBeGreaterThan(0);

    viewAs("Member");
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Invite" })).toBeNull();
    });
    expect(screen.queryByLabelText(/Role for /)).toBeNull();
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
    expect(screen.getByText("Ada Okonkwo")).toBeTruthy();

    viewAs("Viewer");
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Invite" })).toBeNull();
    });
    expect(screen.queryByLabelText(/Role for /)).toBeNull();
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();

    viewAs("Admin");
    expect(await screen.findByRole("button", { name: "Invite" })).toBeTruthy();
  });

  it("keeps the last owner in place and does not offer Owner on invite", async () => {
    renderAt("/settings/members");
    const ownerRole = (await screen.findByLabelText("Role for Ada Okonkwo")) as HTMLSelectElement;
    expect(ownerRole.disabled).toBe(true);
    expect(screen.getByText(lastOwnerReason)).toBeTruthy();
    const removeOwner = screen.getByRole("button", { name: "Remove Ada Okonkwo" }) as HTMLButtonElement;
    expect(removeOwner.disabled).toBe(true);

    const inviteRole = screen.getByLabelText("Invite role");
    const options = within(inviteRole).getAllByRole("option").map((option) => option.textContent);
    expect(options).toEqual(["Admin", "Member", "Viewer"]);
    expect(within(screen.getByRole("group", { name: "View as" })).queryByRole("button", { name: "Owner" })).toBeNull();
  });

  it("does not demote the owner or invite a second one", async () => {
    const api = createStubApi();
    await expect(api.members.setRole("ada", "member")).rejects.toThrow(lastOwnerReason);
    await expect(api.members.remove("ada")).rejects.toThrow(lastOwnerReason);
    await expect(
      api.members.invite({ email: "owner@aster.example", role: "owner" as "admin" }),
    ).rejects.toThrow("Owner is not a choosable role.");
    await api.me.viewAs("member");
    const members = await api.members.list();
    expect(members.find((member) => member.id === "ada")?.role).toBe("owner");
    expect(members.filter((member) => member.role === "owner")).toHaveLength(1);
    members[0]!.name = "Mutated";
    expect((await api.members.list())[0]?.name).toBe("Ada Okonkwo");
  });

  it("shows a message in the shell when a role change is refused and allows closing it", async () => {
    const base = createStubApi();
    const client: ApiClient = {
      ...base,
      members: {
        ...base.members,
        async setRole() {
          throw new Error("Role change refused.");
        },
      },
    };
    const history = createMemoryHistory({ initialEntries: ["/settings/members"] });
    render(
      <AppProviders>
        <App history={history} client={client} />
      </AppProviders>,
    );

    const roleSelect = (await screen.findByLabelText("Role for Lee Chen")) as HTMLSelectElement;
    act(() => {
      fireEvent.change(roleSelect, { target: { value: "admin" } });
    });

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Role change refused.");

    const closeBtn = within(alert).getByRole("button", { name: "Close message" });
    act(() => {
      closeBtn.click();
    });
    await waitFor(() => {
      expect(screen.queryByText("Role change refused.")).toBeNull();
    });
  });
});

describe("live members", () => {
  const original = globalThis.fetch;

  afterEach(() => {
    globalThis.fetch = original;
    cleanup();
  });

  it("invites with a copy link and offers roles the person can manage", async () => {
    let copied = "";
    Object.assign(navigator, {
      clipboard: {
        writeText: async (value: string) => {
          copied = value;
        },
      },
    });
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url.endsWith("/api/teams")) return Response.json([{ id: "team-finance", name: "Finance" }]);
      if (url.endsWith("/api/roles")) {
        return Response.json([
          {
            id: "role-owner",
            name: "Owner",
            system: "owner",
            permissions: ["roles:manage", "members:manage", "teams:manage", "owners:manage"],
            members: 1,
          },
          { id: "role-member", name: "Member", system: "member", permissions: ["task:view"], members: 1 },
        ]);
      }
      if (url.endsWith("/api/session")) {
        return Response.json({ user_id: "ada", has_password: true, sign_in_method: "passkey" });
      }
      if (url.endsWith("/api/passkeys")) return Response.json([{ id: "passkey" }]);
      if (url.endsWith("/api/members") && method === "POST") {
        return Response.json({
          id: "new",
          name: "Ned",
          email: "ned@example.com",
          invited: true,
          teams: [],
          role: { id: "role-member", name: "Member", system: "member" },
          token: "invite-token",
        });
      }
      if (url.endsWith("/api/members")) {
        return Response.json([
          {
            id: "ada",
            name: "Ada",
            email: "ada@example.com",
            invited: false,
            teams: [],
            role: { id: "role-owner", name: "Owner", system: "owner" },
          },
          {
            id: "bob",
            name: "Bob",
            email: "bob@example.com",
            invited: true,
            teams: [],
            role: { id: "role-member", name: "Member", system: "member" },
          },
        ]);
      }
      return new Response("missing", { status: 404 });
    }) as typeof fetch;

    renderAt("/settings/members");
    expect(await screen.findByLabelText("Name")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "View as" })).toBeNull();
    expect(screen.queryByLabelText("Role for Ada")).toBeNull();
    expect(screen.getByLabelText("Role for Bob")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Roles" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "New role" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "New team" })).toBeTruthy();
    expect(screen.getByLabelText("Finance for Ada")).toBeTruthy();
    expect(screen.queryByLabelText("Finance for Bob")).toBeNull();
    expect(screen.getByRole("button", { name: "Duplicate Owner" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Delete Owner" })).toBeNull();
    expect(screen.getByRole("button", { name: "New invite link" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Remove Ada" })).toBeNull();

    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "ned@example.com" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Ned" } });
    fireEvent.change(screen.getByLabelText("Invite role"), { target: { value: "role-member" } });
    fireEvent.click(screen.getByRole("button", { name: "Invite" }));

    const link = await screen.findByLabelText("Invite link");
    expect((link as HTMLInputElement).value).toBe(`${window.location.origin}/login/link#invite-token`);
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await waitFor(() => {
      expect(copied).toBe(`${window.location.origin}/login/link#invite-token`);
    });
  });
});
