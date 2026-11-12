import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";
import { canManage, sharedNote, type ListedProvider } from "./sharing";

const financeTeam = "01990000-0000-7000-8000-0000000000d1";
const opsTeam = "01990000-0000-7000-8000-0000000000d2";

// An account as the brain sends it.
type Row = Record<string, unknown>;

const personal: Row = {
  id: "p-own",
  provider: "anthropic",
  name: "My Claude",
  adapter: "openai_compat",
  plan_kind: "metered",
  base_url: "https://api.anthropic.com",
  status: "Connected",
  usable: true,
  usable_models: ["gpt-x"],
  models: ["claude-3-7-sonnet"],
  visibility: "private",
  team_ids: [],
  team_names: [],
  can_manage: true,
  can_use: true,
  is_owner: true,
};
const forTeams: Row = {
  ...personal,
  id: "p-team",
  name: "Finance and Ops models",
  visibility: "team",
  team_ids: [financeTeam, opsTeam],
  team_names: ["Finance", "Ops"],
  can_manage: false,
  is_owner: false,
};
const forTeamsAsAdmin: Row = {
  ...forTeams,
  id: "p-team-admin",
  name: "Admin's team account",
  can_manage: true,
  is_owner: true,
};
const forWorkspace: Row = {
  ...personal,
  id: "p-workspace",
  name: "Everyone models",
  visibility: "everyone",
  can_manage: false,
  is_owner: false,
};

interface Recorded {
  url: string;
  method: string;
  body: Record<string, unknown> | null;
}

// Puts the app in live mode (GET /api/tasks answers) with a brain that lists
// the accounts, says whether the person may share, and lists the teams.
function stubBrain(options: { accounts?: Row[]; canShare?: boolean }) {
  const requests: Recorded[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body =
        typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : null;
      requests.push({ url, method, body });
      if (url === "/api/tasks" && method === "GET") return Response.json([]);
      if (url === "/api/providers" && method === "GET") return Response.json(options.accounts ?? []);
      if (url === "/api/providers/sharing" && method === "GET") {
        return Response.json({ can_share: options.canShare ?? false });
      }
      if (url === "/api/teams" && method === "GET") {
        return Response.json([
          { id: financeTeam, name: "Finance" },
          { id: opsTeam, name: "Ops" },
        ]);
      }
      if (url === "/api/providers/test" && method === "POST") return Response.json({ ok: true });
      if (url === "/api/providers" && method === "POST") {
        return Response.json({ ...personal, id: "made", name: String(body?.name ?? "") }, { status: 201 });
      }
      if (method === "PUT" && url.startsWith("/api/providers/")) {
        const id = url.slice("/api/providers/".length);
        const current = (options.accounts ?? []).find((account) => account.id === id);
        return Response.json({ ...(current ?? personal), ...body, id });
      }
      return new Response(null, { status: 404 });
    }),
  );
  return requests;
}

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

function click(element: HTMLElement) {
  act(() => {
    element.click();
  });
}

function clickButton(name: string) {
  click(screen.getByRole("button", { name }));
}

async function continueTo(expected: string | RegExp) {
  clickButton("Continue");
  expect(await screen.findByText(expected)).toBeTruthy();
}

function isDisabled(element: HTMLElement) {
  return element.getAttribute("disabled") !== null;
}

// Walks the wizard for Ollama, which needs no key, up to its credentials step.
async function openCredentials() {
  renderAt("/providers/new");
  expect(await screen.findByRole("heading", { level: 1, name: "Connect a provider" })).toBeTruthy();
  clickButton("Ollama");
  await continueTo(/Ollama Credentials & Plan/);
}

async function connect(requests: Recorded[]) {
  await continueTo(/Models & Pricing for Ollama/);
  await continueTo("Review Configuration");
  clickButton("Test connection");
  expect(await screen.findByText("Connection verified")).toBeTruthy();
  clickButton("Connect provider");
  await waitFor(() => {
    expect(requests.some((request) => request.method === "POST" && request.url === "/api/providers")).toBe(true);
  });
  const post = requests.find((request) => request.method === "POST" && request.url === "/api/providers");
  if (!post?.body) throw new Error("the new account was not sent");
  return post.body;
}

// A card and the line under it share one parent.
function withNote(id: string) {
  return within(screen.getByTestId(`provider-card-${id}`).parentElement as HTMLElement);
}

