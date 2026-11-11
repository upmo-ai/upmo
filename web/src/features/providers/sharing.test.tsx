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
  scope: "own",
  team_ids: [],
  team_names: [],
  can_manage: true,
};
const forTeams: Row = {
  ...personal,
  id: "p-team",
  name: "Finance and Ops models",
  scope: "team",
  team_ids: [financeTeam, opsTeam],
  team_names: ["Finance", "Ops"],
  can_manage: false,
};
const forTeamsAsAdmin: Row = { ...forTeams, id: "p-team-admin", name: "Admin's team account", can_manage: true };
const forWorkspace: Row = {
  ...personal,
  id: "p-workspace",
  name: "Everyone models",
  scope: "workspace",
  can_manage: false,
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
    scope: "own",
    ...patch,
  });

  it("lets a person manage what the brain says they may, and else their own and not the workspace's", () => {
    expect(canManage(account({ scope: "own" }))).toBe(true);
    expect(canManage(account({ scope: "global" }))).toBe(false);
    const team = { scope: "team" as const, team_ids: [financeTeam], team_names: ["Finance"] };
    expect(canManage(account({ scope: "global", share: { ...team, can_manage: true } }))).toBe(true);
    expect(canManage(account({ scope: "global", share: { ...team, can_manage: false } }))).toBe(false);
  });

  it("words who a shared account is shared with, and stays quiet for a personal one", () => {
    const team = { scope: "team" as const, team_ids: [financeTeam, opsTeam], team_names: ["Finance", "Ops"] };
    expect(sharedNote(account({ scope: "global", share: { ...team, can_manage: false } }))).toEqual({
      badge: "Team",
      text: "Shared with Finance, Ops. Read only.",
    });
    expect(sharedNote(account({ scope: "global", share: { ...team, can_manage: true } }))).toEqual({
      badge: "Team",
      text: "Shared with Finance, Ops.",
    });
    expect(
      sharedNote(account({ scope: "global", share: { scope: "workspace", team_ids: [], team_names: [], can_manage: false } })),
    ).toEqual({ badge: "Workspace", text: "Shared with the whole workspace. Read only." });
    expect(sharedNote(account({ scope: "global" }))).toEqual({
      badge: "Shared",
      text: "Managed by your workspace. Read only.",
    });
    expect(sharedNote(account({ scope: "own" }))).toBeNull();
    expect(
      sharedNote(account({ scope: "own", share: { scope: "own", team_ids: [], team_names: [], can_manage: true } })),
    ).toBeNull();
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

    expect(withNote("p-team").getByText("Team")).toBeTruthy();
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
    click(screen.getByRole("radio", { name: /Specific teams/ }));
    // A team has to be chosen before the next step.
    expect(await screen.findByRole("checkbox", { name: "Share with Finance" })).toBeTruthy();
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(true);
    click(screen.getByRole("checkbox", { name: "Share with Finance" }));
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);

    const sent = await connect(requests);
    expect(sent.scope).toBe("teams");
    expect(sent.team_ids).toEqual([financeTeam]);
    expect(JSON.stringify(sent)).not.toContain(opsTeam);
  });

  it("sends a share with the whole workspace without teams", async () => {
    const requests = stubBrain({ canShare: true });
    await openCredentials();

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    click(screen.getByRole("radio", { name: /The whole workspace/ }));
    const sent = await connect(requests);
    expect(sent.scope).toBe("workspace");
    expect(sent).not.toHaveProperty("team_ids");
  });

  it("keeps an admin's account personal unless they choose to share it", async () => {
    const requests = stubBrain({ canShare: true });
    await openCredentials();

    expect(await screen.findByText("Who can use this provider")).toBeTruthy();
    const sent = await connect(requests);
    expect(sent).not.toHaveProperty("scope");
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
    expect(sent).not.toHaveProperty("scope");
  });
});
