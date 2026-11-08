import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import {
  BrainError,
  createStubApi,
  type ApiClient,
  type ShareUpdate,
  type TaskShare,
} from "../../app/api";
import { AppProviders } from "../../theme";
import { shareRole } from "./rules";

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
  return history;
}

async function openShare(path: string, title: string, client?: ApiClient) {
  const history = renderAt(path, client);
  expect(await screen.findByRole("heading", { level: 1, name: title })).toBeTruthy();
  act(() => {
    screen.getByRole("button", { name: "Share" }).click();
  });
  const dialog = await screen.findByRole("dialog", { name: "Share" });
  return { history, dialog };
}

function selectOptions(name: string) {
  const listbox = screen.getByRole("listbox", { name, hidden: true });
  return within(listbox)
    .getAllByRole("option", { hidden: true })
    .map((option) => option.textContent);
}

function choose(dialog: HTMLElement, label: string, option: string) {
  act(() => {
    within(dialog).getByRole("combobox", { name: label }).click();
  });
  const listbox = screen.getByRole("listbox", { name: label, hidden: true });
  const target = within(listbox)
    .getAllByRole("option", { hidden: true })
    .find((item) => item.textContent === option);
  if (!target) throw new Error(`no option ${option} in ${label}`);
  act(() => {
    target.click();
  });
}

function accessRow(dialog: HTMLElement, name: string) {
  const list = within(dialog).getByRole("list", { name: "Who has access" });
  const row = within(list).getByText(name, { exact: true }).closest("li");
  if (!row) throw new Error(`no access row for ${name}`);
  return row;
}

/** A client whose sharing writes or reads fail the way the backend would. */
function failing(overrides: Partial<ApiClient["shares"]>): ApiClient {
  const client = createStubApi();
  return { ...client, shares: { ...client.shares, ...overrides } };
}

/** A client where the signed-in person (Lee) holds the given role on every task. */
function holdingRole(role: "editor" | "viewer"): ApiClient {
  const client = createStubApi();
  const lee = async () => ({ ...(await client.me.current()), memberId: "lee" });
  return {
    ...client,
    session: { get: lee },
    me: { ...client.me, current: lee },
    shares: {
      ...client.shares,
      async get(taskId) {
        const stored = await client.shares.get(taskId);
        return {
          ...stored,
          members: stored.members.map((member) =>
            member.subject_id === "lee" ? { ...member, role } : member,
          ),
        };
      },
    },
  };
}