describe("what the page says about sharing", () => {
  const account = (patch: Partial<ListedProvider>): ListedProvider => ({
    id: "a",
    provider: "openai",
    name: "A",
    adapter: "openai_compat",
    plan_kind: "metered",
    status: "Connected",
    usable: true,
    usable_models: ["gpt-x"],
    models: [],
    visibility: "private",
    team_ids: [],
    team_names: [],
    can_manage: true,
    can_use: true,
    is_owner: true,
    ...patch,
  });

  it("lets a person manage what the brain says they may", () => {
    expect(canManage(account({ can_manage: true }))).toBe(true);
    expect(canManage(account({ can_manage: false }))).toBe(false);
  });

  it("words who a shared account is shared with, and stays quiet for a personal one", () => {
    expect(
      sharedNote(
        account({
          visibility: "team",
          team_ids: [financeTeam, opsTeam],
          team_names: ["Finance", "Ops"],
          can_manage: false,
        }),
      ),
    ).toEqual({
      badge: "Teams",
      text: "Shared with Finance, Ops. Read only.",
    });
    expect(
      sharedNote(
        account({
          visibility: "team",
          team_ids: [financeTeam, opsTeam],
          team_names: ["Finance", "Ops"],
          can_manage: true,
        }),
      ),
    ).toEqual({
      badge: "Teams",
      text: "Shared with Finance, Ops.",
    });
    expect(sharedNote(account({ visibility: "everyone", can_manage: false }))).toEqual({
      badge: "Workspace",
      text: "Shared with the whole workspace. Read only.",
    });
    expect(sharedNote(account({ visibility: "team", team_names: [], can_manage: false }))).toEqual({
      badge: "Teams",
      text: "Shared with your team. Read only.",
    });
    expect(sharedNote(account({ visibility: "private" }))).toBeNull();
  });
});

