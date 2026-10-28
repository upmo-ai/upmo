import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
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