describe("sharing", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-205: the owner sees the visibility and each person's and team's role", async () => {
    const { dialog } = await openShare("/tasks/aster-mail", "Mail triage");
    const list = await within(dialog).findByRole("list", { name: "Who has access" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);

    const owner = accessRow(dialog, "Sam Rivera");
    expect(within(owner).getByText("Owner")).toBeTruthy();
    expect(within(owner).queryByRole("button")).toBeNull();

    const team = accessRow(dialog, "Finance");
    expect(within(team).getByText("Team", { selector: "p" })).toBeTruthy();
    expect(within(team).getByRole("combobox", { name: "Access for Finance" })).toHaveProperty("value", "Editor");

    const visibility = within(dialog).getByRole("radiogroup", { name: "Who can open this task" });
    expect(within(visibility).getByRole("radio", { name: "Team" })).toHaveProperty("checked", true);
    expect(within(visibility).getByRole("radio", { name: "Private" })).toHaveProperty("checked", false);
  });

  it("UPM-205: an owner's changes are written through the client and kept", async () => {
    const client = createStubApi();
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", client);
    await within(dialog).findByRole("list", { name: "Who has access" });

    act(() => {
      within(dialog).getByRole("radio", { name: "Everyone" }).click();
    });
    await waitFor(async () => {
      expect((await client.shares.get("aster-digest")).visibility).toBe("everyone");
    });
    await waitFor(() => {
      expect(within(dialog).getByRole("radio", { name: "Everyone" })).toHaveProperty("checked", true);
    });

    choose(dialog, "Person", "Noor Ali (noor@aster.example)");
    choose(dialog, "Level", "Editor");
    act(() => {
      within(dialog).getByRole("button", { name: "Add person" }).click();
    });
    await waitFor(() => {
      expect(accessRow(dialog, "Noor Ali")).toBeTruthy();
    });
    choose(dialog, "Team", "Ops");
    act(() => {
      within(dialog).getByRole("button", { name: "Add team" }).click();
    });
    await waitFor(() => {
      expect(accessRow(dialog, "Ops")).toBeTruthy();
    });

    const stored = await client.shares.get("aster-digest");
    expect(stored.members).toEqual([
      { subject_type: "person", subject_id: "sam", name: "Sam Rivera", role: "owner" },
      { subject_type: "person", subject_id: "lee", name: "Lee Chen", role: "viewer" },
      { subject_type: "person", subject_id: "noor", name: "Noor Ali", role: "editor" },
      { subject_type: "team", subject_id: "ops", name: "Ops", role: "editor" },
    ]);

    choose(dialog, "Access for Lee Chen", "Editor");
    await waitFor(async () => {
      const next = await client.shares.get("aster-digest");
      expect(next.members.find((member) => member.subject_id === "lee")?.role).toBe("editor");
    });

    act(() => {
      within(accessRow(dialog, "Noor Ali")).getByRole("button", { name: "Remove access for Noor Ali" }).click();
    });
    const confirm = await screen.findByRole("dialog", { name: "Remove Noor Ali's access?" });
    act(() => {
      within(confirm).getByRole("button", { name: "Remove access" }).click();
    });
    await waitFor(async () => {
      const next = await client.shares.get("aster-digest");
      expect(next.members.some((member) => member.subject_id === "noor")).toBe(false);
    });
  });

  it("UPM-205: the owner row cannot be removed and no owner level is offered", async () => {
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest");
    await within(dialog).findByRole("list", { name: "Who has access" });
    const owner = accessRow(dialog, "Sam Rivera");
    expect(within(owner).getByText("Owner")).toBeTruthy();
    expect(within(owner).queryByRole("button", { name: /Remove access/ })).toBeNull();

    expect(selectOptions("Access for Lee Chen")).toEqual(["Editor", "Viewer"]);
    expect(selectOptions("Level")).toEqual(["Editor", "Viewer"]);
  });

  it("UPM-205: a viewer sees sharing read-only", async () => {
    const client = createStubApi();
    await client.me.viewAs("member");
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", client);
    const list = await within(dialog).findByRole("list", { name: "Who has access" });

    expect(within(dialog).getByText(/Only the owner can change who has access/)).toBeTruthy();
    expect(within(dialog).getByText("Private")).toBeTruthy();
    expect(within(accessRow(dialog, "Sam Rivera")).getByText("Owner")).toBeTruthy();
    expect(within(accessRow(dialog, "Lee Chen")).getByText("Viewer")).toBeTruthy();

    expect(within(dialog).queryByRole("radio")).toBeNull();
    expect(within(dialog).queryByRole("combobox")).toBeNull();
    expect(within(list).queryByRole("button")).toBeNull();
    expect(within(dialog).queryByRole("button", { name: /^Add / })).toBeNull();
  });

  it("UPM-205: an editor cannot change sharing either", async () => {
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", holdingRole("editor"));
    const list = await within(dialog).findByRole("list", { name: "Who has access" });

    expect(within(accessRow(dialog, "Lee Chen")).getByText("Editor")).toBeTruthy();
    expect(within(dialog).queryByRole("radio")).toBeNull();
    expect(within(dialog).queryByRole("combobox")).toBeNull();
    expect(within(list).queryByRole("button")).toBeNull();
    expect(within(dialog).queryByRole("button", { name: /^Add / })).toBeNull();
  });

  it("UPM-205: a refused write shows a clear message and keeps the person signed in", async () => {
    const client = failing({
      async put() {
        throw new BrainError(403, "forbidden");
      },
    });
    const { history, dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", client);
    await within(dialog).findByRole("list", { name: "Who has access" });

    act(() => {
      within(dialog).getByRole("radio", { name: "Everyone" }).click();
    });
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("Only the owner can change sharing.");
    expect(history.location.pathname).toBe("/tasks/aster-digest");
    expect(within(dialog).getByRole("radio", { name: "Private" })).toHaveProperty("checked", true);
  });

  it("UPM-205: a failed write names the problem", async () => {
    const client = failing({
      async put() {
        throw new Error("The sharing service is down.");
      },
    });
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", client);
    await within(dialog).findByRole("list", { name: "Who has access" });

    await waitFor(() => {
      expect(within(dialog).getByRole("radio", { name: "Team" })).toHaveProperty("disabled", false);
    });
    act(() => {
      within(dialog).getByRole("radio", { name: "Team" }).click();
    });
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("The sharing service is down.");
  });

  it("UPM-205: reopening the dialog starts clean, without the last error", async () => {
    const client = failing({
      async put() {
        throw new Error("The sharing service is down.");
      },
    });
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", client);
    await within(dialog).findByRole("list", { name: "Who has access" });
    await waitFor(() => {
      expect(within(dialog).getByRole("radio", { name: "Team" })).toHaveProperty("disabled", false);
    });
    act(() => {
      within(dialog).getByRole("radio", { name: "Team" }).click();
    });
    await within(dialog).findByRole("alert");

    act(() => {
      within(dialog).getByRole("button", { name: "Close" }).click();
    });
    expect(screen.queryByRole("dialog", { name: "Share" })).toBeNull();

    act(() => {
      screen.getByRole("button", { name: "Share" }).click();
    });
    const reopened = await screen.findByRole("dialog", { name: "Share" });
    await within(reopened).findByRole("list", { name: "Who has access" });
    expect(within(reopened).queryByRole("alert")).toBeNull();
  });

  it("UPM-205: the dialog cannot be closed while a change is being saved", async () => {
    let release: () => void = () => {};
    const client = createStubApi();
    const slow = failing({
      put: (taskId, update) =>
        new Promise((resolve) => {
          release = () => resolve(client.shares.put(taskId, update));
        }),
      get: (taskId) => client.shares.get(taskId),
      teams: () => client.shares.teams(),
    });
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", slow);
    await within(dialog).findByRole("list", { name: "Who has access" });
    act(() => {
      within(dialog).getByRole("radio", { name: "Everyone" }).click();
    });
    await waitFor(() => {
      expect(within(dialog).getByRole("button", { name: "Close" })).toHaveProperty("disabled", true);
    });
    act(() => {
      release();
    });
    await waitFor(() => {
      expect(within(dialog).getByRole("button", { name: "Close" })).toHaveProperty("disabled", false);
    });
  });

  it("UPM-205: nobody is pre-selected when adding access", async () => {
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest");
    const person = await within(dialog).findByRole("combobox", { name: "Person" });
    const team = await within(dialog).findByRole("combobox", { name: "Team" });
    expect(person).toHaveProperty("value", "");
    expect(team).toHaveProperty("value", "");
    expect(within(dialog).getByRole("button", { name: "Add person" })).toHaveProperty("disabled", true);
    expect(within(dialog).getByRole("button", { name: "Add team" })).toHaveProperty("disabled", true);

    choose(dialog, "Person", "Noor Ali (noor@aster.example)");
    expect(within(dialog).getByRole("button", { name: "Add person" })).toHaveProperty("disabled", false);
    expect(within(dialog).getByRole("button", { name: "Add team" })).toHaveProperty("disabled", true);
  });

  it("UPM-205: says so when the people and teams to add cannot be loaded", async () => {
    const client = createStubApi();
    const broken: ApiClient = {
      ...client,
      members: {
        ...client.members,
        async list() {
          throw new Error("down");
        },
      },
    };
    const { dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", broken);
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("The people and teams you can share with could not be loaded.");
  });

  it("UPM-205: a 403 when reading sharing is shown and does not sign the person out", async () => {
    const client = failing({
      async get() {
        throw new BrainError(403, "forbidden");
      },
    });
    const { history, dialog } = await openShare("/tasks/aster-digest", "Daily finance digest", client);
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("You do not have access to this task's sharing.");
    expect(within(dialog).queryByText("Loading sharing.")).toBeNull();
    expect(history.location.pathname).toBe("/tasks/aster-digest");
  });
});

