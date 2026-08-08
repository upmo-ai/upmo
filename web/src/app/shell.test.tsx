import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../App";
import { AppProviders } from "../theme";
import {
  BrainError,
  createStubApi,
  type ApiClient,
  type ApiToken,
  type CreatedToken,
  type CurrentUser,
  type Member,
  type Passkey,
  type TaskShare,
  type Team,
} from "./api";

const routes = [
  ["/", "Home", "Home"],
  ["/chat", "Chat", "Chat"],
  ["/tasks", "Tasks", "Tasks"],
  ["/automations", "Automations", "Automations"],
  ["/agents", "Agents", "Agents"],
  ["/knowledge", "Knowledge", "Knowledge"],
  ["/usage", "Usage", "Usage"],
  ["/settings", "Security", "Settings"],
  ["/settings/members", "Members", "Settings"],
  ["/settings/tokens", "API tokens", "Settings"],
  ["/settings/connectors", "Connectors", "Settings"],
] as const;

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
}

function sidebar() {
  return screen.getByRole("navigation", { name: "Sidebar" });
}

const signedOut: CurrentUser = {
  memberId: "sam",
  view: "admin",
  signIn: null,
  passwordSet: false,
  offerDismissed: false,
};

function identityStub(): Pick<ApiClient, "session" | "me" | "passkeys" | "password" | "members" | "shares" | "tokens"> {
  const passkey: Passkey = { id: "pk", name: "Passkey", added: "", lastUsed: "" };
  const member: Member = { id: "m", name: "Member", email: "member@aster.example", role: "member", teams: [] };
  const share = (taskId: string): TaskShare => ({
    taskId,
    visibility: "private",
    teamId: null,
    people: [],
  });
  const token: ApiToken = {
    id: "token",
    name: "Token",
    hint: "upmo_pat_stub",
    scopes: [],
    created: "",
    lastUsed: null,
    expires: "",
  };
  return {
    session: {
      async get() {
        return signedOut;
      },
    },
    me: {
      async current() {
        return signedOut;
      },
      async signIn(method) {
        return { ...signedOut, signIn: method };
      },
      async dismissPasskeyOffer() {},
      async viewAs(role) {
        return { ...signedOut, view: role, memberId: "lee" };
      },
    },
    passkeys: {
      async list() {
        return [];
      },
      async add(name) {
        return { ...passkey, name };
      },
      async rename(id, name) {
        return { ...passkey, id, name };
      },
      async remove() {},
    },
    password: {
      async set() {},
      async change() {},
      async remove() {},
    },
    members: {
      async list() {
        return [];
      },
      async invite(input) {
        return { ...member, email: input.email, role: input.role };
      },
      async setRole(id, role) {
        return { ...member, id, role };
      },
      async remove() {},
    },
    shares: {
      async teams() {
        return [] as Team[];
      },
      async get(taskId) {
        return share(taskId);
      },
      async setVisibility(taskId, visibility, teamId) {
        return { ...share(taskId), visibility, teamId };
      },
      async addPerson(taskId, memberId, level) {
        return { ...share(taskId), people: [{ memberId, level }] };
      },
      async setLevel(taskId, memberId, level) {
        return { ...share(taskId), people: [{ memberId, level }] };
      },
      async removePerson(taskId) {
        return share(taskId);
      },
    },
    tokens: {
      async list() {
        return [];
      },
      async create(input): Promise<CreatedToken> {
        return {
          token: { ...token, name: input.name, scopes: input.scopes },
          secret: "upmo_pat_secret",
        };
      },
      async revoke() {},
    },
  };
}

