import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { BrainError, createStubApi, type ApiClient } from "../../app/api";
import { AppProviders } from "../../theme";
import { sampleGates, sampleRoutes } from "./sample";

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
  return history;
}

async function asMember(): Promise<ApiClient> {
  const client = createStubApi();
  await client.me.viewAs("member");
  return client;
}

function rowFor(table: HTMLElement, first: string): HTMLTableRowElement {
  const row = within(table)
    .getAllByRole("row")
    .find((candidate) => (candidate as HTMLTableRowElement).cells[0]?.textContent === first);
  if (!row) throw new Error(`no row starting with ${first}`);
  return row as HTMLTableRowElement;
}

function cellTexts(row: HTMLTableRowElement): string[] {
  return Array.from(row.cells).map((cell) => cell.textContent ?? "");
}

describe("UPM-202 models and routes", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-202 lists every route with its capability and decision", async () => {
    renderAt("/settings/models");
    expect(await screen.findByRole("heading", { level: 1, name: "Models and routes" })).toBeTruthy();

    const table = await screen.findByRole("table", { name: "Routes" });
    const body = within(table).getAllByRole("row").slice(1);
    expect(body).toHaveLength(sampleRoutes.length);

    const chat = cellTexts(rowFor(table, "chat"));
    expect(chat.slice(0, 3)).toEqual(["chat", "chat", "None"]);
    const dispatch = cellTexts(rowFor(table, "agent-dispatch"));
    expect(dispatch.slice(0, 3)).toEqual(["agent-dispatch", "decide", "choice"]);
    const confidence = cellTexts(rowFor(table, "result-confidence"));
    expect(confidence.slice(0, 3)).toEqual(["result-confidence", "decide", "yesno"]);
    const stream = cellTexts(rowFor(table, "chat-stream"));
    expect(stream[1]).toBe("stream");
    const embed = cellTexts(rowFor(table, "embed"));
    expect(embed[1]).toBe("embed");
  });

  it("UPM-202 opens a route to show its hops in order and closes it again", async () => {
    renderAt("/settings/models");
    const table = await screen.findByRole("table", { name: "Routes" });
    const toggle = within(table).getByRole("button", { name: "Show 3 hops for chat" });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("table", { name: "Hops for chat" })).toBeNull();
    expect(within(table).getByRole("button", { name: "Show 1 hop for embed" })).toBeTruthy();

    act(() => {
      fireEvent.click(toggle);
    });

    const open = within(table).getByRole("button", { name: "Hide 3 hops for chat" });
    expect(open.getAttribute("aria-expanded")).toBe("true");
    const hops = screen.getByRole("table", { name: "Hops for chat" });
    const rows = within(hops).getAllByRole("row");
    expect(cellTexts(rows[0] as HTMLTableRowElement)).toEqual([
      "Position",
      "Adapter",
      "Model",
      "Weight",
      "Retry",
    ]);
    expect(rows.slice(1).map((row) => cellTexts(row as HTMLTableRowElement))).toEqual([
      ["1", "anthropic", "claude-3-7-sonnet", "3", "1"],
      ["2", "openai_compat", "gpt-4.1", "1", "1"],
      ["3", "ollama", "llama3.3:70b", "1", "0"],
    ]);
    expect(open.getAttribute("aria-controls")).toBe(hops.parentElement?.id);
    // Opening one route leaves the others closed.
    expect(screen.queryByRole("table", { name: "Hops for embed" })).toBeNull();

    act(() => {
      fireEvent.click(open);
    });
    expect(screen.queryByRole("table", { name: "Hops for chat" })).toBeNull();
    expect(within(table).getByRole("button", { name: "Show 3 hops for chat" })).toBeTruthy();
  });

  it("UPM-202 right-aligns each hops header over its numeric cells", async () => {
    renderAt("/settings/models");
    const table = await screen.findByRole("table", { name: "Routes" });
    act(() => {
      fireEvent.click(within(table).getByRole("button", { name: "Show 3 hops for chat" }));
    });
    const hops = screen.getByRole("table", { name: "Hops for chat" });
    const [head, first] = within(hops).getAllByRole("row") as HTMLTableRowElement[];
    const headers = Array.from(head!.cells);
    const cells = Array.from(first!.cells);
    expect(headers.map((cell) => cell.textContent)).toEqual(["Position", "Adapter", "Model", "Weight", "Retry"]);
    const aligned = headers.map((header) => header.style.textAlign);
    const numeric = cells.map((cell) => (cell.classList.contains("upmo-table__num") ? "right" : ""));
    expect(aligned).toEqual(numeric);
    expect(headers[0]!.style.textAlign).toBe("right");
  });

  it("UPM-202 sorts hops by position whatever order the server sends", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      gateway: {
        async routes() {
          return [
            {
              name: "shuffled",
              capability: "chat",
              decision: "",
              hops: [
                { position: 2, adapter: "b", model: "second", weight: 1, retry: 0 },
                { position: 1, adapter: "a", model: "first", weight: 1, retry: 0 },
              ],
            },
          ];
        },
      },
    };
    renderAt("/settings/models", client);
    const table = await screen.findByRole("table", { name: "Routes" });
    act(() => {
      fireEvent.click(within(table).getByRole("button", { name: "Show 2 hops for shuffled" }));
    });
    const hops = screen.getByRole("table", { name: "Hops for shuffled" });
    const models = within(hops)
      .getAllByRole("row")
      .slice(1)
      .map((row) => (row as HTMLTableRowElement).cells[2]?.textContent);
    expect(models).toEqual(["first", "second"]);
  });

  it("UPM-202 shows each gate with its score, bar and a pass or fail mark", async () => {
    renderAt("/settings/models");
    const table = await screen.findByRole("table", { name: "Decision gates" });
    expect(within(table).getAllByRole("row").slice(1)).toHaveLength(sampleGates.length);

    const dispatch = cellTexts(rowFor(table, "agent-dispatch"));
    expect(dispatch[1]).toBe("choice");
    expect(dispatch[2]).toBe("c28d8340a482");
    expect(dispatch[3]).toBe("Pass");
    expect(dispatch[4]).toBe("46 / 50 (92%)");
    expect(dispatch[5]).toBe("90%");
    expect(dispatch[6]).toBe("4 Nov 2026, 09:12 UTC");
    expect(dispatch[7]).toBe("Meets bar");

    // 38 of 40 is 95 percent and the bar is 95: exactly on the bar passes.
    expect(cellTexts(rowFor(table, "fact-keep"))[7]).toBe("Meets bar");

    const tier = cellTexts(rowFor(table, "model-tier"));
    expect(tier[3]).toBe("Fail");
    expect(tier[4]).toBe("27 / 30 (90%)");
    expect(tier[5]).toBe("95%");
    expect(tier[7]).toBe("Below bar");

    const short = within(table).getByRole("button", {
      name: "c28d8340a482, show full fingerprint for agent-dispatch",
    });
    expect(short.textContent).toBe("c28d8340a482");
  });

  it("UPM-202 reaches the full fingerprint with the keyboard and a tap, not only by hover", async () => {
    renderAt("/settings/models");
    const table = await screen.findByRole("table", { name: "Decision gates" });
    const full = sampleGates[0]!.fingerprint;
    expect(full.length).toBeGreaterThan(12);
    expect(screen.queryByText(full)).toBeNull();
    // A title attribute is not enough: nothing may rely on hover alone.
    expect(table.querySelector("[title]")).toBeNull();

    const trigger = within(table).getByRole("button", {
      name: "c28d8340a482, show full fingerprint for agent-dispatch",
    });
    expect(trigger.tabIndex).toBeGreaterThanOrEqual(0);
    act(() => {
      trigger.focus();
    });
    expect(document.activeElement).toBe(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("false");

    act(() => {
      fireEvent.click(trigger);
    });
    // jsdom has no layout, so Mantine marks the floating dropdown as hidden.
    const dialog = await screen.findByRole("dialog", { name: "Full fingerprint for agent-dispatch", hidden: true });
    expect(within(dialog).getByText(full)).toBeTruthy();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");

    act(() => {
      fireEvent.keyDown(dialog, { key: "Escape" });
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Full fingerprint for agent-dispatch", hidden: true })).toBeNull();
    });
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  it("UPM-202 judges the indicator by the counts, not by the stored verdict", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      decisions: {
        async gates() {
          return [
            { ...sampleGates[0]!, route: "stored-pass-but-short", verdict: "pass", hit: 17, total: 20, bar: 90 },
            { ...sampleGates[0]!, route: "stored-fail-but-clear", verdict: "fail", hit: 19, total: 20, bar: 90 },
          ];
        },
      },
    };
    renderAt("/settings/models", client);
    const table = await screen.findByRole("table", { name: "Decision gates" });
    expect(cellTexts(rowFor(table, "stored-pass-but-short"))[7]).toBe("Below bar");
    expect(cellTexts(rowFor(table, "stored-fail-but-clear"))[7]).toBe("Meets bar");
  });

  it("UPM-202 shows empty states when there are no routes and no gates", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      gateway: { routes: async () => [] },
      decisions: { gates: async () => [] },
    };
    renderAt("/settings/models", client);
    expect(await screen.findByRole("heading", { name: "No routes yet" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "No decision gates yet" })).toBeTruthy();
    expect(screen.queryByRole("table", { name: "Routes" })).toBeNull();
    expect(screen.queryByRole("table", { name: "Decision gates" })).toBeNull();
  });

  it("UPM-202 reports one failed list and still shows the other", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      gateway: {
        async routes() {
          throw new Error("gateway down");
        },
      },
    };
    renderAt("/settings/models", client);
    expect((await screen.findByText("Routes could not be loaded.")).closest("[role='alert']")).toBeTruthy();
    expect(await screen.findByRole("table", { name: "Decision gates" })).toBeTruthy();
    expect(screen.queryByText("Needs settings access")).toBeNull();
  });

  it("UPM-202 tells a member they need settings access and asks the server for nothing", async () => {
    const stub = await asMember();
    const routes = vi.fn<ApiClient["gateway"]["routes"]>(stub.gateway.routes);
    const gates = vi.fn<ApiClient["decisions"]["gates"]>(stub.decisions.gates);
    const client: ApiClient = { ...stub, gateway: { routes }, decisions: { gates } };

    const history = renderAt("/settings/models", client);
    expect(await screen.findByRole("heading", { level: 1, name: "Models and routes" })).toBeTruthy();
    expect(await screen.findByRole("heading", { name: "Needs settings access" })).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
    expect(routes).not.toHaveBeenCalled();
    expect(gates).not.toHaveBeenCalled();
    // Still signed in and still on the page.
    expect(history.location.pathname).toBe("/settings/models");
    expect(screen.queryByLabelText("Email")).toBeNull();
    expect(screen.getByRole("button", { name: /^User account:/ })).toBeTruthy();
  });

  it("UPM-202 shows the same state, without signing out, when the server refuses an admin", async () => {
    const stub = createStubApi();
    const refuse = async (): Promise<never> => {
      throw new BrainError(403, "forbidden", "Needs settings access.");
    };
    const client: ApiClient = {
      ...stub,
      gateway: { routes: refuse },
      decisions: { gates: refuse },
    };
    const history = renderAt("/settings/models", client);
    expect(await screen.findByRole("heading", { name: "Needs settings access" })).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
    expect(history.location.pathname).toBe("/settings/models");
    expect(screen.queryByText("Routes could not be loaded.")).toBeNull();
  });

  it("UPM-202 has the stub refuse a member and hand out copies to an admin", async () => {
    const api = createStubApi();
    const routes = await api.gateway.routes();
    routes[0]!.name = "mutated";
    routes[0]!.hops[0]!.model = "mutated";
    expect((await api.gateway.routes())[0]?.name).toBe("chat");
    expect((await api.gateway.routes())[0]?.hops[0]?.model).toBe("claude-3-7-sonnet");
    expect(await api.decisions.gates()).toHaveLength(sampleGates.length);

    await api.me.viewAs("member");
    await expect(api.gateway.routes()).rejects.toMatchObject({ status: 403 });
    await expect(api.decisions.gates()).rejects.toMatchObject({ status: 403 });
  });

  it("UPM-202 puts Models and routes in the navigation for an admin and opens the page", async () => {
    renderAt("/");
    await screen.findByRole("heading", { level: 1, name: "Home" });
    const nav = screen.getByRole("navigation", { name: "Main" });
    const link = within(nav).getByRole("link", { name: "Models and routes" });
    expect(link.getAttribute("href")).toBe("/settings/models");

    act(() => {
      fireEvent.click(link);
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Models and routes" })).toBeTruthy();
    await waitFor(() => {
      expect(within(nav).getByRole("link", { name: "Models and routes" }).getAttribute("aria-current")).toBe("page");
    });
    expect(await screen.findByRole("table", { name: "Routes" })).toBeTruthy();
  });

  it.each(["member", "viewer"] as const)("UPM-202 hides the navigation item from a %s", async (role) => {
    const stub = createStubApi();
    await stub.me.viewAs(role);
    renderAt("/", stub);
    await screen.findByRole("heading", { level: 1, name: "Home" });
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "Connectors" })).toBeTruthy();
    expect(within(nav).queryByRole("link", { name: "Models and routes" })).toBeNull();
  });
});