describe("sharing in the stub", () => {
  it("UPM-205: refuses a write from anyone but the owner with a 403", async () => {
    const api = createStubApi();
    await api.me.viewAs("member");
    const before = await api.shares.get("aster-digest");
    const update: ShareUpdate = { visibility: "everyone", members: [] };
    await expect(api.shares.put("aster-digest", update)).rejects.toMatchObject({
      name: "BrainError",
      status: 403,
    });
    expect(await api.shares.get("aster-digest")).toEqual(before);
  });

  it("UPM-205: refuses an owner level and removal or demotion of the owner", async () => {
    const api = createStubApi();
    const owner = { subject_type: "person", subject_id: "sam", role: "owner" } as const;
    const lee = { subject_type: "person", subject_id: "lee", role: "viewer" } as const;
    const noor = { subject_type: "person", subject_id: "noor" } as const;

    await expect(
      api.shares.put("aster-digest", {
        visibility: "private",
        members: [owner, lee, { ...noor, role: "owner" }],
      }),
    ).rejects.toThrow("Owner is not a share level.");
    await expect(api.shares.put("aster-digest", { visibility: "private", members: [lee] })).rejects.toThrow(
      "The owner cannot be removed.",
    );
    await expect(
      api.shares.put("aster-digest", { visibility: "private", members: [{ ...owner, role: "viewer" }, lee] }),
    ).rejects.toThrow("The owner cannot be changed.");
    await expect(
      api.shares.put("aster-digest", {
        visibility: "private",
        members: [owner, lee, { subject_type: "team", subject_id: "nobody", role: "viewer" }],
      }),
    ).rejects.toThrow("That team does not exist.");

    const stored = await api.shares.get("aster-digest");
    expect(stored.members.map((member) => `${member.subject_id}:${member.role}`)).toEqual([
      "sam:owner",
      "lee:viewer",
    ]);
  });

  it("UPM-205: refuses a subject that is neither a person nor a team", async () => {
    const api = createStubApi();
    const owner = { subject_type: "person", subject_id: "sam", role: "owner" } as const;
    const lee = { subject_type: "person", subject_id: "lee", role: "viewer" } as const;
    const odd = { subject_type: "service", subject_id: "bot", role: "viewer" } as unknown as ShareUpdate["members"][number];
    await expect(api.shares.put("aster-digest", { visibility: "private", members: [owner, lee, odd] })).rejects.toThrow(
      "Share with a person or a team.",
    );
    const stored = await api.shares.get("aster-digest");
    expect(stored.members).toHaveLength(2);
  });

  it("UPM-205: keeps the stored share when a caller edits what it was given", async () => {
    const api = createStubApi();
    const share = await api.shares.get("aster-digest");
    share.members[0]!.role = "viewer";
    share.visibility = "everyone";
    const stored = await api.shares.get("aster-digest");
    expect(stored.members[0]?.role).toBe("owner");
    expect(stored.visibility).toBe("private");
  });

  it("UPM-205: names a person's role only from a direct entry", () => {
    const share: TaskShare = {
      visibility: "team",
      members: [
        { subject_type: "person", subject_id: "sam", name: "Sam Rivera", role: "owner" },
        { subject_type: "team", subject_id: "lee", name: "Lee team", role: "editor" },
      ],
    };
    expect(shareRole(share, "sam")).toBe("owner");
    expect(shareRole(share, "lee")).toBeNull();
    expect(shareRole(undefined, "sam")).toBeNull();
    expect(shareRole(share, undefined)).toBeNull();
  });
});
