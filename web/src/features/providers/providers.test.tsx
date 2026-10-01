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

describe("providers", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("renders providers page with initial provider cards", async () => {
    renderAt("/providers");

    expect(
      await screen.findByText("Connect model providers to power agents and tasks. Keys stay on this server."),
    ).toBeTruthy();
    expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();

    expect(await screen.findByTestId("provider-card-provider-anthropic")).toBeTruthy();
    expect(screen.getByTestId("provider-card-provider-ollama")).toBeTruthy();
    expect(screen.getByText("claude-3-7-sonnet")).toBeTruthy();
    expect(screen.getByText("llama3.3:70b")).toBeTruthy();
  });

  it("can add a new cloud provider via multi-step wizard with test connection gate", async () => {
    renderAt("/providers");

    const addBtn = await screen.findByRole("link", { name: "Add provider" });
    act(() => {
      addBtn.click();
    });

    expect(await screen.findByRole("heading", { level: 1, name: "Connect a provider" })).toBeTruthy();

    // Step 1: Select OpenAI
    const openaiBtn = screen.getByRole("button", { name: "OpenAI" });
    act(() => {
      openaiBtn.click();
    });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 2: Credentials & Plan
    expect(await screen.findByText(/OpenAI Credentials & Plan/)).toBeTruthy();
    const apiKeyInput = screen.getByPlaceholderText("sk-...");
    fireEvent.change(apiKeyInput, { target: { value: "sk-proj-test1234" } });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 3: Models & Pricing
    expect(await screen.findByText(/Models & Pricing for OpenAI/)).toBeTruthy();
    expect(screen.getByText("GPT-4o")).toBeTruthy();
    expect(screen.getByText(/in \$2\.50 · out \$10 \/ MTok/)).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 4: Validation & Connect
    expect(await screen.findByText("Review Configuration")).toBeTruthy();
    const connectBtn = screen.getByRole("button", { name: "Connect provider" });
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
      expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
      expect(screen.getByText("OpenAI")).toBeTruthy();
      expect(screen.getByText("gpt-4o")).toBeTruthy();
    });
  });

  it("can add Nebius provider with models & pricing via multi-step wizard", async () => {
    renderAt("/providers/new");

    expect(await screen.findByRole("heading", { level: 1, name: "Connect a provider" })).toBeTruthy();

    // Step 1: Select Nebius
    const nebiusBtn = screen.getByRole("button", { name: "Nebius" });
    act(() => {
      nebiusBtn.click();
    });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 2: Credentials
    expect(await screen.findByText(/Nebius Credentials & Plan/)).toBeTruthy();
    const apiKeyInput = screen.getByPlaceholderText("sk-...");
    fireEvent.change(apiKeyInput, { target: { value: "nebius-key-test-1234" } });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 3: Models & Pricing
    expect(await screen.findByText(/Models & Pricing for Nebius/)).toBeTruthy();
    expect(screen.getByText("DeepSeek-R1")).toBeTruthy();
    expect(screen.getByText(/in \$0\.55 · out \$2\.19 \/ MTok/)).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 4: Validation
    act(() => {
      screen.getByRole("button", { name: "Test Connection" }).click();
    });

    expect(await screen.findByText(/Connection verified/)).toBeTruthy();

    // Submit
    act(() => {
      screen.getByRole("button", { name: "Connect provider" }).click();
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
      expect(screen.getByText("Nebius")).toBeTruthy();
      expect(screen.getByText("deepseek-ai/DeepSeek-R1")).toBeTruthy();
    });
  });

  it("can edit an existing provider via standalone multi-step page", async () => {
    renderAt("/providers");

    const anthropicCard = await screen.findByTestId("provider-card-provider-anthropic");
    const editBtn = within(anthropicCard).getByRole("link", { name: "Edit" });
    act(() => {
      editBtn.click();
    });

    expect(await screen.findByRole("heading", { level: 1, name: /Edit/ })).toBeTruthy();

    // Step 2: Change display name
    const nameInput = screen.getByLabelText(/Provider display name/);
    fireEvent.change(nameInput, { target: { value: "Claude Enterprise" } });

    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 3: Advance to Validation
    act(() => {
      screen.getByRole("button", { name: "Continue" }).click();
    });

    // Step 4: Run test then save
    act(() => {
      screen.getByRole("button", { name: "Test Connection" }).click();
    });

    expect(await screen.findByText(/Connection verified/)).toBeTruthy();

    act(() => {
      screen.getByRole("button", { name: "Save changes" }).click();
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
      expect(screen.getByText("Claude Enterprise")).toBeTruthy();
    });
  });

  it("can disconnect a provider", async () => {
    renderAt("/providers");

    const ollamaCard = await screen.findByTestId("provider-card-provider-ollama");
    act(() => {
      within(ollamaCard).getByRole("button", { name: "Disconnect" }).click();
    });

    const confirm = await screen.findByRole("dialog");
    expect(within(confirm).getByText(/Agents and tasks using models from Ollama Local will pause/)).toBeTruthy();

    act(() => {
      within(confirm).getByRole("button", { name: "Disconnect" }).click();
    });

    await waitFor(() => {
      expect(screen.queryByTestId("provider-card-provider-ollama")).toBeNull();
    });
  });
});
