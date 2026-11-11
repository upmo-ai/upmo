import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createBrainApi } from "../../app/brain-api";
import { AppProviders } from "../../theme";

type Call = { method: string; path: string; body: Record<string, unknown> | undefined };

// A provider account as the brain sends it.
type Row = {
  id: string;
  provider: string;
  name: string;
  adapter: string;
  plan_kind: string;
  base_url?: string;
  status: string;
  models: string[];
  usable: boolean;
  usable_models: string[];
  scope: "own" | "global";
};

const secret = "sk-live-secret-9d41f7";

const claude: Row = {
  id: "p1",
  provider: "anthropic",
  name: "Claude Team",
  adapter: "openai_compat",
  plan_kind: "metered",
  base_url: "https://api.anthropic.com",
  status: "Connected",
  models: ["claude-3-7-sonnet"],
  usable: true,
  usable_models: ["claude-3-7-sonnet"],
  scope: "own",
};
const local: Row = {
  id: "p2",
  provider: "ollama",
  name: "Office Ollama",
  adapter: "openai_compat",
  plan_kind: "local",
  base_url: "http://ollama.office:11434",
  status: "Key rejected",
  models: ["llama3.3:70b"],
  usable: false,
  usable_models: [],
  scope: "own",
};
const shared: Row = {
  id: "g1",
  provider: "openai",
  name: "Workspace OpenAI",
  adapter: "openai_compat",
  plan_kind: "metered",
  status: "Offline",
  models: ["gpt-4o"],
  usable: false,
  usable_models: [],
  scope: "global",
};

/**
 * A brain that keeps provider accounts in memory and answers the provider
 * routes the way the real ones do. A refusal is a status with {"error": ...}.
 * Every request is recorded, with its credentials mode checked.
 */
