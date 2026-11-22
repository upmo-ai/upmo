import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createBrainApi } from "../../app/brain-api";
import { AppProviders } from "../../theme";

const taskId = "01990000-0000-7000-8000-000000000001";
const adaId = "01990000-0000-7000-8000-0000000000a1";
const leeId = "01990000-0000-7000-8000-0000000000a2";
const financeId = "01990000-0000-7000-8000-0000000000d1";
const opsId = "01990000-0000-7000-8000-0000000000d2";
const teams = [
  { id: financeId, name: "Finance" },
  { id: opsId, name: "Ops" },
];

type Grant = { subject_type: "person" | "team"; subject_id: string; role: string };
type Call = { method: string; path: string; body: unknown };

const ownerGrant: Grant = { subject_type: "person", subject_id: adaId, role: "owner" };

/**
 * A brain that keeps one task and its share in memory and answers the sharing
 * routes the way the real ones do: the share names its people, a team
 * visibility needs a team the owner belongs to, and a refusal is a 403 or a
 * 422 whose message is written for the person.
 */
function brain(
  options: {
    ownerTeams?: string[];
    shareViewer?: boolean;
    refusePut?: () => Response;
    list?: () => Response;
  } = {},
) {
  const ownerTeams = options.ownerTeams ?? ["Finance"];
  const people = [
    { id: adaId, name: "Ada Lovelace", email: "ada@example.com", role: "owner", teams: ownerTeams },
    { id: leeId, name: "Lee Chen", email: "lee@example.com", role: "member", teams: [] as string[] },
  ];
  const task = {
    id: taskId,
    title: "Sort the morning mail",
    status: "Working",
    phase: "Working",
    spent_cents: 0,
    limit_cents: 0,
    updated_at: "2026-11-08T09:00:00Z",
    result: "",
    detail: "",
    subtasks: [],
    events: [],
  };
  let share: { visibility: string; team_id?: string; members: (Grant & { name: string })[] } = {
    visibility: "private",
    members: [
      { ...ownerGrant, name: "Ada Lovelace" },
      ...(options.shareViewer
        ? [{ subject_type: "person" as const, subject_id: leeId, role: "viewer", name: "Lee Chen" }]
        : []),
    ],
  };
  const calls: Call[] = [];

  const nameOf = (grant: Grant) =>
    grant.subject_type === "team"
      ? teams.find((team) => team.id === grant.subject_id)?.name ?? "a team"
      : people.find((person) => person.id === grant.subject_id)?.name ?? "someone";
  const shareJson = () => ({
    visibility: share.visibility,
    ...(share.team_id ? { team_id: share.team_id } : {}),
    members: share.members,
  });

  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    calls.push({ method, path, body });
    expect(init?.credentials).toBe("include");

    if (method === "GET" && path === "/api/session") {
      return Response.json({
        user_id: adaId,
        sign_in_method: "password",
        has_password: true,
        passkey_count: 1,
      });
    }
    // The page and the bell read these too; the brain has no approvals or notices here.
    if (method === "GET" && (path === "/api/approvals" || path.startsWith("/api/notifications"))) {
      return Response.json([]);
    }
    if (path === "/api/tasks" && method === "GET") return options.list?.() ?? Response.json([task]);
    if (path === `/api/tasks/${taskId}` && method === "GET") return Response.json(task);
    if (path === "/api/members") {
      return Response.json(
        people.map((person) => ({
          id: person.id,
          name: person.name,
          email: person.email,
          invited: false,
          teams: person.teams,
          role: { id: `role-${person.role}`, name: person.role, system: person.role },
        })),
      );
    }
    if (path === "/api/teams") return Response.json(teams);
    if (path === `/api/tasks/${taskId}/share` && method === "GET") return Response.json(shareJson());
    if (path === `/api/tasks/${taskId}/share` && method === "PUT") {
      const refusal = options.refusePut?.();
      if (refusal) return refusal;
      const update = body as { visibility: string; team_id?: string; members: Grant[] };
      let team_id = update.team_id;
      if (update.visibility === "team") {
        const mine = teams.filter((team) => ownerTeams.includes(team.name));
        if (!team_id) {
          if (mine.length !== 1) {
            return Response.json(
              {
                error:
                  mine.length === 0
                    ? "the owner belongs to no team"
                    : "the owner belongs to more than one team",
              },
              { status: 422 },
            );
          }
          team_id = mine[0]!.id;
        } else if (!mine.some((team) => team.id === team_id)) {
          return Response.json({ error: "the owner does not belong to that team" }, { status: 422 });
        }
      } else {
        team_id = undefined;
      }
      share = {
        visibility: update.visibility,
        team_id,
        members: update.members.map((grant) => ({ ...grant, name: nameOf(grant) })),
      };
      return Response.json(shareJson());
    }
    return new Response(null, { status: 404 });
  });
  return { fetcher, calls };
}

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={createBrainApi()} />
    </AppProviders>,
  );
  return history;
}

