import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";

function renderAgents() {
  const history = createMemoryHistory({ initialEntries: ["/agents"] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

type Row = {
  id: string;
  name: string;
  description: string;
  prompt_overlay: string;
  route: string;
  model_provider: string;
  model_id: string;
  enabled: boolean;
  usable: boolean;
  scope: "own" | "global";
  editable: boolean;
};

const mine: Row = {
  id: "a1",
  name: "Writer",
  description: "Drafts and edits",
  prompt_overlay: "Write plainly.",
  route: "fast",
  model_provider: "OpenAI",
  model_id: "gpt-x",
  enabled: true,
  usable: true,
  scope: "own",
  editable: true,
};
const house: Row = {
  id: "g1",
  name: "House style",
  description: "The workspace way",
  prompt_overlay: "Follow the house style.",
  route: "",
  model_provider: "",
  model_id: "",
  enabled: true,
  usable: true,
  scope: "global",
  editable: false,
};
const managed: Row = { ...house, id: "g2", name: "Managed", editable: true };

type Call = { method: string; path: string; body: Record<string, unknown> | undefined };

/**
 * A brain that keeps agents in memory and answers the agent routes the way
 * the real ones do, with every request recorded. A refusal is a status with
 * {"error": ...}.
 */
function brain(rows: Row[], refuse?: (call: Call) => Response | undefined) {
  const agents = [...rows];
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
      calls.push({ method, path, body });
      const refused = refuse?.({ method, path, body });
      if (refused) return refused;
      if (path === "/api/tasks") return Response.json([]);
      if (path === "/api/providers") {
        return Response.json([
          {
            id: "p1",
            provider: "OpenAI",
            name: "My OpenAI",
            adapter: "openai_compat",
            plan_kind: "metered",
            status: "Connected",
            models: ["gpt-x", "gpt-y"],
            usable: true,
            usable_models: ["gpt-x", "gpt-y"],
            visibility: "private",
            team_ids: [],
            team_names: [],
            can_manage: true,
            can_use: true,
            is_owner: true,
          },
        ]);
      }
      if (path === "/api/agents" && method === "GET") return Response.json(agents);
      if (path === "/api/agents" && method === "POST") {
        const made = { id: "new1", usable: true, scope: "own", editable: true, ...body } as Row;
        agents.push(made);
        return Response.json(made, { status: 201 });
      }
      const target = agents.find((row) => path === `/api/agents/${row.id}`);
      if (target && method === "PATCH") {
        Object.assign(target, body);
        return Response.json(target);
      }
      if (target && method === "DELETE") {
        agents.splice(agents.indexOf(target), 1);
        return new Response(null, { status: 204 });
      }
      return new Response(null, { status: 404 });
    }),
  );
  return calls;
}

function click(element: HTMLElement) {
  fireEvent.click(element);
}