function brain(
  options: {
    accounts?: Row[];
    tasks?: () => Response;
    providers?: () => Response;
    refuse?: (call: Call) => Response | undefined;
    testReply?: () => Response;
  } = {},
) {
  const accounts = [...(options.accounts ?? [])];
  const calls: Call[] = [];
  let serial = 0;

  const fetcher = vi.fn<typeof fetch>(async (input, init) => {
    const path = String(input);
    const method = init?.method ?? "GET";
    const body =
      typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
    const call = { method, path, body };
    calls.push(call);
    expect(init?.credentials).toBe("include");

    if (path === "/api/tasks" && method === "GET") return options.tasks?.() ?? Response.json([]);
    if (!path.startsWith("/api/providers")) return new Response(null, { status: 404 });
    if (method !== "GET") {
      const refusal = options.refuse?.(call);
      if (refusal) return refusal;
    }
    if (path === "/api/providers" && method === "GET") {
      return options.providers?.() ?? Response.json(accounts);
    }
    if (path === "/api/providers/test" && method === "POST") {
      return options.testReply?.() ?? Response.json({ ok: true });
    }
    if (path === "/api/providers" && method === "POST") {
      const sent = body as Record<string, unknown>;
      if (accounts.some((account) => account.name === sent.name)) {
        return Response.json({ error: "You already have a provider with that name." }, { status: 409 });
      }
      serial += 1;
      const made: Row = {
        id: `made-${serial}`,
        provider: String(sent.provider),
        name: String(sent.name),
        adapter: String(sent.adapter),
        plan_kind: String(sent.plan_kind),
        ...(typeof sent.base_url === "string" ? { base_url: sent.base_url } : {}),
        status: "Not connected",
        models: sent.models as string[],
        usable: false,
        usable_models: [],
        scope: "own",
      };
      accounts.push(made);
      return Response.json(made, { status: 201 });
    }
    const target = accounts.find((account) => path === `/api/providers/${account.id}`);
    if (target && method === "PUT") {
      // The brain keeps the key sealed and answers without it.
      const changes = { ...body };
      delete changes.api_key;
      Object.assign(target, changes);
      return Response.json(target);
    }
    if (target && method === "DELETE") {
      accounts.splice(accounts.indexOf(target), 1);
      return new Response(null, { status: 204 });
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

// The ids of the provider cards on the page, in the order shown.
function cardIds() {
  return screen.getAllByTestId(/^provider-card-/).map((card) => card.dataset.testid);
}

function writes(calls: Call[], method: string) {
  return calls.filter((call) => call.method === method && call.path.startsWith("/api/providers"));
}

// Walks the wizard for OpenAI with a typed key and stops on the Review step.
async function openReviewWithKey() {
  const history = renderAt("/providers/new");
  await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
  clickButton("OpenAI");
  await continueTo(/OpenAI Credentials & Plan/);
  fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: secret } });
  await continueTo(/Models & Pricing for OpenAI/);
  await continueTo("Review Configuration");
  return history;
}

async function openReviewOfEdit(id: string, name: RegExp) {
  renderAt(`/providers/${id}/edit`);
  expect(await screen.findByText(name)).toBeTruthy();
  await continueTo(/Models & Pricing for/);
  await continueTo("Review Configuration");
}

describe("providers from the brain", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists the person's own accounts first, then the shared one read only, with the status the brain stores", async () => {
    const { fetcher, calls } = brain({ accounts: [claude, local, shared] });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/providers");

    await screen.findByTestId("provider-card-p1");
    expect(cardIds()).toEqual(["provider-card-p1", "provider-card-p2", "provider-card-g1"]);

    const own = screen.getByTestId("provider-card-p1");
    expect(within(own).getByText("Connected")).toBeTruthy();
    expect(within(own).getByText("OpenAI-compatible")).toBeTruthy();
    expect(within(own).getByRole("button", { name: "Edit" })).toBeTruthy();
    expect(within(own).getByRole("button", { name: "Disconnect" })).toBeTruthy();
    expect(within(screen.getByTestId("provider-card-p2")).getByText("Key rejected")).toBeTruthy();

    const global = screen.getByTestId("provider-card-g1");
    expect(within(global).getByText("Offline")).toBeTruthy();
    expect(within(global).queryByRole("button", { name: "Edit" })).toBeNull();
    expect(within(global).queryByRole("button", { name: "Disconnect" })).toBeNull();
    expect(screen.getByText("Shared")).toBeTruthy();
    expect(screen.getByText("Managed by your workspace. Read only.")).toBeTruthy();

    // One read of the list when the page opens: it is not seeded from a snapshot.
    expect(calls.filter((call) => call.method === "GET" && call.path === "/api/providers")).toHaveLength(1);
  });

  it("opens a shared account's address as a read only page", async () => {
    vi.stubGlobal("fetch", brain({ accounts: [shared] }).fetcher);
    renderAt("/providers/g1/edit");

    expect(await screen.findByText("This provider is shared by the workspace. It is read only here.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });

  it("adds an account with the body the brain expects, then never shows or sends the key again", async () => {
    const { fetcher, calls } = brain({ accounts: [claude] });
    vi.stubGlobal("fetch", fetcher);
    await openReviewWithKey();

    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Connect provider");
    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
    });
    await screen.findByTestId("provider-card-made-1");

    expect(writes(calls, "POST").map((call) => [call.path, call.body])).toEqual([
      [
        "/api/providers/test",
        {
          provider: "openai",
          adapter: "openai_compat",
          plan_kind: "metered",
          base_url: "https://api.openai.com",
          api_key: secret,
          model: "gpt-4o",
        },
      ],
      [
        "/api/providers",
        {
          provider: "openai",
          name: "OpenAI",
          adapter: "openai_compat",
          plan_kind: "metered",
          base_url: "https://api.openai.com",
          api_key: secret,
          models: ["gpt-4o", "o3-mini"],
        },
      ],
    ]);

    // The new account is in the list as the brain stores it, with no key anywhere.
    expect(within(screen.getByTestId("provider-card-made-1")).getByText("Not connected")).toBeTruthy();
    expect(document.body.innerHTML).not.toContain(secret);
    expect(JSON.stringify({ ...localStorage })).not.toContain(secret);
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(secret);
    const postSave = calls.slice(calls.findIndex((call) => call.method === "POST" && call.path === "/api/providers") + 1);
    expect(postSave.length).toBeGreaterThan(0);
    expect(JSON.stringify(postSave)).not.toContain(secret);
  });

  it("starts every hosted vendor on its own address and lets a typed address replace it", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderAt("/providers/new");
    await screen.findByRole("heading", { level: 1, name: "Connect a provider" });

    clickButton("OpenAI");
    await continueTo(/OpenAI Credentials & Plan/);
    expect(screen.getByLabelText(/Base URL/)).toHaveProperty("value", "https://api.openai.com");

    clickButton("Back");
    clickButton("DeepSeek");
    await continueTo(/DeepSeek Credentials & Plan/);
    const baseUrl = screen.getByLabelText(/Base URL/);
    expect(baseUrl).toHaveProperty("value", "https://api.deepseek.com");
    fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: secret } });
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);

    fireEvent.change(baseUrl, { target: { value: "   " } });
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);

    fireEvent.change(baseUrl, { target: { value: "https://api.deepseek.com" } });
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);
  });

  it("asks for a key on the metered plan of a local provider and holds Continue until one is typed", async () => {
    vi.stubGlobal("fetch", brain().fetcher);
    renderAt("/providers/new");
    await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
    clickButton("Ollama");
    await continueTo(/Ollama Credentials & Plan/);

    // On its own local plan Ollama takes no key.
    expect(screen.queryByLabelText(/^API Key/)).toBeNull();
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);

    const plans = screen.getByRole("listbox", { name: "Plan kind", hidden: true });
    click(within(plans).getByRole("option", { name: "Metered (Pay-as-you-go)", hidden: true }));

    const key = await screen.findByLabelText(/^API Key/);
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(true);
    fireEvent.change(key, { target: { value: secret } });
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);
  });

  it("does not give a saved account without an address the first vendor's default address on edit", async () => {
    const bare: Row = {
      id: "f1",
      provider: "fake",
      name: "Bare fake",
      adapter: "fake",
      plan_kind: "local",
      status: "Connected",
      models: ["alpha"],
      usable: true,
      usable_models: ["alpha"],
      scope: "own",
    };
    const { fetcher, calls } = brain({ accounts: [bare] });
    vi.stubGlobal("fetch", fetcher);
    await openReviewOfEdit("f1", /Test provider Credentials & Plan/);

    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Save changes");
    await waitFor(() => {
      expect(writes(calls, "PUT")).toHaveLength(1);
    });
    expect(writes(calls, "PUT")[0]?.body).toEqual({
      name: "Bare fake",
      plan_kind: "local",
      base_url: "",
      models: ["alpha"],
    });
  });

  it("sends an emptied address as an explicit clear when a saved account is edited", async () => {
    const pointed: Row = {
      id: "f2",
      provider: "fake",
      name: "Pointed fake",
      adapter: "fake",
      plan_kind: "local",
      base_url: "http://127.0.0.1:4000",
      status: "Connected",
      models: ["alpha"],
      usable: true,
      usable_models: ["alpha"],
      scope: "own",
    };
    const { fetcher, calls } = brain({ accounts: [pointed] });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/providers/f2/edit");
    expect(await screen.findByText(/Test provider Credentials & Plan/)).toBeTruthy();

    const baseUrl = screen.getByLabelText(/Base URL/);
    expect(baseUrl).toHaveProperty("value", "http://127.0.0.1:4000");
    fireEvent.change(baseUrl, { target: { value: "" } });
    await continueTo(/Models & Pricing for/);
    await continueTo("Review Configuration");
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Save changes");
    await waitFor(() => {
      expect(writes(calls, "PUT")).toHaveLength(1);
    });
    // "" is the clear; leaving the field out would keep the saved address.
    expect(writes(calls, "PUT")[0]?.body).toEqual({
      name: "Pointed fake",
      plan_kind: "local",
      base_url: "",
      models: ["alpha"],
    });
  });

  it("adds the built in test provider on the local plan with no key", async () => {
    const { fetcher, calls } = brain();
    vi.stubGlobal("fetch", fetcher);
    renderAt("/providers/new");
    await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
    clickButton("Test provider");
    await continueTo(/Test provider Credentials & Plan/);
    // A local account may go without a key.
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(false);
    await continueTo(/Models & Pricing for Test provider/);
    expect(isDisabled(screen.getByRole("button", { name: "Continue" }))).toBe(true);
    const input = screen.getByLabelText(/Add Custom Model Identifier/);
    for (const model of ["alpha", "beta"]) {
      fireEvent.change(input, { target: { value: model } });
      clickButton("Add");
    }
    await continueTo("Review Configuration");
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Connect provider");
    await screen.findByTestId("provider-card-made-1");

    expect(writes(calls, "POST").find((call) => call.path === "/api/providers")?.body).toEqual({
      provider: "fake",
      name: "Test provider",
      adapter: "fake",
      plan_kind: "local",
      models: ["alpha", "beta"],
    });
    expect(within(screen.getByTestId("provider-card-made-1")).getByText("Test adapter")).toBeTruthy();
  });

  it("does not prefill the key on edit, tests the saved key by id and saves without an api_key", async () => {
    const { fetcher, calls } = brain({ accounts: [claude] });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/providers/p1/edit");
    expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
    expect(screen.getByLabelText(/^API Key/)).toHaveProperty("value", "");
    await continueTo(/Models & Pricing for Anthropic/);
    await continueTo("Review Configuration");

    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Save changes");
    await waitFor(() => {
      expect(writes(calls, "PUT")).toHaveLength(1);
    });

    expect(writes(calls, "POST")[0]?.body).toEqual({
      provider: "anthropic",
      adapter: "anthropic",
      plan_kind: "metered",
      base_url: "https://api.anthropic.com",
      model: "claude-3-7-sonnet",
      id: "p1",
    });
    const put = writes(calls, "PUT")[0];
    expect(put?.path).toBe("/api/providers/p1");
    expect(put?.body).toEqual({
      name: "Claude Team",
      plan_kind: "metered",
      base_url: "https://api.anthropic.com",
      models: ["claude-3-7-sonnet"],
    });
  });

  it("tests a newly typed key by value and not by id", async () => {
    const { fetcher, calls } = brain({ accounts: [claude] });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/providers/p1/edit");
    expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: secret } });
    await continueTo(/Models & Pricing for Anthropic/);
    await continueTo("Review Configuration");
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();

    const probe = writes(calls, "POST")[0]?.body;
    expect(probe?.api_key).toBe(secret);
    expect(probe).not.toHaveProperty("id");
    expect(probe?.plan_kind).toBe("metered");
  });

  it("shows the sentence of a failed test and of a throttled one", async () => {
    let reply = () => Response.json({ ok: false, error: "The key was rejected." });
    vi.stubGlobal("fetch", brain({ accounts: [claude], testReply: () => reply() }).fetcher);
    await openReviewOfEdit("p1", /Anthropic Credentials & Plan/);

    clickButton("Test connection");
    expect(await screen.findByText("Connection failed")).toBeTruthy();
    expect(screen.getByText("The key was rejected.")).toBeTruthy();
    expect(isDisabled(screen.getByRole("button", { name: "Save changes" }))).toBe(true);

    reply = () => Response.json({ error: "Too many tests. Try again in a minute." }, { status: 429 });
    clickButton("Test connection");
    expect(await screen.findByText("Too many tests. Try again in a minute.")).toBeTruthy();
  });

  it("shows a 422 sentence and a duplicate name in the form and keeps the form", async () => {
    const { fetcher, calls } = brain({
      accounts: [claude],
      refuse: (call) =>
        call.path === "/api/providers" && call.body?.name === "Bad name"
          ? Response.json({ error: "The name must be 1 to 60 characters." }, { status: 422 })
          : undefined,
    });
    vi.stubGlobal("fetch", fetcher);
    const history = await openReviewWithKey();

    // A name the brain refuses.
    clickButton("Back");
    clickButton("Back");
    fireEvent.change(await screen.findByLabelText(/Provider display name/), { target: { value: "Bad name" } });
    await continueTo(/Models & Pricing for OpenAI/);
    await continueTo("Review Configuration");
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Connect provider");
    expect(await screen.findByText("The name must be 1 to 60 characters.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect provider" })).toBeTruthy();
    expect(history.location.pathname).toBe("/providers/new");

    // A name already used.
    clickButton("Back");
    clickButton("Back");
    fireEvent.change(await screen.findByLabelText(/Provider display name/), { target: { value: "Claude Team" } });
    await continueTo(/Models & Pricing for OpenAI/);
    await continueTo("Review Configuration");
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Connect provider");
    expect(await screen.findByText("You already have a provider with that name.")).toBeTruthy();
    expect(screen.queryByText("The name must be 1 to 60 characters.")).toBeNull();
    expect(history.location.pathname).toBe("/providers/new");
    expect(writes(calls, "POST").filter((call) => call.path === "/api/providers")).toHaveLength(2);
  });

  it("says saving keys is turned off on a 503, in its own words, and keeps the form", async () => {
    const { fetcher } = brain({
      refuse: (call) =>
        call.path === "/api/providers"
          ? Response.json({ error: "Saving provider keys is not set up on this server." }, { status: 503 })
          : undefined,
    });
    vi.stubGlobal("fetch", fetcher);
    const history = await openReviewWithKey();
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Connect provider");

    expect(await screen.findByText("Saving keys is turned off on this server.")).toBeTruthy();
    expect(screen.queryByText(/not set up/)).toBeNull();
    expect(screen.getByRole("button", { name: "Connect provider" })).toBeTruthy();
    expect(history.location.pathname).toBe("/providers/new");
    expect(document.body.textContent).not.toContain(secret);
  });

  it("says nothing the brain did not write for the person when a save fails some other way", async () => {
    const { fetcher } = brain({
      refuse: (call) =>
        call.path === "/api/providers" ? Response.json({ error: `providers ${secret}` }, { status: 500 }) : undefined,
    });
    vi.stubGlobal("fetch", fetcher);
    await openReviewWithKey();
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    clickButton("Connect provider");

    expect(await screen.findByText("Something went wrong. Try again.")).toBeTruthy();
    expect(document.body.textContent).not.toContain(secret);
  });

  it("removes an account and says which routes use one that is in use", async () => {
    const { fetcher, calls } = brain({
      accounts: [claude, local],
      refuse: (call) =>
        call.method === "DELETE" && call.path === "/api/providers/p1"
          ? Response.json({ error: "Used by route chat, agent-dispatch" }, { status: 409 })
          : undefined,
    });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/providers");

    click(within(await screen.findByTestId("provider-card-p1")).getByRole("button", { name: "Disconnect" }));
    let dialog = await screen.findByRole("dialog");
    click(within(dialog).getByRole("button", { name: "Disconnect provider" }));
    expect(await within(dialog).findByText("Used by route chat, agent-dispatch")).toBeTruthy();
    expect(screen.getByTestId("provider-card-p1")).toBeTruthy();

    // Cancelled, then another account goes.
    click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    click(within(screen.getByTestId("provider-card-p2")).getByRole("button", { name: "Disconnect" }));
    dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText("Used by route chat, agent-dispatch")).toBeNull();
    click(within(dialog).getByRole("button", { name: "Disconnect provider" }));
    await waitFor(() => {
      expect(screen.queryByTestId("provider-card-p2")).toBeNull();
    });
    expect(writes(calls, "DELETE").map((call) => call.path)).toEqual(["/api/providers/p1", "/api/providers/p2"]);
  });

  it("does not hide a 401 on the provider list behind the sample accounts", async () => {
    vi.stubGlobal(
      "fetch",
      brain({ providers: () => Response.json({ error: "unauthorised" }, { status: 401 }) }).fetcher,
    );
    const history = renderAt("/providers");

    await waitFor(() => {
      expect(history.location.pathname).toBe("/login");
    });
    expect(screen.queryByText("Anthropic Claude")).toBeNull();
  });

  it("does not hide a 401 from the brain's first answer behind the sample accounts", async () => {
    vi.stubGlobal(
      "fetch",
      brain({ tasks: () => Response.json({ error: "unauthorised" }, { status: 401 }) }).fetcher,
    );
    const history = renderAt("/providers");

    await waitFor(() => {
      expect(history.location.pathname).toBe("/login");
    });
    expect(screen.queryByText("Anthropic Claude")).toBeNull();
  });

  it("says the list could not be loaded on a server error and shows no empty state", async () => {
    vi.stubGlobal("fetch", brain({ providers: () => new Response(null, { status: 500 }) }).fetcher);
    renderAt("/providers");

    expect(await screen.findByText("Providers could not be loaded")).toBeTruthy();
    expect(screen.queryByRole("heading", { level: 3, name: "Connect a provider" })).toBeNull();
  });

  it("uses the sample accounts for the whole session when no brain is there", async () => {
    const { fetcher, calls } = brain({ tasks: () => new Response(null, { status: 404 }) });
    vi.stubGlobal("fetch", fetcher);
    renderAt("/providers");

    expect(await screen.findByTestId("provider-card-provider-anthropic")).toBeTruthy();
    expect(screen.getByTestId("provider-card-provider-ollama")).toBeTruthy();
    expect(calls.filter((call) => call.path.startsWith("/api/providers"))).toEqual([]);
  });
});

function isDisabled(element: HTMLElement) {
  return element.getAttribute("disabled") !== null;
}