describe("sharing a provider", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows who an account is shared with, and Edit and Disconnect only where the brain says the person may", async () => {
    stubBrain({ accounts: [personal, forTeams, forTeamsAsAdmin, forWorkspace] });
    renderAt("/providers");

    await screen.findByTestId("provider-card-p-own");
    const buttons = (id: string) => {
      const card = within(screen.getByTestId(`provider-card-${id}`));
      return [card.queryByRole("button", { name: "Edit" }), card.queryByRole("button", { name: "Disconnect" })];
    };
    // Their own, and a team account they manage, can be changed.
    for (const id of ["p-own", "p-team-admin"]) {
      const [edit, disconnect] = buttons(id);
      expect([id, Boolean(edit)]).toEqual([id, true]);
      expect([id, Boolean(disconnect)]).toEqual([id, true]);
    }
    // A team account and a workspace account they only use cannot.
    for (const id of ["p-team", "p-workspace"]) {
      const [edit, disconnect] = buttons(id);
      expect([id, edit]).toEqual([id, null]);
      expect([id, disconnect]).toEqual([id, null]);
    }

    expect(withNote("p-team").getByText("Teams")).toBeTruthy();
    expect(withNote("p-team").getByText("Shared with Finance, Ops. Read only.")).toBeTruthy();
    expect(withNote("p-team-admin").getByText("Shared with Finance, Ops.")).toBeTruthy();
    expect(withNote("p-workspace").getByText("Workspace")).toBeTruthy();
    expect(withNote("p-workspace").getByText("Shared with the whole workspace. Read only.")).toBeTruthy();
    expect(withNote("p-own").queryByText(/^Shared with/)).toBeNull();
  });

  it("opens a shared account the person may not manage as a read only page", async () => {
    stubBrain({ accounts: [forTeams] });
    renderAt("/providers/p-team/edit");

    expect(await screen.findByText("This provider is shared by the workspace. It is read only here.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });

  it("offers an admin the sharing choice and sends specific teams with the account", async () => {
    const requests = stubBrain({ canShare: true });
    await openCredentials();

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    click(screen.getByRole("radio", { name: /^Teams/ }));
    // A team has to be chosen before the next step.
    expect(await screen.findByRole("checkbox", { name: "Share with Finance" })).toBeTruthy();
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(true);
    click(screen.getByRole("checkbox", { name: "Share with Finance" }));
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);

    const sent = await connect(requests);
    expect(sent.visibility).toBe("team");
    expect(sent.team_ids).toEqual([financeTeam]);
    expect(JSON.stringify(sent)).not.toContain(opsTeam);
  });

  it("sends a share with the whole workspace without teams", async () => {
    const requests = stubBrain({ canShare: true });
    await openCredentials();

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    click(screen.getByRole("radio", { name: /^Workspace/ }));
    const sent = await connect(requests);
    expect(sent.visibility).toBe("everyone");
    expect(sent).not.toHaveProperty("team_ids");
  });

  it("keeps an admin's account personal unless they choose to share it", async () => {
    const requests = stubBrain({ canShare: true });
    await openCredentials();

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    const sent = await connect(requests);
    expect(sent).not.toHaveProperty("visibility");
    expect(sent).not.toHaveProperty("team_ids");
  });

  it("does not ask a person who may not share", async () => {
    const requests = stubBrain({ canShare: false });
    await openCredentials();

    await waitFor(() => {
      expect(requests.some((request) => request.url === "/api/providers/sharing")).toBe(true);
    });
    expect(screen.queryByText("Who can use this provider")).toBeNull();
    const sent = await connect(requests);
    expect(sent).not.toHaveProperty("visibility");
  });

  async function saveEdit(requests: Recorded[]) {
    await continueTo(/Models & Pricing for/);
    await continueTo("Review Configuration");
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Save changes");
    await waitFor(() => {
      expect(requests.some((request) => request.method === "PUT" && request.url.startsWith("/api/providers/"))).toBe(
        true,
      );
    });
    const put = requests.find((request) => request.method === "PUT" && request.url.startsWith("/api/providers/"));
    if (!put?.body) throw new Error("the change was not sent");
    return put.body;
  }

  it("offers the sharing choice when editing a private account and sends the teams that were picked", async () => {
    const requests = stubBrain({ accounts: [personal], canShare: true });
    renderAt("/providers/p-own/edit");

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    expect(screen.getByRole("radio", { name: /^Only me/ })).toBeTruthy();
    click(screen.getByRole("radio", { name: /^Teams/ }));
    click(await screen.findByRole("checkbox", { name: "Share with Finance" }));

    const sent = await saveEdit(requests);
    expect(sent.visibility).toBe("team");
    expect(sent.team_ids).toEqual([financeTeam]);
  });

  it("lets the owner of a shared account take it back to only them", async () => {
    const requests = stubBrain({ accounts: [forTeamsAsAdmin], canShare: true });
    renderAt("/providers/p-team-admin/edit");

    expect(await screen.findByRole("radio", { name: /^Only me/ })).toBeTruthy();
    click(screen.getByRole("radio", { name: /^Only me/ }));

    const sent = await saveEdit(requests);
    expect(sent.visibility).toBe("private");
    expect(sent).not.toHaveProperty("team_ids");
  });

  it("hides Only me when a person who may share does not own the account", async () => {
    stubBrain({ accounts: [{ ...forTeamsAsAdmin, is_owner: false }], canShare: true });
    renderAt("/providers/p-team-admin/edit");

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    expect(screen.getByRole("radio", { name: /^Teams/ })).toBeTruthy();
    expect(screen.getByRole("radio", { name: /^Workspace/ })).toBeTruthy();
    expect(screen.queryByRole("radio", { name: /^Only me/ })).toBeNull();
  });

  it("does not ask a person who may not share when they edit an account", async () => {
    const requests = stubBrain({ accounts: [personal], canShare: false });
    renderAt("/providers/p-own/edit");

    expect(await screen.findByRole("heading", { level: 1, name: "Edit My Claude" })).toBeTruthy();
    await waitFor(() => {
      expect(requests.some((request) => request.url === "/api/providers/sharing")).toBe(true);
    });
    expect(screen.queryByText("Who can use this provider")).toBeNull();
    const sent = await saveEdit(requests);
    expect(sent).not.toHaveProperty("visibility");
  });

  it("sends no visibility when an edit leaves the sharing choice alone", async () => {
    const requests = stubBrain({ accounts: [forTeamsAsAdmin], canShare: true });
    renderAt("/providers/p-team-admin/edit");

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    const sent = await saveEdit(requests);
    expect(sent).not.toHaveProperty("visibility");
    expect(sent).not.toHaveProperty("team_ids");
  });
});