async function openShare() {
  const history = renderAt(`/tasks/${taskId}`);
  expect(await screen.findByRole("heading", { level: 1, name: "Sort the morning mail" })).toBeTruthy();
  act(() => {
    screen.getByRole("button", { name: "Share" }).click();
  });
  const dialog = await screen.findByRole("dialog", { name: "Share" });
  await within(dialog).findByRole("list", { name: "Who has access" });
  return { history, dialog };
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

function putBodies(calls: Call[]) {
  return calls
    .filter((call) => call.method === "PUT" && call.path === `/api/tasks/${taskId}/share`)
    .map((call) => call.body);
}

describe("task shares from the brain", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the stored share with names, the owner first", async () => {
    vi.stubGlobal("fetch", brain({ shareViewer: true }).fetcher);
    const { dialog } = await openShare();

    const rows = within(within(dialog).getByRole("list", { name: "Who has access" })).getAllByRole(
      "listitem",
    );
    expect(rows).toHaveLength(2);
    expect(rows[0]!.textContent).toContain("Ada Lovelace");
    expect(within(rows[0]!).getByText("Owner")).toBeTruthy();
    expect(rows[1]!.textContent).toContain("Lee Chen");
    expect(
      within(rows[1]!).getByRole("combobox", { name: "Access for Lee Chen" }),
    ).toHaveProperty("value", "Viewer");
    expect(within(dialog).getByRole("radio", { name: "Private" })).toHaveProperty("checked", true);
  });

  it("adds a person as a viewer and PUTs the update the brain expects", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    const { dialog } = await openShare();

    choose(dialog, "Person", "Lee Chen (lee@example.com)");
    act(() => {
      within(dialog).getByRole("button", { name: "Add person" }).click();
    });
    await waitFor(() => {
      expect(accessRow(dialog, "Lee Chen")).toBeTruthy();
    });
    expect(putBodies(calls)).toEqual([
      {
        visibility: "private",
        members: [ownerGrant, { subject_type: "person", subject_id: leeId, role: "viewer" }],
      },
    ]);
    // The stored share came back and is shown: the new person reads as a Viewer.
    expect(
      within(accessRow(dialog, "Lee Chen")).getByRole("combobox", { name: "Access for Lee Chen" }),
    ).toHaveProperty("value", "Viewer");
  });

  it("shows the owner-only message on a 403 and keeps the dialog usable", async () => {
    vi.stubGlobal(
      "fetch",
      brain({ refusePut: () => Response.json({ error: "unauthorized" }, { status: 403 }) }).fetcher,
    );
    const { history, dialog } = await openShare();

    act(() => {
      within(dialog).getByRole("radio", { name: "Everyone" }).click();
    });
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("Only the owner can change sharing.");
    expect(history.location.pathname).toBe(`/tasks/${taskId}`);
    expect(within(dialog).getByRole("radio", { name: "Private" })).toHaveProperty("checked", true);
    expect(within(dialog).getByRole("button", { name: "Close" })).toHaveProperty("disabled", false);
  });

  it("shows the brain's own words when it refuses the update", async () => {
    // The owner belongs to no team, so the brain refuses a team visibility with its message.
    vi.stubGlobal("fetch", brain({ ownerTeams: [] }).fetcher);
    const { dialog } = await openShare();

    await waitFor(() => {
      expect(within(dialog).getByRole("radio", { name: "Team" })).toHaveProperty("disabled", false);
    });
    act(() => {
      within(dialog).getByRole("radio", { name: "Team" }).click();
    });
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("the owner belongs to no team");
    expect(within(dialog).getByRole("radio", { name: "Private" })).toHaveProperty("checked", true);
  });

  it("sends no team when the owner has one: the brain names it", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    const { dialog } = await openShare();

    await waitFor(() => {
      expect(within(dialog).getByRole("radio", { name: "Team" })).toHaveProperty("disabled", false);
    });
    act(() => {
      within(dialog).getByRole("radio", { name: "Team" }).click();
    });
    await waitFor(() => {
      expect(within(dialog).getByRole("radio", { name: "Team" })).toHaveProperty("checked", true);
    });
    expect(within(dialog).queryByRole("combobox", { name: "Shared team" })).toBeNull();
    expect(putBodies(calls)).toEqual([{ visibility: "team", members: [ownerGrant] }]);
  });

  it("lets the owner pick the team when they belong to two, and remembers it", async () => {
    const { fetcher, calls } = brain({ ownerTeams: ["Finance", "Ops"] });
    vi.stubGlobal("fetch", fetcher);
    const { dialog } = await openShare();
    expect(within(dialog).queryByRole("combobox", { name: "Shared team" })).toBeNull();

    // The radios wait for the roster and the teams, so the first PUT can name the team.
    await waitFor(() => {
      expect(within(dialog).getByRole("radio", { name: "Team" })).toHaveProperty("disabled", false);
    });
    act(() => {
      within(dialog).getByRole("radio", { name: "Team" }).click();
    });
    const picker = await within(dialog).findByRole("combobox", { name: "Shared team" });
    expect(picker).toHaveProperty("value", "Finance");
    // The first of the owner's teams is sent, so the visibility takes.
    expect(putBodies(calls)).toEqual([{ visibility: "team", team_id: financeId, members: [ownerGrant] }]);

    choose(dialog, "Shared team", "Ops");
    await waitFor(() => {
      expect(within(dialog).getByRole("combobox", { name: "Shared team" })).toHaveProperty(
        "value",
        "Ops",
      );
    });
    expect(putBodies(calls).slice(-1)).toEqual([
      { visibility: "team", team_id: opsId, members: [ownerGrant] },
    ]);

    // The saved team opens picked.
    act(() => {
      within(dialog).getByRole("button", { name: "Close" }).click();
    });
    act(() => {
      screen.getByRole("button", { name: "Share" }).click();
    });
    const reopened = await screen.findByRole("dialog", { name: "Share" });
    expect(await within(reopened).findByRole("combobox", { name: "Shared team" })).toHaveProperty(
      "value",
      "Ops",
    );
  });

  it("does not hide a 401 behind the sample share", async () => {
    vi.stubGlobal(
      "fetch",
      brain({ list: () => Response.json({ error: "unauthorized" }, { status: 401 }) }).fetcher,
    );
    const history = renderAt(`/tasks/${taskId}`);

    await waitFor(() => {
      expect(history.location.pathname).toBe("/login");
    });
    expect(screen.queryByText("Daily finance digest")).toBeNull();
  });

  it("uses the sample sharing for the whole session when the brain answers 404", async () => {
    const { fetcher, calls } = brain({ list: () => new Response(null, { status: 404 }) });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/tasks/aster-digest");
    expect(await screen.findByRole("heading", { level: 1, name: "Daily finance digest" })).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Share" }).click();
    });
    const dialog = await screen.findByRole("dialog", { name: "Share" });
    await within(dialog).findByRole("list", { name: "Who has access" });
    expect(within(accessRow(dialog, "Sam Rivera")).getByText("Owner")).toBeTruthy();
    // The roster, the teams and the share itself stayed on the sample data.
    expect(
      calls.filter(
        (call) => call.path === "/api/members" || call.path === "/api/teams" || call.path.endsWith("/share"),
      ),
    ).toEqual([]);
  });
});
