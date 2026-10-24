import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
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

function selectOptions(name: string) {
  const listbox = screen.getByRole("listbox", { name, hidden: true });
  return within(listbox)
    .getAllByRole("option", { hidden: true })
    .map((option) => option.textContent);
}

function sidebar() {
  return screen.getByRole("navigation", { name: "Main" });
}

describe("sharing", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("does not remove the owner or grant an owner level", async () => {
    renderAt("/tasks/aster-digest");
    expect(await screen.findByRole("heading", { level: 1, name: "Daily finance digest" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Share" }).click();
    });
    const dialog = await screen.findByRole("dialog");
    const people = await within(dialog).findByRole("list", { name: "People with access" });
    const owner = within(people).getByText("Sam Rivera").closest("li");
    if (!owner) throw new Error("owner row missing");
    expect(within(owner).getByText("Owner")).toBeTruthy();
    expect(within(owner).queryByRole("button", { name: /Remove access/ })).toBeNull();

    expect(selectOptions("Access for Lee Chen")).toEqual(["Editor", "Viewer"]);
    expect(selectOptions("Level")).toEqual(["Editor", "Viewer"]);
  });

  it("opens read-only when the signed-in person cannot edit sharing", async () => {
    renderAt("/admin");
    expect(await screen.findByRole("button", { name: "Invite" })).toBeTruthy();
    act(() => {
      within(screen.getByRole("group", { name: "View as" })).getByRole("button", { name: "Viewer" }).click();
    });
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "Invite" })).toBeNull();
    });
    act(() => {
      within(sidebar()).getByRole("link", { name: "Tasks" }).click();
    });
    const digest = await screen.findByRole("link", { name: /Daily finance digest/ });
    act(() => {
      digest.click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Daily finance digest" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Share" }).click();
    });
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Only me")).toBeTruthy();
    expect(within(dialog).queryByRole("button", { name: "Add person" })).toBeNull();
    expect(within(dialog).queryByRole("radio", { name: "Everyone in the workspace" })).toBeNull();
    expect(within(dialog).queryByRole("button", { name: /Remove access/ })).toBeNull();
  });

  it("refuses an owner share level and removal of the owner", async () => {
    const api = createStubApi();
    await expect(
      api.shares.addPerson("aster-digest", "noor", "owner" as "viewer"),
    ).rejects.toThrow("Owner is not a share level.");
    await expect(api.shares.removePerson("aster-digest", "sam")).rejects.toThrow(
      "The owner cannot be removed.",
    );
    const share = await api.shares.get("aster-digest");
    share.people[0]!.level = "viewer";
    const stored = await api.shares.get("aster-digest");
    expect(stored.people.find((person) => person.memberId === "sam")?.level).toBe("owner");
  });
});