describe("app shell", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists primary nav then Workspace and Settings", async () => {
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    expect(within(sidebar()).getByRole("link", { name: "Upmo" }).getAttribute("href")).toBe("/login");
    const links = within(sidebar())
      .getAllByRole("link")
      .filter((link) => link.querySelector("img") == null);
    expect(links.map((link) => link.textContent?.trim())).toEqual([
      "Home",
      "Chat",
      "Tasks",
      "Automations",
      "Agents",
      "Knowledge",
      "Usage",
      "Settings",
    ]);
    expect(within(sidebar()).getByText("Workspace")).toBeTruthy();
    expect(within(sidebar()).getByText("Aster")).toBeTruthy();
    expect(screen.queryByRole("menu", { name: "Workspaces" })).toBeNull();
    expect(within(sidebar()).getByTestId("theme-switch")).toBeTruthy();
    expect(within(sidebar()).getByText("0.1.0")).toBeTruthy();
    expect(within(sidebar()).getByRole("img", { name: "Upmo" })).toBeTruthy();
  });

  it("uses an injected API client", async () => {
    const client: ApiClient = {
      async listTasks() {
        return [
          {
            id: "custom-task",
            title: "Custom task",
            status: "Working",
            phase: "Preparing",
            result: "Custom task is finished.",
            detail: "A custom task.",
          },
        ];
      },
      async listNotifications() {
        return [];
      },
      async approveNotification() {},
      async nextPhase() {
        return {
          id: "custom-task",
          title: "Custom task",
          status: "Working",
          phase: "Planning",
          result: "Custom task is finished.",
          detail: "A custom task.",
        };
      },
      approvals: {
        async list() {
          return [];
        },
        async approve(id) {
          return { id, taskId: id, title: "", action: "", state: "approved" };
        },
        async deny(id) {
          return { id, taskId: id, title: "", action: "", state: "denied" };
        },
      },
      ...identityStub(),
    };
    renderAt("/tasks", client);
    expect(await screen.findByText("Custom task")).toBeTruthy();
  });

  it("shows the unread count and lists approvals before finished tasks", async () => {
    renderAt("/");
    const bell = await screen.findByRole("button", { name: "Notifications, 4 unread" });
    act(() => {
      bell.click();
    });
    const items = await screen.findAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual([
      "Approve the digestApprove",
      "Question about the budgetApprove",
      "Mail triage done",
      "Weekly close failed",
    ]);
    expect(within(items[3]!).queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("removes an approval and drops the unread count", async () => {
    renderAt("/");
    const bell = await screen.findByRole("button", { name: "Notifications, 4 unread" });
    act(() => {
      bell.click();
    });
    const [approval] = await screen.findAllByRole("listitem");
    const approve = within(approval!).getByRole("button", { name: "Approve", hidden: true });
    act(() => {
      approve.click();
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Notifications, 3 unread" })).toBeTruthy();
    });
    const remaining = screen
      .getAllByRole("listitem", { hidden: true })
      .map((item) => item.textContent ?? "");
    expect(remaining.some((text) => text.includes("Approve the digest"))).toBe(false);
    expect(remaining.some((text) => text.includes("Question about the budget"))).toBe(true);
  });

  it("shows the empty bell copy", async () => {
    renderAt("/", {
      async listTasks() {
        return [];
      },
      async listNotifications() {
        return [];
      },
      async approveNotification() {},
      async nextPhase() {
        throw new Error("task missing");
      },
      approvals: {
        async list() {
          return [];
        },
        async approve() {
          throw new Error("approval missing");
        },
        async deny() {
          throw new Error("approval missing");
        },
      },
      ...identityStub(),
    });
    const bell = await screen.findByRole("button", { name: /^Notifications$/ });
    act(() => {
      bell.click();
    });
    expect(await screen.findByText("You are up to date")).toBeTruthy();
    expect(screen.getByText("Approvals and finished tasks show up here.")).toBeTruthy();
  });

  it.each(routes)("%s has one h1 %s and current nav %s", async (path, title, navLabel) => {
    renderAt(path);
    const headings = await screen.findAllByRole("heading", { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0]?.textContent).toBe(title);
    const current = within(sidebar())
      .getAllByRole("link")
      .filter((link) => link.getAttribute("aria-current") === "page");
    expect(current.map((link) => link.textContent?.trim())).toEqual([navLabel]);
  });

  it("unauthorised 401 sends a signed-in route to /login and does not retry; /login still renders with no session", async () => {
    let sessionFetches = 0;
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      session: {
        async get() {
          sessionFetches += 1;
          throw new BrainError(401, "unauthorised");
        },
      },
      me: {
        ...stub.me,
        async current() {
          sessionFetches += 1;
          throw new BrainError(401, "unauthorised");
        },
      },
    };
    renderAt("/", client);
    expect(await screen.findByRole("button", { name: "Log in", exact: true })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();
    expect(sessionFetches).toBe(1);

    cleanup();

    renderAt("/login", client);
    expect(await screen.findByRole("button", { name: "Log in", exact: true })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();
  });

  it("sign-out clears the query cache", async () => {
    let sessionCalls = 0;
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      session: {
        async get() {
          sessionCalls += 1;
          return { ...signedOut, signIn: "password" };
        },
      },
      me: {
        ...stub.me,
        async current() {
          sessionCalls += 1;
          return { ...signedOut, signIn: "password" };
        },
      },
    };
    renderAt("/", client);
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    const beforeCalls = sessionCalls;
    act(() => {
      screen.getByRole("button", { name: "Log out" }).click();
    });
    expect(await screen.findByRole("button", { name: "Log in", exact: true })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Sidebar" })).toBeNull();
    expect(beforeCalls).toBeGreaterThan(0);
  });

  it("/api/session is fetched once per shell load", async () => {
    let sessionFetches = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<() => Promise<Response>>(async (input) => {
        if (String(input).includes("/api/session")) {
          sessionFetches += 1;
          return new Response(
            JSON.stringify({
              user_id: "sam",
              sign_in_method: "password",
              signed_in_at: new Date().toISOString(),
              has_password: true,
            }),
            { status: 200, headers: { "content-type": "application/json" } },
          );
        }
        return new Response("{}", { status: 200 });
      }),
    );

    renderAt("/");
    expect(await screen.findByRole("heading", { level: 1, name: "Home" })).toBeTruthy();
    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Upmo" }).getAttribute("href")).toBe("/");
    });
    expect(sessionFetches).toBe(1);
  });
});
