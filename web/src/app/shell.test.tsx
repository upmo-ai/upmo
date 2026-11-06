import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../App";
import { AppProviders } from "../theme";
import {
  BrainError,
  createStubApi,
  type ApiClient,
  type ApiToken,
  type AppNotification,
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

// With another approval left, focus moves to its button instead of the body.
// jsdom reports the popover dropdown as display: none, which makes focus() a
// no-op there, so the check is on which element was asked to take focus.
async function expectFocusOnRemainingApprove(dialog: HTMLElement, focus: { mock: { contexts: unknown[] } }) {
  await waitFor(() => {
    const remaining = within(dialog).getByRole("button", { name: "Approve", hidden: true });
    expect(focus.mock.contexts.at(-1)).toBe(remaining);
  });
}

// The bell's live region holds the empty-state line for a moment after the
// last approval.
function announced() {
  return document.querySelector(".shell-bell [role='status']")?.textContent ?? "";
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

function identityStub(): Pick<ApiClient, "session" | "me" | "passkeys" | "password" | "members" | "shares" | "tokens" | "connectors" | "providers" | "knowledge" | "createTask" | "postTaskMessage" | "gateway" | "decisions"> {
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
    async createTask() {
      throw new Error("not used");
    },
    async postTaskMessage() {
      throw new Error("not used");
    },
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
    knowledge: createStubApi().knowledge,
    gateway: {
      async routes() {
        return [];
      },
    },
    decisions: {
      async gates() {
        return [];
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
    vi.restoreAllMocks();
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
    const empty = document.querySelector(".upmo-empty-state--first-run") as HTMLElement | null;
    expect(empty).not.toBeNull();
    expect(within(empty as HTMLElement).getByRole("heading", { level: 3, name: "Your first task" })).toBeTruthy();
    expect(within(empty as HTMLElement).getByRole("link", { name: "Create task" })).toBeTruthy();
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
      "Models and routes",
    ]);
    const workspace = within(sidebar()).getByRole("group", { name: "Workspace" });
    expect(linkLabels(workspace)).toEqual(["Providers", "Connectors", "Models and routes"]);
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

  it("UPM-196 toggles the navigation drawer with Ctrl+B below md and leaves the rail state alone", async () => {
    setWidth(800);
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    expect(rail().getAttribute("data-rail")).toBe("true");

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "b", ctrlKey: true }));
    });
    await screen.findByRole("dialog", { name: "Navigation" });
    expect(screen.getByRole("button", { name: "Open navigation" }).getAttribute("aria-expanded")).toBe("true");
    expect(rail().getAttribute("data-rail")).toBe("true");

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "b", ctrlKey: true }));
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Navigation" })).toBeNull();
    });
    expect(rail().getAttribute("data-rail")).toBe("true");

    setWidth(1024);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    await waitFor(() => {
      expect(rail().getAttribute("data-rail")).toBeNull();
    });
    expect(screen.getByRole("button", { name: "Collapse navigation" })).toBeTruthy();
    expect(screen.getByTestId("theme-switch")).toBeTruthy();

    // From md the same shortcut collapses the rail and opens no drawer.
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "b", ctrlKey: true }));
    });
    expect(rail().getAttribute("data-rail")).toBe("true");
    expect(screen.queryByRole("dialog", { name: "Navigation" })).toBeNull();
  });

  it("UPM-196 does not bring the drawer back when the viewport widens and narrows again", async () => {
    setWidth(800);
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    act(() => {
      screen.getByRole("button", { name: "Open navigation" }).click();
    });
    await screen.findByRole("dialog", { name: "Navigation" });

    setWidth(1024);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Navigation" })).toBeNull();
    });
    expect(rail().hasAttribute("inert")).toBe(false);

    setWidth(800);
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Open navigation" })).toBeTruthy();
    });
    expect(screen.queryByRole("dialog", { name: "Navigation" })).toBeNull();
    expect(screen.getByRole("button", { name: "Open navigation" }).getAttribute("aria-expanded")).toBe("false");
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
      chats: createStubApi().chats,
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
    const list = await screen.findByRole("list", { name: "Notifications", hidden: true });
    const items = within(list).getAllByRole("listitem", { hidden: true });
    // Every listed notification is counted as unread, so each one says so.
    expect(items.map((item) => item.textContent)).toEqual([
      "Unread: Approve the digestApprove",
      "Unread: Question about the budgetApprove",
      "Unread: Mail triage done",
      "Unread: Weekly close failed",
    ]);
    expect(within(items[3]!).queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("removes an approval and drops the unread count", async () => {
    renderAt("/");
    const bell = await screen.findByRole("button", { name: "Notifications, 4 unread" });
    act(() => {
      bell.click();
    });
    const [approval] = within(await screen.findByRole("list", { name: "Notifications", hidden: true })).getAllByRole(
      "listitem",
      { hidden: true },
    );
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
      chats: createStubApi().chats,
    });
    const bell = await screen.findByRole("button", { name: /^Notifications$/ });
    act(() => {
      bell.click();
    });
    expect(await screen.findByRole("heading", { level: 3, name: "You are up to date" })).toBeTruthy();
    expect(screen.getByText("Approvals and finished tasks show up here.")).toBeTruthy();
  });

  it("UPM-196 shows a spinner and holds back the empty state while the notifications load", async () => {
    const stub = createStubApi();
    let release: (items: AppNotification[]) => void = () => {};
    const client: ApiClient = {
      ...stub,
      listNotifications: () =>
        new Promise<AppNotification[]>((resolve) => {
          release = resolve;
        }),
      approvals: {
        ...stub.approvals,
        async list() {
          return [];
        },
      },
    };
    renderAt("/", client);
    const bell = await screen.findByRole("button", { name: /^Notifications$/ });
    act(() => {
      bell.click();
    });
    const dialog = await screen.findByRole("dialog", { name: /^Notifications/ });
    expect(dialog.getAttribute("aria-busy")).toBe("true");
    expect(within(dialog).getByRole("img", { name: "Loading", hidden: true })).toBeTruthy();
    expect(within(dialog).queryByRole("heading", { level: 3, hidden: true })).toBeNull();

    await act(async () => {
      release([]);
    });
    expect(
      await within(dialog).findByRole("heading", { level: 3, name: "You are up to date", hidden: true }),
    ).toBeTruthy();
    expect(dialog.getAttribute("aria-busy")).toBeNull();
    expect(within(dialog).queryByRole("img", { name: "Loading", hidden: true })).toBeNull();
  });

  it("shows a spinner while the approvals load, even once the notifications have", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      async listNotifications() {
        return [];
      },
      approvals: {
        ...stub.approvals,
        list: () => new Promise(() => {}),
      },
    };
    renderAt("/", client);
    const bell = await screen.findByRole("button", { name: /^Notifications$/ });
    act(() => {
      bell.click();
    });
    const dialog = await screen.findByRole("dialog", { name: /^Notifications/ });
    // Give the notifications time to settle: only the approvals are pending.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    expect(dialog.getAttribute("aria-busy")).toBe("true");
    expect(within(dialog).getByRole("img", { name: "Loading", hidden: true })).toBeTruthy();
    expect(within(dialog).queryByRole("heading", { level: 3, hidden: true })).toBeNull();
  });

  it("UPM-196 does not claim the user is up to date when the notifications fail to load", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      async listNotifications() {
        throw new Error("notifications unavailable");
      },
    };
    renderAt("/", client);
    const bell = await screen.findByRole("button", { name: /^Notifications$/ });
    act(() => {
      bell.click();
    });
    const dialog = await screen.findByRole("dialog", { name: /^Notifications/ });
    await waitFor(() => {
      expect(dialog.getAttribute("aria-busy")).toBeNull();
    });
    expect(within(dialog).queryByRole("heading", { level: 3, hidden: true })).toBeNull();
    expect(within(dialog).queryByRole("img", { name: "Loading", hidden: true })).toBeNull();
    expect(within(dialog).queryByRole("list", { hidden: true })).toBeNull();
  });

  it("shows the empty state, a plain label and no badge after the last approval", async () => {
    const stub = createStubApi();
    let notes: AppNotification[] = [{ id: "note-question", kind: "question", title: "Which budget?" }];
    let pending = true;
    const client: ApiClient = {
      ...stub,
      async listNotifications() {
        return notes;
      },
      async approveNotification(id) {
        notes = notes.filter((item) => item.id !== id);
      },
      approvals: {
        ...stub.approvals,
        async list() {
          return [
            {
              id: "approval-digest",
              taskId: "task-digest",
              title: "Approve the plan",
              action: "Send",
              state: pending ? "pending" : "approved",
            },
          ];
        },
        async approve(id) {
          pending = false;
          return { id, taskId: "task-digest", title: "Approve the plan", action: "Send", state: "approved" };
        },
      },
    };
    renderAt("/", client);
    const bell = await screen.findByRole("button", { name: "Notifications, 2 unread" });
    // The badge is hidden from assistive tech, so its text is the count itself.
    expect(bell.textContent).toBe("2");
    act(() => {
      bell.click();
    });
    const dialog = await screen.findByRole("dialog", { name: /^Notifications/ });

    for (const title of ["Approve the plan", "Which budget?"]) {
      const row = within(dialog)
        .getAllByRole("listitem", { hidden: true })
        .find((item) => item.textContent?.includes(title));
      if (!row) throw new Error(`no row for ${title}`);
      const approve = within(row).getByRole("button", { name: "Approve", hidden: true });
      act(() => {
        approve.focus();
        approve.click();
      });
      await waitFor(() => {
        expect(screen.queryByText(title)).toBeNull();
      });
    }

    // Nothing is left to approve, so the popover closes and the empty state is
    // said through the status line instead.
    await waitFor(() => {
      expect(announced()).toContain("You are up to date");
    });
    const plain = screen.getByRole("button", { name: /^Notifications$/ });
    expect(plain.textContent).toBe("");
    expect(screen.queryByRole("button", { name: /unread/ })).toBeNull();
    expect(screen.queryByRole("list", { name: "Notifications", hidden: true })).toBeNull();
  });

  it("returns focus to the bell and announces the empty state after the last approval", async () => {
    const stub = createStubApi();
    let notes: AppNotification[] = [{ id: "note-question", kind: "question", title: "Which budget?" }];
    const client: ApiClient = {
      ...stub,
      async listNotifications() {
        return notes;
      },
      async approveNotification(id) {
        notes = notes.filter((item) => item.id !== id);
      },
      approvals: {
        ...stub.approvals,
        async list() {
          return [];
        },
      },
    };
    renderAt("/", client);
    const bell = await screen.findByRole("button", { name: "Notifications, 1 unread" });
    act(() => {
      bell.click();
    });
    const dialog = await screen.findByRole("dialog", { name: /^Notifications/ });
    const approve = within(dialog).getByRole("button", { name: "Approve", hidden: true });
    act(() => {
      approve.focus();
      approve.click();
    });

    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByRole("button", { name: /^Notifications$/ }));
    });
    await waitFor(() => {
      expect(announced()).toContain("You are up to date");
    });
    // The line does not linger in the reading order.
    await waitFor(
      () => {
        expect(announced()).toBe("");
      },
      { timeout: 4000 },
    );
  }, 10_000);

  it("returns focus to the bell when only finished tasks are left", async () => {
    renderAt("/");
    const bell = await screen.findByRole("button", { name: "Notifications, 4 unread" });
    act(() => {
      bell.click();
    });
    const dialog = await screen.findByRole("dialog", { name: /^Notifications/ });
    const focus = vi.spyOn(HTMLElement.prototype, "focus");
    for (const title of ["Approve the digest", "Question about the budget"]) {
      const row = within(dialog)
        .getAllByRole("listitem", { hidden: true })
        .find((item) => item.textContent?.includes(title));
      if (!row) throw new Error(`no row for ${title}`);
      const approve = within(row).getByRole("button", { name: "Approve", hidden: true });
      act(() => {
        approve.focus();
        approve.click();
      });
      await waitFor(() => {
        expect(screen.queryByText(title)).toBeNull();
      });
      if (title === "Approve the digest") await expectFocusOnRemainingApprove(dialog, focus);
    }

    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "Notifications, 2 unread" }));
    });
    expect(announced()).toBe("");

    // The popover traps focus while it is open and swallows Tab when nothing
    // inside can take it. With focus back on the bell it must be closed, so Tab
    // and Shift+Tab are left to the browser.
    const settled = screen.getByRole("button", { name: "Notifications, 2 unread" });
    await waitFor(() => {
      expect(settled.getAttribute("aria-expanded")).toBe("false");
    });
    expect(fireEvent.keyDown(settled, { key: "Tab" })).toBe(true);
    expect(fireEvent.keyDown(settled, { key: "Tab", shiftKey: true })).toBe(true);
  });

  it("keeps the popover open while an approval is left", async () => {
    renderAt("/");
    const bell = await screen.findByRole("button", { name: "Notifications, 4 unread" });
    act(() => {
      bell.click();
    });
    const dialog = await screen.findByRole("dialog", { name: /^Notifications/ });
    const [first] = within(dialog).getAllByRole("button", { name: "Approve", hidden: true });
    act(() => {
      first!.focus();
      first!.click();
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Notifications, 3 unread" })).toBeTruthy();
    });
    expect(screen.getByRole("button", { name: "Notifications, 3 unread" }).getAttribute("aria-expanded")).toBe("true");
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
