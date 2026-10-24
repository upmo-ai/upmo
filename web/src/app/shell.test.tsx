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

const sidebarRoutes = [
  ["/", "Home", "Home"],
  ["/chat", "Chat", "Chat"],
  ["/tasks", "Tasks", "Tasks"],
  ["/automations", "Automations", "Automations"],
  ["/agents", "Agents", "Agents"],
  ["/knowledge", "Knowledge", "Knowledge"],
  ["/usage", "Usage", "Usage"],
  ["/providers", "Providers", "Providers"],
  ["/connectors", "Connectors", "Connectors"],
  ["/admin/members", "Members", "Members"],
  ["/admin/teams", "Teams", "Teams"],
  ["/admin/roles", "Roles", "Roles"],
] as const;

const userSettingsRoutes = [
  ["/security", "Security"],
  ["/personalisation", "Personalisation"],
  ["/tokens", "API tokens"],
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
  return screen.getByRole("navigation", { name: "Main" });
}

function rail() {
  const element = document.querySelector<HTMLElement>(".upmo-app-frame__rail");
  if (!element) throw new Error("the rail is not rendered");
  return element;
}

function setWidth(width: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, writable: true, value: width });
}

function linkLabels(container: HTMLElement) {
  return within(container)
    .getAllByRole("link")
    .map((link) => link.textContent?.trim());
}

const signedOut: CurrentUser = {
  memberId: "sam",
  view: "admin",
  signIn: null,
  passwordSet: false,
  offerDismissed: false,
};