describe("agents page", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists own agents first and marks a workspace agent as shared and read only", async () => {
    brain([mine, house]);
    renderAgents();

    expect(await screen.findByRole("heading", { level: 1, name: "Agents" })).toBeTruthy();
    const own = await screen.findByTestId("agent-card-a1");
    expect(within(own).getByText("Writer")).toBeTruthy();
    expect(within(own).getByText("Route: fast")).toBeTruthy();
    expect(within(own).getByText("Model: OpenAI / gpt-x")).toBeTruthy();
    expect(within(own).getByRole("button", { name: "Edit Writer" })).toBeTruthy();
    expect(within(own).getByRole("button", { name: "Delete Writer" })).toBeTruthy();
    expect(within(own).queryByText("Shared")).toBeNull();

    const shared = screen.getByTestId("agent-card-g1");
    expect(within(shared).getByText("Shared")).toBeTruthy();
    expect(within(shared).getByText("Managed by your workspace. Read only.")).toBeTruthy();
    expect(within(shared).queryByRole("button", { name: /Edit|Delete/ })).toBeNull();
    const cards = screen.getAllByTestId(/^agent-card-/).map((card) => card.getAttribute("data-testid"));
    expect(cards).toEqual(["agent-card-a1", "agent-card-g1"]);
  });

  it("offers edit and delete on a workspace agent only when the brain says the person may change it", async () => {
    brain([managed]);
    renderAgents();

    const card = await screen.findByTestId("agent-card-g2");
    expect(within(card).getByText("Shared")).toBeTruthy();
    expect(within(card).getByRole("button", { name: "Edit Managed" })).toBeTruthy();
    expect(within(card).queryByText("Managed by your workspace. Read only.")).toBeNull();
  });

  it("creates an agent with its instructions, route and a model from the person's accounts", async () => {
    const calls = brain([house]);
    renderAgents();

    click(await screen.findByRole("button", { name: "New agent" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: "Create agent" }).hasAttribute("disabled")).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "  Research  " } });
    fireEvent.change(within(dialog).getByLabelText(/^Instructions/), { target: { value: "Answer as a researcher." } });
    fireEvent.change(within(dialog).getByLabelText(/^Route/), { target: { value: "deep" } });
    const listbox = await waitFor(() => screen.getByRole("listbox", { name: "Model", hidden: true }));
    await waitFor(() => within(listbox).getByRole("option", { name: "gpt-y", hidden: true }));
    click(within(listbox).getByRole("option", { name: "gpt-y", hidden: true }));
    click(within(dialog).getByRole("button", { name: "Create agent" }));

    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      name: "Research",
      description: "",
      prompt_overlay: "Answer as a researcher.",
      route: "deep",
      model_provider: "OpenAI",
      model_id: "gpt-y",
      enabled: true,
    });
    expect(await screen.findByTestId("agent-card-new1")).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("shows the brain's plain sentence when a name is taken and keeps the dialog open", async () => {
    brain([mine], (call) =>
      call.method === "POST"
        ? Response.json({ error: "An agent with that name already exists." }, { status: 409 })
        : undefined,
    );
    renderAgents();

    click(await screen.findByRole("button", { name: "New agent" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "writer" } });
    click(within(dialog).getByRole("button", { name: "Create agent" }));

    expect((await within(dialog).findByRole("alert")).textContent).toBe("An agent with that name already exists.");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });

  it("edits an agent by sending only the fields that changed", async () => {
    const calls = brain([mine]);
    renderAgents();

    click(await screen.findByRole("button", { name: "Edit Writer" }));
    const dialog = await screen.findByRole("dialog");
    expect((within(dialog).getByLabelText(/^Name/) as HTMLInputElement).value).toBe("Writer");
    fireEvent.change(within(dialog).getByLabelText(/^Route/), { target: { value: "" } });
    fireEvent.click(within(dialog).getByLabelText("Available to pick"));
    click(within(dialog).getByRole("button", { name: "Save agent" }));

    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.path).toBe("/api/agents/a1");
    expect(patch?.body).toEqual({ route: "", enabled: false });
    expect(await screen.findByText("Off")).toBeTruthy();
  });

  it("deletes an agent after a confirmation", async () => {
    const calls = brain([mine]);
    renderAgents();

    click(await screen.findByRole("button", { name: "Delete Writer" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    click(await screen.findByRole("button", { name: "Delete agent" }));

    await waitFor(() => expect(calls.some((c) => c.method === "DELETE")).toBe(true));
    expect(calls.find((c) => c.method === "DELETE")?.path).toBe("/api/agents/a1");
    await waitFor(() => expect(screen.queryByTestId("agent-card-a1")).toBeNull());
  });

  it("filters the list by what the person types", async () => {
    brain([mine, house]);
    renderAgents();

    await screen.findByTestId("agent-card-a1");
    fireEvent.change(screen.getByLabelText("Search agents"), { target: { value: "house" } });
    expect(screen.queryByTestId("agent-card-a1")).toBeNull();
    expect(screen.getByTestId("agent-card-g1")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Search agents"), { target: { value: "zzz" } });
    expect(screen.getByText(/No agents found matching/)).toBeTruthy();
  });

  it("invites the first agent when there are none", async () => {
    brain([]);
    renderAgents();
    expect(await screen.findByRole("heading", { name: "Add an agent" })).toBeTruthy();
  });

  it("says plainly when the list could not be loaded, never with the server's own words", async () => {
    brain([], (call) =>
      call.path === "/api/agents" ? Response.json({ error: "pq: relation secret" }, { status: 500 }) : undefined,
    );
    renderAgents();
    expect(await screen.findByText("Agents could not be loaded")).toBeTruthy();
    expect(screen.getByText("Something went wrong. Try again.")).toBeTruthy();
    expect(screen.queryByText(/secret/)).toBeNull();
  });
});

describe("agents page without a brain", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => cleanup());

  it("works on the stub: a workspace sample, and an agent made and removed here", async () => {
    renderAgents();

    const sample = await screen.findByTestId("agent-card-agent-research");
    expect(within(sample).getByText("Shared")).toBeTruthy();
    expect(within(sample).queryByRole("button", { name: /Edit|Delete/ })).toBeNull();

    click(screen.getByRole("button", { name: "New agent" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/^Name/), { target: { value: "Mine" } });
    click(within(dialog).getByRole("button", { name: "Create agent" }));

    const made = await screen.findByRole("button", { name: "Delete Mine" });
    click(made);
    click(await screen.findByRole("button", { name: "Delete agent" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Delete Mine" })).toBeNull());
  });
});
