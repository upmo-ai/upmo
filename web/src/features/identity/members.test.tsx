import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi } from "../../app/api";
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
});