function identityStub(): Pick<ApiClient, "session" | "me" | "passkeys" | "password" | "members" | "shares" | "tokens" | "connectors" | "providers"> {
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
    connectors: {
      async list() {
        return [];
      },
      async put(grant) {
        return grant;
      },
      async remove() {},
      async test() {
        return { ok: true, latency_ms: 50 };
      },
    },
    providers: {
      async list() {
        return [];
      },
      async add(input) {
        return {
          id: "prov",
          provider: input.provider,
          name: input.name,
          adapter: input.adapter,
          plan_kind: input.plan_kind,
          status: "Connected",
          models: input.models,
        };
      },
      async update(id, input) {
        return {
          id,
          provider: "openai",
          name: input.name ?? "OpenAI",
          adapter: "openai",
          plan_kind: input.plan_kind ?? "metered",
          status: "Connected",
          models: input.models ?? [],
        };
      },
      async remove() {},
      async test() {
        return { ok: true, latency_ms: 50 };
      },
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
    setWidth(1024);
  });

  it("shows a forward path on the home empty state", async () => {
    renderAt("/");
    expect(await screen.findByRole("heading", { level: 3, name: "Your first task" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Create task" }).getAttribute("href")).toBe("/tasks");
    expect(screen.getAllByRole("link")[0]?.textContent).toBe("Skip to content");
    const target = screen.getByRole("link", { name: "Skip to content" }).getAttribute("href")?.slice(1);
    expect(target).toBeTruthy();
    expect(target).not.toBe("content");
    expect(document.getElementById(target ?? "")).toBe(screen.getByRole("main"));
    expect(screen.getByRole("heading", { level: 3, name: "Your first task" }).closest(".empty-state")).toBeTruthy();
  });

  it("opens navigation from the command launcher", async () => {
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", metaKey: true, bubbles: true }));
    });
    const dialog = await screen.findByRole("dialog", { name: "Go to" });
    expect(within(dialog).getByRole("link", { name: "Tasks" }).getAttribute("href")).toBe("/tasks");
  });

  it("lists the primary items, the Workspace section and a closed Management group", async () => {
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    expect(linkLabels(sidebar())).toEqual([
      "Home",
      "Chat",
      "Tasks",
      "Automations",
      "Agents",
      "Knowledge",
      "Usage",
      "Providers",
      "Connectors",
    ]);
    const workspace = within(sidebar()).getByRole("group", { name: "Workspace" });
    expect(linkLabels(workspace)).toEqual(["Providers", "Connectors"]);
    const management = within(sidebar()).getByRole("button", { name: "Management" });
    expect(management.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("link", { name: "Members" })).toBeNull();
    expect(screen.getByText("Kodeeo Limited")).toBeTruthy();
    expect(screen.queryByRole("menu", { name: "Workspaces" })).toBeNull();
    expect(screen.getByTestId("theme-switch")).toBeTruthy();
    expect(screen.getByText("v0.1.0")).toBeTruthy();
  });

  it("opens the Management group under /admin and from its button", async () => {
    renderAt("/admin/teams");
    await screen.findByRole("heading", { level: 1, name: "Teams" });
    const management = within(sidebar()).getByRole("button", { name: "Management" });
    expect(management.getAttribute("aria-expanded")).toBe("true");
    expect(linkLabels(sidebar()).slice(-3)).toEqual(["Members", "Teams", "Roles"]);

    cleanup();
    renderAt("/tasks");
    await screen.findByRole("heading", { level: 1, name: "Tasks" });
    const closed = within(sidebar()).getByRole("button", { name: "Management" });
    expect(closed.getAttribute("aria-expanded")).toBe("false");
    act(() => {
      closed.click();
    });
    await waitFor(() => {
      expect(closed.getAttribute("aria-expanded")).toBe("true");
      expect(linkLabels(sidebar()).slice(-3)).toEqual(["Members", "Teams", "Roles"]);
    });
  });

  it("names the brand link and sends it home", async () => {
    renderAt("/tasks");
    await screen.findByRole("heading", { level: 1, name: "Tasks" });
    const brand = screen.getByRole("link", { name: "Upmo" });
    expect(brand.getAttribute("href")).toBe("/");
    expect(brand.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(within(brand).queryByRole("img")).toBeNull();
  });

  it("exposes one Main navigation landmark", async () => {
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    const landmarks = screen.getAllByRole("navigation");
    expect(landmarks.map((landmark) => landmark.getAttribute("aria-label"))).toEqual(["Main"]);
    expect(screen.getAllByRole("banner")).toHaveLength(1);
    expect(screen.getAllByRole("main")).toHaveLength(1);
  });

  it("collapses to the rail with the toggle button and the keyboard shortcut", async () => {
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    const header = screen.getByRole("banner");
    expect(rail().getAttribute("data-rail")).toBeNull();

    act(() => {
      within(header).getByRole("button", { name: "Collapse navigation" }).click();
    });
    expect(rail().getAttribute("data-rail")).toBe("true");
    expect(within(header).getByRole("button", { name: "Expand navigation" })).toBeTruthy();
    expect(screen.queryByText("Kodeeo Limited")).toBeNull();
    expect(screen.getByRole("button", { name: "Toggle colour scheme" })).toBeTruthy();
    expect(screen.queryByTestId("theme-switch")).toBeNull();
    expect(screen.getByRole("link", { name: "Home" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Management" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^User account:/ })).toBeTruthy();

    act(() => {
      within(header).getByRole("button", { name: "Expand navigation" }).click();
    });
    expect(rail().getAttribute("data-rail")).toBeNull();

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "b", metaKey: true }));
    });
    expect(rail().getAttribute("data-rail")).toBe("true");
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "B", ctrlKey: true }));
    });
    expect(rail().getAttribute("data-rail")).toBeNull();
    expect(within(header).getByRole("button", { name: "Collapse navigation" })).toBeTruthy();
  });

  it("shows the icon rail between sm and md and opens the full navigation in a drawer", async () => {
    setWidth(800);
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    expect(rail().getAttribute("data-rail")).toBe("true");
    expect(screen.queryByRole("button", { name: "Collapse navigation" })).toBeNull();
    const toggle = screen.getByRole("button", { name: "Open navigation" });

    act(() => {
      toggle.click();
    });
    const drawer = await screen.findByRole("dialog", { name: "Navigation" });
    expect(rail().hasAttribute("inert")).toBe(true);
    expect(within(drawer).getByRole("navigation", { name: "Main" })).toBeTruthy();
    expect(within(drawer).getByText("Kodeeo Limited")).toBeTruthy();
    expect(within(drawer).getByTestId("theme-switch")).toBeTruthy();
    expect(within(drawer).getByRole("button", { name: "Close navigation" })).toBeTruthy();

    act(() => {
      within(drawer).getByRole("link", { name: "Tasks" }).click();
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Tasks" })).toBeTruthy();
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Navigation" })).toBeNull();
    });
    expect(rail().hasAttribute("inert")).toBe(false);
  });

  it("has no rail on a phone, only the drawer", async () => {
    setWidth(360);
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    expect(document.querySelector(".upmo-app-frame__rail")).toBeNull();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();

    act(() => {
      screen.getByRole("button", { name: "Open navigation" }).click();
    });
    const drawer = await screen.findByRole("dialog", { name: "Navigation" });
    expect(screen.getAllByRole("navigation")).toHaveLength(1);
    expect(linkLabels(within(drawer).getByRole("navigation", { name: "Main" }))).toContain("Chat");
  });

  it("closes the drawer when the route changes without a nav click", async () => {
    setWidth(360);
    const history = createMemoryHistory({ initialEntries: ["/"] });
    render(
      <AppProviders>
        <App history={history} />
      </AppProviders>,
    );
    await screen.findByRole("heading", { level: 1, name: "Home" });

    act(() => {
      screen.getByRole("button", { name: "Open navigation" }).click();
    });
    await screen.findByRole("dialog", { name: "Navigation" });

    act(() => {
      history.push("/tasks");
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Tasks" })).toBeTruthy();
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Navigation" })).toBeNull();
    });
  });

  it("leaves the collapsed state alone when Ctrl+B is pressed below md", async () => {
    setWidth(800);
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    expect(rail().getAttribute("data-rail")).toBe("true");

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "b", ctrlKey: true }));
    });
    expect(rail().getAttribute("data-rail")).toBe("true");
    expect(screen.queryByRole("dialog", { name: "Navigation" })).toBeNull();

    setWidth(1024);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    await waitFor(() => {
      expect(rail().getAttribute("data-rail")).toBeNull();
    });
    expect(screen.getByRole("button", { name: "Collapse navigation" })).toBeTruthy();
    expect(screen.getByTestId("theme-switch")).toBeTruthy();
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

  it.each(sidebarRoutes)("%s has one h1 %s and current nav %s", async (path, title, navLabel) => {
    renderAt(path);
    const headings = await screen.findAllByRole("heading", { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0]?.textContent).toBe(title);
    const links = within(sidebar()).getAllByRole("link");
    const current = links.filter((link) => link.getAttribute("aria-current") === "page");
    expect(current.map((link) => link.textContent?.trim())).toEqual([navLabel]);
    // data-active drives the selected styling, so it must agree with aria-current.
    const selected = links.filter((link) => link.hasAttribute("data-active"));
    expect(selected.map((link) => link.textContent?.trim())).toEqual([navLabel]);
  });

  it.each(userSettingsRoutes)("%s has one h1 %s", async (path, title) => {
    renderAt(path);
    const headings = await screen.findAllByRole("heading", { level: 1 });
    expect(headings).toHaveLength(1);
    expect(headings[0]?.textContent).toBe(title);
  });

  it("opens user profile menu with Personalisation, Security, API tokens, and Log out", async () => {
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    const userCard = screen.getByRole("button", { name: /^User account:/ });
    expect(userCard).toBeTruthy();
    act(() => {
      userCard.click();
    });
    const items = await screen.findAllByRole("menuitem");
    expect(items.map((i) => i.textContent?.trim())).toEqual([
      "Personalisation",
      "Security",
      "API tokens",
      "Log out",
    ]);
  });

  function adminClient(): ApiClient {
    const view = "admin";
    const stub = createStubApi();
    return {
      ...stub,
      session: {
        async get() {
          return { ...signedOut, view };
        },
      },
      me: {
        ...stub.me,
        async current() {
          return { ...signedOut, view };
        },
      },
    };
  }

  it("lists a closed Management group for an admin at /", async () => {
    renderAt("/", adminClient());
    await screen.findByRole("heading", { level: 1, name: "Home" });
    const management = within(sidebar()).getByRole("button", { name: "Management" });
    expect(management.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("link", { name: "Members" })).toBeNull();
  });

  it("opens the Management group for an admin at /admin/roles", async () => {
    renderAt("/admin/roles", adminClient());
    await screen.findByRole("heading", { level: 1, name: "Roles" });
    const management = within(sidebar()).getByRole("button", { name: "Management" });
    expect(management.getAttribute("aria-expanded")).toBe("true");
    expect(linkLabels(sidebar())).toEqual(expect.arrayContaining(["Members", "Teams", "Roles"]));
  });

  it.each(["member", "viewer"] as const)("shows Connectors but hides Management from a %s", async (view) => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      session: {
        async get() {
          return { ...signedOut, view };
        },
      },
      me: {
        ...stub.me,
        async current() {
          return { ...signedOut, view };
        },
      },
    };
    renderAt("/", client);
    await screen.findByRole("heading", { level: 1, name: "Home" });
    expect(linkLabels(sidebar())).toContain("Connectors");
    expect(within(sidebar()).queryByText("Management")).toBeNull();
    expect(within(sidebar()).queryByRole("button", { name: "Management" })).toBeNull();
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
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
    expect(sessionFetches).toBe(1);

    cleanup();

    renderAt("/login", client);
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
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
      screen.getByRole("button", { name: /^User account:/ }).click();
    });
    const logoutItem = await screen.findByRole("menuitem", { name: "Log out" });
    act(() => {
      logoutItem.click();
    });
    expect(await screen.findByRole("button", { name: /^Log in$/ })).toBeTruthy();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
    expect(beforeCalls).toBeGreaterThan(0);
  });

  it("/api/session is fetched once per shell load", async () => {
    let sessionFetches = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<(input: RequestInfo | URL) => Promise<Response>>(async (input) => {
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

  it("redirects legacy /settings paths to new top-level paths", async () => {
    renderAt("/settings");
    expect(await screen.findByRole("heading", { level: 1, name: "Security" })).toBeTruthy();

    cleanup();
    renderAt("/settings/connectors");
    expect(await screen.findByRole("heading", { level: 1, name: "Connectors" })).toBeTruthy();

    cleanup();
    renderAt("/settings/providers");
    expect(await screen.findByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();

    cleanup();
    renderAt("/personalization");
    expect(await screen.findByRole("heading", { level: 1, name: "Personalisation" })).toBeTruthy();

    cleanup();
    renderAt("/settings/personalization");
    expect(await screen.findByRole("heading", { level: 1, name: "Personalisation" })).toBeTruthy();

    cleanup();
    renderAt("/settings/tokens");
    expect(await screen.findByRole("heading", { level: 1, name: "API tokens" })).toBeTruthy();
  });
});
