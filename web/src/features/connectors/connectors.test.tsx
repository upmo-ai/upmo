import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, configure, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { BrainError, createStubApi, type ApiClient } from "../../app/api";
import { AppProviders } from "../../theme";

// The first findBy after a route render waits on a lazy chunk and a fetch. The 1000ms default
// is too short on a loaded machine (UPM-384); this stays under the 5000ms test timeout.
configure({ asyncUtilTimeout: 4000 });

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
  return history;
}

describe("connectors", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders connector page with initial cards", async () => {
    renderAt("/connectors");

    expect(
      await screen.findByText("GitHub, email, calendar and any MCP server. Agents use only the connectors you add."),
    ).toBeTruthy();
    expect(screen.getByRole("heading", { level: 1, name: "Connectors" })).toBeTruthy();

    expect(await screen.findByTestId("connector-card-github")).toBeTruthy();
    expect(screen.getByTestId("connector-card-slack")).toBeTruthy();
  });

  it("can add a new connector via multi-step wizard with connection test gate", async () => {
    renderAt("/connectors");

    expect(await screen.findByRole("heading", { level: 1, name: "Connectors" })).toBeTruthy();

    const addBtn = await screen.findByRole("link", { name: "Add connector" });
    act(() => {
      addBtn.click();
    });

    expect(await screen.findByRole("heading", { level: 1, name: "Connect a tool" })).toBeTruthy();

    // Step 1: Select Notion
    const notionBtn = screen.getByRole("button", { name: "Notion" });
    act(() => {
      notionBtn.click();
    });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 2: Authentication
    expect(await screen.findByText(/Notion Authentication/)).toBeTruthy();
    const tokenInput = screen.getByPlaceholderText("secret_...");
    fireEvent.change(tokenInput, { target: { value: "secret_notion_key_1234" } });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 3: Permissions
    expect(await screen.findByText("Permissions & Scope")).toBeTruthy();
    const onlyTheseRadio = screen.getByRole("radio", { name: /Only these specific resources/ });
    act(() => {
      onlyTheseRadio.click();
    });

    // Enter resource identifier
    const resourceInput = await screen.findByLabelText(/Resource IDs/);
    fireEvent.change(resourceInput, { target: { value: "workspace/engineering" } });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 4: Validation & Connect
    expect(await screen.findByText("Review Permissions")).toBeTruthy();
    const connectBtn = screen.getByRole("button", { name: "Connect tool" });
    expect(connectBtn.getAttribute("disabled")).not.toBeNull();

    // Run connection test
    act(() => {
      screen.getByRole("button", { name: "Test connection" }).click();
    });

    expect(await screen.findByText("Connection verified")).toBeTruthy();
    expect(connectBtn.getAttribute("disabled")).toBeNull();

    // Submit
    act(() => {
      connectBtn.click();
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Connectors" })).toBeTruthy();
      expect(screen.getByTestId("connector-card-notion")).toBeTruthy();
      expect(screen.getByText("workspace/engineering")).toBeTruthy();
    });
  });

  it("can edit connector permissions via multi-step wizard", async () => {
    renderAt("/connectors");

    const githubCard = await screen.findByTestId("connector-card-github");
    const editBtn = within(githubCard).getByRole("button", { name: "Edit permissions" });
    act(() => {
      editBtn.click();
    });

    expect(await screen.findByRole("heading", { level: 1, name: "Edit GitHub permissions" })).toBeTruthy();
    expect(screen.getByText("Permissions & Scope")).toBeTruthy();

    // Change policy to "Allow"
    const allowRadio = screen.getByRole("radio", { name: "Allow" });
    act(() => {
      allowRadio.click();
    });

    // Advance to Validation
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Run test and save
    act(() => {
      screen.getByRole("button", { name: "Test connection" }).click();
    });

    expect(await screen.findByText("Connection verified")).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Save permissions" }).click();
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Connectors" })).toBeTruthy();
      expect(within(screen.getByTestId("connector-card-github")).getByText("Allow")).toBeTruthy();
    });
  });

  it("can disconnect a connector", async () => {
    renderAt("/connectors");

    const slackCard = await screen.findByTestId("connector-card-slack");
    act(() => {
      within(slackCard).getByRole("button", { name: "Disconnect" }).click();
    });

    const confirm = await screen.findByRole("dialog");
    expect(within(confirm).getByText(/Agents will lose access to Slack immediately/)).toBeTruthy();

    act(() => {
      within(confirm).getByRole("button", { name: "Disconnect connector" }).click();
    });

    await waitFor(() => {
      expect(screen.queryByTestId("connector-card-slack")).toBeNull();
    });
  });

  it("keeps the first step locked and starts at Permissions in edit mode", async () => {
    renderAt("/connectors");

    const githubCard = await screen.findByTestId("connector-card-github");
    act(() => {
      within(githubCard).getByRole("button", { name: "Edit permissions" }).click();
    });

    expect(await screen.findByText("Permissions & Scope")).toBeTruthy();
    expect(screen.queryByText("Select Tool")).toBeNull();

    const toolStep = screen.getByRole("button", { name: /Tool/ });
    expect(toolStep.getAttribute("aria-disabled")).toBe("true");
    act(() => {
      toolStep.click();
    });
    expect(screen.getByText("Permissions & Scope")).toBeTruthy();
    expect(screen.queryByText("Select Tool")).toBeNull();

    // Authentication stays reachable by going back.
    act(() => {
      screen.getByRole("button", { name: "Back" }).click();
    });
    expect(await screen.findByText(/GitHub Authentication/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
  });

  it("keeps Connect disabled until the connection test succeeds", async () => {
    renderAt("/connectors/new");

    expect(await screen.findByRole("heading", { level: 1, name: "Connect a tool" })).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Notion" }).click();
    });
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });
    expect(await screen.findByText(/Notion Authentication/)).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });
    expect(await screen.findByText("Permissions & Scope")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    expect(await screen.findByText("Ready to test")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect tool" }).getAttribute("disabled")).not.toBeNull();

    act(() => {
      screen.getByRole("button", { name: "Test connection" }).click();
    });
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect tool" }).getAttribute("disabled")).toBeNull();

    // Changing the permissions invalidates the passing test.
    act(() => {
      screen.getByRole("button", { name: "Back" }).click();
    });
    act(() => {
      screen.getByRole("radio", { name: "Block" }).click();
    });
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });
    expect(await screen.findByText("Ready to test")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect tool" }).getAttribute("disabled")).not.toBeNull();
  });

  it("shows the error and keeps Connect disabled when the connection test fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/connectors/test" && init?.method === "POST") {
          return Response.json({ ok: false, error: "401 Unauthorized: the token was rejected." });
        }
        if (url === "/api/connector-grants" && !init?.method) {
          return Response.json([]);
        }
        return new Response(null, { status: 404 });
      }),
    );
    renderAt("/connectors/new");

    expect(await screen.findByRole("heading", { level: 1, name: "Connect a tool" })).toBeTruthy();
    for (const expected of [/GitHub Authentication/, "Permissions & Scope"]) {
      act(() => {
        screen.getByRole("button", { name: "Continue" }).click();
      });
      expect(await screen.findByText(expected)).toBeTruthy();
    }
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    act(() => {
      screen.getByRole("button", { name: "Test connection" }).click();
    });

    expect(await screen.findByText("Connection failed")).toBeTruthy();
    expect(screen.getByText("401 Unauthorized: the token was rejected.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect tool" }).getAttribute("disabled")).not.toBeNull();
  });

  it("UPM-103 shows the current read, change and delete rule for each connector", async () => {
    renderAt("/connectors");

    const github = await screen.findByRole("group", { name: "GitHub rules" });
    expect(within(github).getByText("Read: Allow")).toBeTruthy();
    expect(within(github).getByText("Change: Ask first")).toBeTruthy();
    expect(within(github).getByText("Delete: Ask first")).toBeTruthy();

    // Slack has no stored rule, so change follows its grant, which allows changes.
    const slack = await screen.findByRole("group", { name: "Slack rules" });
    expect(within(slack).getByText("Read: Allow")).toBeTruthy();
    expect(within(slack).getByText("Change: Allow")).toBeTruthy();
    expect(within(slack).getByText("Delete: Ask first")).toBeTruthy();
  });

  it("UPM-103 lets a person change the read and change rule and saves them", async () => {
    const client = createStubApi();
    renderAt("/connectors", client);

    fireEvent.click(await screen.findByRole("button", { name: "Edit rules for GitHub" }));
    const dialog = await screen.findByRole("dialog", { name: "GitHub rules" });
    const save = within(dialog).getByRole("button", { name: "Save rules" });
    expect(save.getAttribute("disabled")).not.toBeNull();

    const read = within(dialog).getByRole("radiogroup", { name: "Read rule" });
    const change = within(dialog).getByRole("radiogroup", { name: "Change rule" });
    expect((within(read).getByRole("radio", { name: "Allow" }) as HTMLInputElement).checked).toBe(true);
    expect((within(change).getByRole("radio", { name: "Ask first" }) as HTMLInputElement).checked).toBe(true);

    act(() => {
      within(read).getByRole("radio", { name: "Block" }).click();
    });
    act(() => {
      within(change).getByRole("radio", { name: "Allow" }).click();
    });
    expect(save.getAttribute("disabled")).toBeNull();

    act(() => {
      save.click();
    });

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "GitHub rules" })).toBeNull();
    });
    const group = await screen.findByRole("group", { name: "GitHub rules" });
    await waitFor(() => {
      expect(within(group).getByText("Read: Block")).toBeTruthy();
    });
    expect(within(group).getByText("Change: Allow")).toBeTruthy();
    expect(within(group).getByText("Delete: Ask first")).toBeTruthy();

    // The stub kept the rule, and delete still asks.
    expect(await client.connectors.rules.get("github")).toEqual({
      read: "block",
      change: "allow",
      delete: "ask",
    });

    // Opening the editor again starts from what was saved.
    act(() => {
      screen.getByRole("button", { name: "Edit rules for GitHub" }).click();
    });
    const again = await screen.findByRole("dialog", { name: "GitHub rules" });
    expect(
      (within(within(again).getByRole("radiogroup", { name: "Read rule" })).getByRole("radio", { name: "Block" }) as HTMLInputElement)
        .checked,
    ).toBe(true);
  });

  it("UPM-103 shows delete as fixed at ask and offers no control for it", async () => {
    renderAt("/connectors");

    fireEvent.click(await screen.findByRole("button", { name: "Edit rules for GitHub" }));
    const dialog = await screen.findByRole("dialog", { name: "GitHub rules" });

    expect(within(dialog).getAllByRole("radiogroup")).toHaveLength(2);
    expect(within(dialog).queryByRole("radiogroup", { name: /delete/i })).toBeNull();
    expect(within(dialog).getByText("Delete")).toBeTruthy();
    expect(within(dialog).getByText("Deleting always asks first. This rule is fixed and cannot be changed.")).toBeTruthy();
  });

  it("UPM-103 leaves the saved rules alone when the editor is cancelled", async () => {
    const client = createStubApi();
    renderAt("/connectors", client);

    fireEvent.click(await screen.findByRole("button", { name: "Edit rules for GitHub" }));
    const dialog = await screen.findByRole("dialog", { name: "GitHub rules" });
    act(() => {
      within(within(dialog).getByRole("radiogroup", { name: "Read rule" })).getByRole("radio", { name: "Block" }).click();
    });
    act(() => {
      within(dialog).getByRole("button", { name: "Cancel" }).click();
    });

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "GitHub rules" })).toBeNull();
    });
    expect(within(screen.getByRole("group", { name: "GitHub rules" })).getByText("Read: Allow")).toBeTruthy();
    expect((await client.connectors.rules.get("github")).read).toBe("allow");
  });

  it.each([
    [403, "forbidden", "You are not allowed to change these rules. Nothing was saved."],
    [401, "unauthorised", "You are not allowed to change these rules. Nothing was saved."],
    [500, "server", "Rules could not be saved. Nothing changed. Try again."],
  ])("UPM-103 explains a failed save (%i) and keeps the person signed in", async (status, code, message) => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      connectors: {
        ...stub.connectors,
        rules: {
          get: stub.connectors.rules.get,
          async put() {
            throw new BrainError(status, code);
          },
        },
      },
    };
    const history = renderAt("/connectors", client);

    fireEvent.click(await screen.findByRole("button", { name: "Edit rules for GitHub" }));
    const dialog = await screen.findByRole("dialog", { name: "GitHub rules" });
    act(() => {
      within(within(dialog).getByRole("radiogroup", { name: "Change rule" })).getByRole("radio", { name: "Block" }).click();
    });
    act(() => {
      within(dialog).getByRole("button", { name: "Save rules" }).click();
    });

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain(message);
    // The dialog stays open with the choice kept, and nothing was signed out.
    expect(
      (within(within(dialog).getByRole("radiogroup", { name: "Change rule" })).getByRole("radio", { name: "Block" }) as HTMLInputElement)
        .checked,
    ).toBe(true);
    expect(history.location.pathname).toBe("/connectors");
    expect(screen.queryByLabelText("Email")).toBeNull();
    expect(screen.getByRole("button", { name: /^User account:/ })).toBeTruthy();
    expect(within(screen.getByRole("group", { name: "GitHub rules" })).getByText("Change: Ask first")).toBeTruthy();
  });

  it("UPM-103 shows no rules, and no edit button, when the rules cannot be read", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      connectors: {
        ...stub.connectors,
        rules: {
          async get() {
            throw new BrainError(404, "not_found");
          },
          put: stub.connectors.rules.put,
        },
      },
    };
    renderAt("/connectors", client);

    await waitFor(() => {
      expect(screen.getAllByText("Rules are not available.")).toHaveLength(3);
    });
    expect(screen.queryByRole("group", { name: "GitHub rules" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit rules for GitHub" })).toBeNull();
    expect(screen.queryByText(/Read: /)).toBeNull();
  });

  it("UPM-103 keeps stub rules per connector, fixes delete at ask and refuses an unknown policy", async () => {
    const api = createStubApi();

    expect(await api.connectors.rules.get("github")).toEqual({ read: "allow", change: "ask", delete: "ask" });
    expect(await api.connectors.rules.get("slack")).toEqual({ read: "allow", change: "allow", delete: "ask" });

    const saved = await api.connectors.rules.put("slack", { read: "ask", change: "block" });
    expect(saved).toEqual({ read: "ask", change: "block", delete: "ask" });
    expect(await api.connectors.rules.get("slack")).toEqual(saved);
    expect((await api.connectors.rules.get("github")).read).toBe("allow");

    // A delete sent anyway is ignored.
    const sneaky = { read: "allow", change: "allow", delete: "allow" } as unknown as Parameters<
      typeof api.connectors.rules.put
    >[1];
    expect((await api.connectors.rules.put("slack", sneaky)).delete).toBe("ask");

    await expect(
      api.connectors.rules.put("slack", { read: "maybe", change: "allow" } as never),
    ).rejects.toThrow("Choose allow, ask or block.");
    expect((await api.connectors.rules.get("slack")).read).toBe("allow");

    await expect(api.connectors.rules.get("Not A Slug")).rejects.toMatchObject({ status: 400 });

    // Disconnecting a connector drops its rules: there is nothing to read, and
    // nothing to write, until it is connected again.
    await api.connectors.remove("slack");
    await expect(api.connectors.rules.get("slack")).rejects.toMatchObject({ status: 404 });
    await expect(api.connectors.rules.put("slack", { read: "allow", change: "allow" })).rejects.toMatchObject({
      status: 404,
    });
    await api.connectors.put({
      connector: "slack",
      scope: "own",
      resource_scope: "all",
      resource_ids: [],
      changes: "ask",
    });
    expect(await api.connectors.rules.get("slack")).toEqual({ read: "allow", change: "ask", delete: "ask" });
  });

  it("UPM-103 returns not found, not default rules, for a connector that is not connected", async () => {
    const api = createStubApi();

    await expect(api.connectors.rules.get("notion")).rejects.toMatchObject({ status: 404 });
    await expect(api.connectors.rules.put("notion", { read: "block", change: "block" })).rejects.toMatchObject({
      status: 404,
    });
    // The refused write left nothing behind.
    await expect(api.connectors.rules.get("notion")).rejects.toMatchObject({ status: 404 });
    expect((await api.connectors.list()).some((grant) => grant.connector === "notion")).toBe(false);
  });

  it("UPM-103 keeps one change rule: saving rules moves the card, and editing permissions moves the rules", async () => {
    const client = createStubApi();
    renderAt("/connectors", client);

    const card = await screen.findByTestId("connector-card-github");
    expect(within(card).getByText("Ask first")).toBeTruthy();

    fireEvent.click(await screen.findByRole("button", { name: "Edit rules for GitHub" }));
    const dialog = await screen.findByRole("dialog", { name: "GitHub rules" });
    act(() => {
      within(within(dialog).getByRole("radiogroup", { name: "Change rule" })).getByRole("radio", { name: "Allow" }).click();
    });
    act(() => {
      within(dialog).getByRole("button", { name: "Save rules" }).click();
    });

    await waitFor(() => {
      expect(within(screen.getByTestId("connector-card-github")).getByText("Allow")).toBeTruthy();
    });
    expect(within(screen.getByTestId("connector-card-github")).queryByText("Ask first")).toBeNull();
    expect(within(screen.getByRole("group", { name: "GitHub rules" })).getByText("Change: Allow")).toBeTruthy();
    expect((await client.connectors.list()).find((grant) => grant.connector === "github")?.changes).toBe("allow");

    // The other way round: the permissions wizard is the same rule.
    const grant = (await client.connectors.list()).find((item) => item.connector === "github");
    await client.connectors.put({ ...grant!, changes: "block" });
    expect((await client.connectors.rules.get("github")).change).toBe("block");
  });

  it("UPM-103 shows a global connector's rules read only with a Global marker", async () => {
    const client = createStubApi();
    renderAt("/connectors", client);

    const rules = await screen.findByRole("group", { name: "Google Drive rules" });
    expect(within(rules).getByText("Global")).toBeTruthy();
    expect(within(rules).getByText("Read: Allow")).toBeTruthy();
    expect(within(rules).getByText("Change: Ask first")).toBeTruthy();
    expect(within(rules).getByText("Delete: Ask first")).toBeTruthy();

    // No editor, no permissions wizard and no disconnect for a connector the person does not own.
    expect(screen.queryByRole("button", { name: "Edit rules for Google Drive" })).toBeNull();
    const card = screen.getByTestId("connector-card-google-drive");
    expect(within(card).queryByRole("button", { name: "Edit permissions" })).toBeNull();
    expect(within(card).queryByRole("button", { name: "Disconnect" })).toBeNull();

    // Their own connectors stay editable and carry no marker.
    expect(within(screen.getByRole("group", { name: "GitHub rules" })).queryByText("Global")).toBeNull();
    expect(screen.getByRole("button", { name: "Edit rules for GitHub" })).toBeTruthy();
    expect(within(screen.getByTestId("connector-card-github")).getByRole("button", { name: "Disconnect" })).toBeTruthy();
  });

  it("UPM-103 refuses to change, edit or remove a global connector", async () => {
    const api = createStubApi();
    const before = await api.connectors.rules.get("google-drive");

    await expect(api.connectors.rules.put("google-drive", { read: "block", change: "block" })).rejects.toMatchObject({
      status: 403,
    });
    const grant = (await api.connectors.list()).find((item) => item.connector === "google-drive")!;
    expect(grant.scope).toBe("global");
    await expect(api.connectors.put({ ...grant, changes: "allow" })).rejects.toMatchObject({ status: 403 });
    await expect(api.connectors.remove("google-drive")).rejects.toMatchObject({ status: 403 });

    expect(await api.connectors.rules.get("google-drive")).toEqual(before);
    expect((await api.connectors.list()).filter((item) => item.scope === "own").length).toBeGreaterThan(0);
  });

  it("UPM-103 opens no permissions wizard for a global connector", async () => {
    renderAt("/connectors/google-drive/edit");

    expect(await screen.findByRole("heading", { level: 1, name: "Global connector" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save permissions" })).toBeNull();
    expect(screen.getByRole("link", { name: "Back to Connectors" })).toBeTruthy();
  });

  it("UPM-103 reads rules from the API and not from fixtures when the live backend serves the grants", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input) === "/api/connector-grants" && !init?.method) {
          return Response.json([{ connector: "github", resource_scope: "all", resource_ids: [], changes: "ask" }]);
        }
        return new Response(null, { status: 404 });
      }),
    );
    const stub = createStubApi();
    const get = vi.fn<ApiClient["connectors"]["rules"]["get"]>(stub.connectors.rules.get);
    const client: ApiClient = { ...stub, connectors: { ...stub.connectors, rules: { get, put: stub.connectors.rules.put } } };
    renderAt("/connectors", client);

    expect(await screen.findByTestId("connector-card-github")).toBeTruthy();
    expect(screen.getByText("Rules are not available.")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "GitHub rules" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit rules for GitHub" })).toBeNull();
    expect(get).not.toHaveBeenCalled();
  });

  it("UPM-207 fetches the grant list on mount instead of freezing on the probe snapshot", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input) === "/api/connector-grants" && !init?.method) {
          calls += 1;
          return Response.json(
            calls === 1
              ? []
              : [{ connector: "github", resource_scope: "all", resource_ids: [], changes: "ask" }],
          );
        }
        return new Response(null, { status: 404 });
      }),
    );
    renderAt("/connectors");

    // The first call is the live probe and sees an empty workspace. The list
    // must then fetch for itself; seeding it with the probe snapshot would
    // count as fresh forever and hide the connector the API now serves.
    expect(await screen.findByTestId("connector-card-github")).toBeTruthy();
    expect(calls).toBe(2);
  });

  it("UPM-207 opens the editor for a connector that was connected in the same session", async () => {
    const grants: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input);
        if (url === "/api/connectors/test" && init?.method === "POST") {
          return Response.json({ ok: true });
        }
        if (url === "/api/connector-grants" && !init?.method) {
          return Response.json(grants);
        }
        if (url === "/api/connector-grants/github" && init?.method === "PUT") {
          grants.push({ connector: "github", resource_scope: "all", resource_ids: [], changes: "ask" });
          return new Response(null, { status: 204 });
        }
        return new Response(null, { status: 404 });
      }),
    );
    const history = renderAt("/connectors/new");

    expect(await screen.findByRole("heading", { level: 1, name: "Connect a tool" })).toBeTruthy();
    for (let step = 0; step < 3; step += 1) {
      act(() => {
        screen.getByRole("button", { name: "Continue" }).click();
      });
    }
    act(() => {
      screen.getByRole("button", { name: "Test connection" }).click();
    });
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    act(() => {
      screen.getByRole("button", { name: "Connect tool" }).click();
    });
    expect(await screen.findByTestId("connector-card-github")).toBeTruthy();

    // The editor reads the same grants. A probe cached before the connect
    // would still say "no grants" and show "Connector not found".
    act(() => {
      history.push("/connectors/github/edit");
    });
    expect(await screen.findByText("Permissions & Scope")).toBeTruthy();
    expect(screen.queryByText("Connector not found")).toBeNull();
  });

  it("UPM-103 links each rule control to its description", async () => {
    renderAt("/connectors");

    fireEvent.click(await screen.findByRole("button", { name: "Edit rules for GitHub" }));
    const dialog = await screen.findByRole("dialog", { name: "GitHub rules" });

    for (const [name, hint] of [
      ["Read rule", /Looking things up/],
      ["Change rule", /Creating or editing/],
    ] as const) {
      const control = within(dialog).getByRole("radiogroup", { name });
      const id = control.getAttribute("aria-describedby");
      expect(id).toBeTruthy();
      expect(document.getElementById(id ?? "")?.textContent).toMatch(hint);
    }
  });

  it("UPM-103 shows the reason when a rule is rejected as invalid", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      connectors: {
        ...stub.connectors,
        rules: {
          get: stub.connectors.rules.get,
          async put() {
            throw new BrainError(400, "bad_request", "Change cannot be blocked while a task is running.");
          },
        },
      },
    };
    renderAt("/connectors", client);

    fireEvent.click(await screen.findByRole("button", { name: "Edit rules for GitHub" }));
    const dialog = await screen.findByRole("dialog", { name: "GitHub rules" });
    act(() => {
      within(within(dialog).getByRole("radiogroup", { name: "Change rule" })).getByRole("radio", { name: "Block" }).click();
    });
    act(() => {
      within(dialog).getByRole("button", { name: "Save rules" }).click();
    });

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("Change cannot be blocked while a task is running.");
    expect(alert.textContent).not.toContain("Try again");
  });

  it("filters the tool tiles and shows the empty state", async () => {
    renderAt("/connectors/new");

    const filter = await screen.findByPlaceholderText("Filter tools...");
    fireEvent.change(filter, { target: { value: "zzz" } });
    expect(await screen.findByText(/No connectors found matching/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "GitHub" })).toBeNull();

    fireEvent.change(filter, { target: { value: "notion" } });
    expect(await screen.findByRole("button", { name: "Notion" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Slack" })).toBeNull();
  });
});
