import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

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
      screen.getByRole("button", { name: "Test Connection" }).click();
    });

    expect(await screen.findByText(/Connection verified/)).toBeTruthy();
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
    const editBtn = within(githubCard).getByRole("link", { name: "Edit permissions" });
    act(() => {
      editBtn.click();
    });

    expect(await screen.findByRole("heading", { level: 1, name: "Edit GitHub permissions" })).toBeTruthy();

    // Advance to Permissions step
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

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
      screen.getByRole("button", { name: "Test Connection" }).click();
    });

    expect(await screen.findByText(/Connection verified/)).toBeTruthy();

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
});
