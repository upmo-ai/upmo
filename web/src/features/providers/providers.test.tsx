import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { aliases, marks } from "@upmo-ai/brands";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { type ProviderAccount } from "../../app/api";
import { AppProviders } from "../../theme";
import { KNOWN_PROVIDERS } from "./providers";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

interface RecordedRequest {
  url: string;
  method: string;
  body: Record<string, unknown> | null;
}

// Puts the page in live mode: GET /api/tasks answers, so the shared client talks
// to fetch instead of the in-memory stub. Every request is recorded
// so tests can assert on the payloads the form sends.
function stubProviderApi(options: { accounts?: ProviderAccount[]; testResponse?: object }) {
  const testResponse = options.testResponse ?? { ok: true, latency_ms: 128 };
  const requests: RecordedRequest[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body =
        typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : null;
      requests.push({ url, method, body });
      // The shared client asks GET /api/tasks first to decide whether a brain is there.
      if (url === "/api/tasks" && method === "GET") {
        return Response.json([]);
      }
      if (url === "/api/providers/test" && method === "POST") {
        return Response.json(testResponse);
      }
      if (url === "/api/providers" && method === "GET") {
        return Response.json(options.accounts ?? []);
      }
      const target = (options.accounts ?? []).find((account) => url === `/api/providers/${account.id}`);
      if (target && method === "PUT") {
        return Response.json({ ...target, ...body });
      }
      return new Response(null, { status: 404 });
    }),
  );
  return requests;
}

const anthropicAccount: ProviderAccount = {
  id: "p1",
  provider: "anthropic",
  name: "Claude Team",
  adapter: "anthropic",
  plan_kind: "metered",
  base_url: "https://api.anthropic.com",
  status: "Connected",
  models: ["claude-3-7-sonnet"],
  scope: "own",
};

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

async function openValidationStep(path = "/providers/new") {
  renderAt(path);
  expect(await screen.findByRole("heading", { level: 1, name: "Connect a provider" })).toBeTruthy();
  clickButton("Ollama");
  await continueTo(/Ollama Credentials & Plan/);
  await continueTo(/Models & Pricing for Ollama/);
  await continueTo("Review Configuration");
}

function continueButton() {
  return screen.getByRole("button", { name: "Continue" });
}

function isDisabled(element: HTMLElement) {
  return element.getAttribute("disabled") !== null;
}

// Walks the wizard for OpenAI, which needs an API key, and stops on Validation.
async function openValidationStepWithKey() {
  renderAt("/providers/new");
  expect(await screen.findByRole("heading", { level: 1, name: "Connect a provider" })).toBeTruthy();
  clickButton("OpenAI");
  await continueTo(/OpenAI Credentials & Plan/);
  fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: "sk-proj-test1234" } });
  await continueTo(/Models & Pricing for OpenAI/);
  await continueTo("Review Configuration");
}

async function expectPassingTest() {
  clickButton("Test connection");
  expect(await screen.findByText("Connection verified")).toBeTruthy();
  expect(isDisabled(screen.getByRole("button", { name: "Connect provider" }))).toBe(false);
}

function expectTestReset() {
  expect(screen.getByText("Ready to test")).toBeTruthy();
  expect(screen.queryByText("Connection verified")).toBeNull();
  expect(isDisabled(screen.getByRole("button", { name: "Connect provider" }))).toBe(true);
}

// The brand mark inside a provider card: a real brand svg, or the generic server glyph.
function cardMark(card: HTMLElement) {
  const mark = card.querySelector("svg.upmo-brandmark");
  if (!mark) throw new Error("provider card has no brand mark");
  return mark;
}

function markMarkup(inner: string) {
  const probe = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  probe.innerHTML = inner;
  return probe.innerHTML;
}

function selectedBadges() {
  const label = screen.getByText(/^Selected \(\d+\):$/);
  return label.parentElement as HTMLElement;
}

async function putBody(requests: RecordedRequest[]) {
  await waitFor(() => {
    expect(requests.some((request) => request.method === "PUT")).toBe(true);
  });
  const put = requests.find((request) => request.method === "PUT");
  if (!put?.body) throw new Error("PUT request has no body");
  return put.body;
}

describe("providers", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
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

  it("shows each provider status on its card with Edit and Disconnect buttons", async () => {
    const statuses: ProviderAccount["status"][] = [
      "Connected",
      "Not connected",
      "Rate limited",
      "Key rejected",
      "Offline",
    ];
    stubProviderApi({
      accounts: statuses.map((status, index) => ({
        id: `p${index}`,
        provider: "openai",
        name: `OpenAI ${index}`,
        adapter: "openai",
        plan_kind: "metered",
        status,
        models: ["gpt-4o"],
        scope: "own",
      })),
    });
    renderAt("/providers");

    for (const [index, status] of statuses.entries()) {
      const card = await screen.findByTestId(`provider-card-p${index}`);
      expect(within(card).getByText(status)).toBeTruthy();
      expect(within(card).getByRole("button", { name: "Edit" })).toBeTruthy();
      expect(within(card).getByRole("button", { name: "Disconnect" })).toBeTruthy();
    }
  });

  it("shows the empty state when no provider is connected", async () => {
    stubProviderApi({ accounts: [] });
    renderAt("/providers");

    expect(await screen.findByRole("heading", { level: 3, name: "Connect a provider" })).toBeTruthy();
    expect(screen.queryByPlaceholderText("Search providers...")).toBeNull();
  });

  it("filters the provider cards and shows the empty search state", async () => {
    renderAt("/providers");

    const search = await screen.findByPlaceholderText("Search providers...");
    fireEvent.change(search, { target: { value: "zzz" } });
    expect(await screen.findByText(/No providers found matching/)).toBeTruthy();
    expect(screen.queryByTestId("provider-card-provider-anthropic")).toBeNull();

    fireEvent.change(search, { target: { value: "llama" } });
    expect(await screen.findByTestId("provider-card-provider-ollama")).toBeTruthy();
    expect(screen.queryByTestId("provider-card-provider-anthropic")).toBeNull();
  });

  it("has a brand mark for every known provider except Nebius", () => {
    const aliasOf: Record<string, string> = aliases;
    const missing = KNOWN_PROVIDERS.filter(({ id }) => {
      const key = id.toLowerCase();
      return !Object.hasOwn(marks, aliasOf[key] ?? key);
    }).map(({ id }) => id);

    expect(missing).toEqual(["nebius"]);
  });

  it("can add a new cloud provider via multi-step wizard with test connection gate", async () => {
    renderAt("/providers");

    const addBtn = await screen.findByRole("link", { name: "Add provider" });
    click(addBtn);

    expect(await screen.findByRole("heading", { level: 1, name: "Connect a provider" })).toBeTruthy();

    // Step 1: Select OpenAI
    clickButton("OpenAI");
    clickButton("Continue");

    // Step 2: Credentials & Plan
    expect(await screen.findByText(/OpenAI Credentials & Plan/)).toBeTruthy();
    const apiKeyInput = screen.getByPlaceholderText("sk-...");
    fireEvent.change(apiKeyInput, { target: { value: "sk-proj-test1234" } });
    clickButton("Continue");

    // Step 3: Models & Pricing
    expect(await screen.findByText(/Models & Pricing for OpenAI/)).toBeTruthy();
    expect(screen.getByText("GPT-4o")).toBeTruthy();
    expect(screen.getByText(/in \$2\.50 · out \$10 \/ MTok/)).toBeTruthy();
    clickButton("Continue");

    // Step 4: Validation & Connect
    expect(await screen.findByText("Review Configuration")).toBeTruthy();
    const connectBtn = screen.getByRole("button", { name: "Connect provider" });
    expect(connectBtn.getAttribute("disabled")).not.toBeNull();

    // Run connection test
    clickButton("Test connection");

    expect(await screen.findByText("Connection verified")).toBeTruthy();
    expect(screen.getByText("Responded in 124 ms.")).toBeTruthy();
    expect(connectBtn.getAttribute("disabled")).toBeNull();

    // Submit
    click(connectBtn);

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
    clickButton("Nebius");
    clickButton("Continue");

    // Step 2: Credentials
    expect(await screen.findByText(/Nebius Credentials & Plan/)).toBeTruthy();
    const apiKeyInput = screen.getByPlaceholderText("sk-...");
    fireEvent.change(apiKeyInput, { target: { value: "nebius-key-test-1234" } });
    clickButton("Continue");

    // Step 3: Models & Pricing
    expect(await screen.findByText(/Models & Pricing for Nebius/)).toBeTruthy();
    expect(screen.getByText("DeepSeek-R1")).toBeTruthy();
    expect(screen.getByText(/in \$0\.55 · out \$2\.19 \/ MTok/)).toBeTruthy();
    clickButton("Continue");

    // Step 4: Validation
    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();

    // Submit
    clickButton("Connect provider");

    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
      expect(screen.getByText("Nebius")).toBeTruthy();
      expect(screen.getByText("deepseek-ai/DeepSeek-R1")).toBeTruthy();
    });
  });

  it("can edit an existing provider via standalone multi-step page", async () => {
    renderAt("/providers");

    const anthropicCard = await screen.findByTestId("provider-card-provider-anthropic");
    click(within(anthropicCard).getByRole("button", { name: "Edit" }));

    expect(await screen.findByRole("heading", { level: 1, name: /Edit/ })).toBeTruthy();

    // Step 2: Change display name
    const nameInput = screen.getByLabelText(/Provider display name/);
    fireEvent.change(nameInput, { target: { value: "Claude Enterprise" } });
    clickButton("Continue");

    // Step 3: Advance to Validation
    clickButton("Continue");

    // Step 4: Run test then save
    clickButton("Test connection");
    expect(await screen.findByText(/Connection verified/)).toBeTruthy();
    clickButton("Save changes");

    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
      expect(screen.getByText("Claude Enterprise")).toBeTruthy();
    });
  });

  it("starts at Credentials with the provider step locked in edit mode", async () => {
    renderAt("/providers");

    const anthropicCard = await screen.findByTestId("provider-card-provider-anthropic");
    click(within(anthropicCard).getByRole("button", { name: "Edit" }));

    expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
    expect(screen.queryByText("Select Provider")).toBeNull();

    const providerStep = screen.getByRole("button", { name: /Choose service/ });
    expect(providerStep.getAttribute("aria-disabled")).toBe("true");
    click(providerStep);
    expect(screen.getByText(/Anthropic Credentials & Plan/)).toBeTruthy();
    expect(screen.queryByText("Select Provider")).toBeNull();

    // Models stays reachable and going back lands on Credentials, never on the provider step.
    await continueTo(/Models & Pricing for Anthropic/);
    clickButton("Back");
    expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Back" })).toBeNull();
    expect(screen.queryByText("Select Provider")).toBeNull();
  });

  it("keeps Connect disabled until the connection test succeeds", async () => {
    await openValidationStep();

    expect(screen.getByText("Ready to test")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect provider" }).getAttribute("disabled")).not.toBeNull();
    expect(
      screen.getByText("You must run a passing connection test before connecting this provider."),
    ).toBeTruthy();

    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect provider" }).getAttribute("disabled")).toBeNull();
  });

  it("shows the error and keeps Connect disabled when the connection test fails", async () => {
    stubProviderApi({
      testResponse: { ok: false, error: "401 Unauthorized: the API key was rejected." },
    });
    await openValidationStep();

    clickButton("Test connection");

    expect(await screen.findByText("Connection failed")).toBeTruthy();
    expect(screen.getByText("401 Unauthorized: the API key was rejected.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect provider" }).getAttribute("disabled")).not.toBeNull();
  });

  it("falls back to a generic message when the failing test has no error text", async () => {
    stubProviderApi({ testResponse: { ok: false } });
    await openValidationStep();

    clickButton("Test connection");

    expect(await screen.findByText("Connection failed")).toBeTruthy();
    expect(screen.getByText("Failed to reach provider. Check your API key or endpoint URL.")).toBeTruthy();
  });

  it("resets the test result when a field changes", async () => {
    await openValidationStep();

    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect provider" }).getAttribute("disabled")).toBeNull();

    // A model toggle on the Models step invalidates the passing test.
    clickButton("Back");
    expect(await screen.findByText(/Models & Pricing for Ollama/)).toBeTruthy();
    click(screen.getByRole("checkbox", { name: "Select Qwen 2.5 Coder 32B (Local)" }));
    await continueTo("Ready to test");
    expect(screen.getByRole("button", { name: "Connect provider" }).getAttribute("disabled")).not.toBeNull();

    clickButton("Test connection");
    expect(await screen.findByText("Connection verified")).toBeTruthy();

    // So does a credentials field.
    clickButton("Back");
    clickButton("Back");
    expect(await screen.findByText(/Ollama Credentials & Plan/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/Base URL/), { target: { value: "http://localhost:11435" } });
    await continueTo(/Models & Pricing for Ollama/);
    await continueTo("Ready to test");
    expect(screen.queryByText("Connection verified")).toBeNull();
    expect(screen.getByRole("button", { name: "Connect provider" }).getAttribute("disabled")).not.toBeNull();
  });

  it("filters the provider tiles and shows the empty state", async () => {
    renderAt("/providers/new");

    const filter = await screen.findByPlaceholderText("Filter providers...");
    fireEvent.change(filter, { target: { value: "zzz" } });
    expect(await screen.findByText(/No providers found matching/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "OpenAI" })).toBeNull();

    fireEvent.change(filter, { target: { value: "gpt" } });
    expect(await screen.findByRole("button", { name: "OpenAI" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Anthropic" })).toBeNull();
  });

  it("names the provider tiles by brand and marks the selected one", async () => {
    renderAt("/providers/new");

    const anthropic = await screen.findByRole("button", { name: "Anthropic" });
    expect(anthropic.getAttribute("aria-pressed")).toBe("true");
    const openai = screen.getByRole("button", { name: "OpenAI" });
    expect(openai.getAttribute("aria-pressed")).toBe("false");

    click(openai);
    expect(screen.getByRole("button", { name: "OpenAI" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Anthropic" }).getAttribute("aria-pressed")).toBe("false");
  });

  it("can disconnect a provider", async () => {
    renderAt("/providers");

    const ollamaCard = await screen.findByTestId("provider-card-provider-ollama");
    click(within(ollamaCard).getByRole("button", { name: "Disconnect" }));

    const confirm = await screen.findByRole("dialog");
    expect(within(confirm).getByText(/Agents and tasks using models from Ollama Local will pause/)).toBeTruthy();

    click(within(confirm).getByRole("button", { name: "Disconnect provider" }));

    await waitFor(() => {
      expect(screen.queryByTestId("provider-card-provider-ollama")).toBeNull();
    });
  });

  it("keeps the provider when the disconnect dialog is cancelled", async () => {
    renderAt("/providers");

    const ollamaCard = await screen.findByTestId("provider-card-provider-ollama");
    click(within(ollamaCard).getByRole("button", { name: "Disconnect" }));

    const confirm = await screen.findByRole("dialog");
    click(within(confirm).getByRole("button", { name: "Cancel" }));

    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    expect(screen.getByTestId("provider-card-provider-ollama")).toBeTruthy();
  });

  it("gives each provider card the brand mark of its provider, not the generic glyph", async () => {
    const account = (id: string, provider: string): ProviderAccount => ({
      ...anthropicAccount,
      id,
      provider,
      name: `Account ${id}`,
    });
    // The ids never equal a brand, so a card that fell back to its id would show the glyph.
    stubProviderApi({
      accounts: [account("a1", "anthropic"), account("a2", "openai"), account("a3", "gemini"), account("a4", "acme-unknown")],
    });
    renderAt("/providers");

    const anthropic = cardMark(await screen.findByTestId("provider-card-a1"));
    const openai = cardMark(screen.getByTestId("provider-card-a2"));
    const gemini = cardMark(screen.getByTestId("provider-card-a3"));
    const unknown = cardMark(screen.getByTestId("provider-card-a4"));

    // Gemini reaches its mark through the alias table.
    const aliasOf: Record<string, string> = aliases;
    const markOf: Record<string, string> = marks;
    expect(anthropic.innerHTML).toBe(markMarkup(markOf.anthropic));
    expect(openai.innerHTML).toBe(markMarkup(markOf.openai));
    expect(gemini.innerHTML).toBe(markMarkup(markOf[aliasOf.gemini]));
    expect(anthropic.innerHTML).not.toBe(openai.innerHTML);
    for (const mark of [anthropic, openai, gemini]) {
      expect(mark.classList.contains("tabler-icon")).toBe(false);
    }

    // A provider without a mark gets the generic server glyph.
    expect(unknown.classList.contains("tabler-icon-server")).toBe(true);
  });

  describe("step gates", () => {
    it("keeps Continue disabled on Credentials until a required API key is typed", async () => {
      renderAt("/providers/new");
      await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
      clickButton("OpenAI");
      await continueTo(/OpenAI Credentials & Plan/);

      expect(isDisabled(continueButton())).toBe(true);

      const apiKey = screen.getByLabelText(/^API Key/);
      fireEvent.change(apiKey, { target: { value: "sk-proj-test1234" } });
      expect(isDisabled(continueButton())).toBe(false);

      // Whitespace is not a key.
      fireEvent.change(apiKey, { target: { value: "   " } });
      expect(isDisabled(continueButton())).toBe(true);

      fireEvent.change(apiKey, { target: { value: "sk-proj-test1234" } });
      expect(isDisabled(continueButton())).toBe(false);
    });

    it("keeps Continue disabled when the provider display name is empty", async () => {
      renderAt("/providers/new");
      await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
      clickButton("Ollama");
      await continueTo(/Ollama Credentials & Plan/);

      expect(isDisabled(continueButton())).toBe(false);
      fireEvent.change(screen.getByLabelText(/Provider display name/), { target: { value: "  " } });
      expect(isDisabled(continueButton())).toBe(true);
    });

    it("keeps Continue disabled for a local provider with an empty Base URL", async () => {
      renderAt("/providers/new");
      await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
      clickButton("Ollama");
      await continueTo(/Ollama Credentials & Plan/);

      // Ollama needs no key, so the Base URL is the only credential field.
      expect(screen.queryByLabelText(/^API Key/)).toBeNull();
      const baseUrl = screen.getByLabelText(/Base URL/);
      expect(isDisabled(continueButton())).toBe(false);

      fireEvent.change(baseUrl, { target: { value: "" } });
      expect(isDisabled(continueButton())).toBe(true);

      fireEvent.change(baseUrl, { target: { value: "   " } });
      expect(isDisabled(continueButton())).toBe(true);

      fireEvent.change(baseUrl, { target: { value: "http://localhost:11434" } });
      expect(isDisabled(continueButton())).toBe(false);
    });

    it("keeps Continue disabled on Models when no model is selected", async () => {
      renderAt("/providers/new");
      await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
      clickButton("Ollama");
      await continueTo(/Ollama Credentials & Plan/);
      await continueTo(/Models & Pricing for Ollama/);

      expect(isDisabled(continueButton())).toBe(false);

      const checked = screen.getAllByRole("checkbox").filter((box) => (box as HTMLInputElement).checked);
      expect(checked.length).toBeGreaterThan(0);
      for (const box of checked) click(box);

      expect(screen.getByText("Selected (0):")).toBeTruthy();
      expect(isDisabled(continueButton())).toBe(true);

      // Selecting one model opens the gate again.
      click(screen.getAllByRole("checkbox")[0]);
      expect(isDisabled(continueButton())).toBe(false);
    });

    it("does not let a later step be reached by clicking it", async () => {
      renderAt("/providers/new");
      await screen.findByRole("heading", { level: 1, name: "Connect a provider" });

      // From Provider, neither Models nor Validation can be jumped to.
      expect(screen.getByText("Select Provider")).toBeTruthy();
      click(screen.getByRole("button", { name: /Select models/ }));
      click(screen.getByRole("button", { name: /Test connection/ }));
      expect(screen.getByText("Select Provider")).toBeTruthy();
      expect(screen.queryByText(/Models & Pricing for/)).toBeNull();
      expect(screen.queryByText("Review Configuration")).toBeNull();

      // From Credentials with the API key still empty, Continue is disabled and the later steps stay locked.
      clickButton("OpenAI");
      await continueTo(/OpenAI Credentials & Plan/);
      expect(isDisabled(continueButton())).toBe(true);
      click(screen.getByRole("button", { name: /Select models/ }));
      click(screen.getByRole("button", { name: /Test connection/ }));
      expect(screen.getByText(/OpenAI Credentials & Plan/)).toBeTruthy();
      expect(screen.queryByText(/Models & Pricing for/)).toBeNull();
      expect(screen.queryByText("Review Configuration")).toBeNull();

      // Going back through the stepper stays possible.
      click(screen.getByRole("button", { name: /Choose service/ }));
      expect(await screen.findByText("Select Provider")).toBeTruthy();
    });
  });

  describe("edit payload", () => {
    async function saveEdit(apiKey?: string) {
      const requests = stubProviderApi({ accounts: [anthropicAccount] });
      renderAt("/providers/p1/edit");

      expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
      if (apiKey !== undefined) {
        fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: apiKey } });
      }
      await continueTo(/Models & Pricing for Anthropic/);
      await continueTo("Review Configuration");
      clickButton("Test connection");
      expect(await screen.findByText("Connection verified")).toBeTruthy();
      clickButton("Save changes");
      return putBody(requests);
    }

    it("does not send api_key when no key was typed", async () => {
      const body = await saveEdit();

      expect(Object.hasOwn(body, "api_key")).toBe(false);
      expect(body.name).toBe("Claude Team");
      expect(body.models).toEqual(["claude-3-7-sonnet"]);
    });

    it("does not send api_key when only whitespace was typed", async () => {
      const body = await saveEdit("   ");

      expect(Object.hasOwn(body, "api_key")).toBe(false);
    });

    it("sends api_key when a new key was typed", async () => {
      const body = await saveEdit("sk-ant-new-key-5678");

      expect(body.api_key).toBe("sk-ant-new-key-5678");
      expect(body.name).toBe("Claude Team");
    });

    it("sends the new key to the connection test as well", async () => {
      const requests = stubProviderApi({ accounts: [anthropicAccount] });
      renderAt("/providers/p1/edit");
      expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
      fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: "sk-ant-new-key-5678" } });
      await continueTo(/Models & Pricing for Anthropic/);
      await continueTo("Review Configuration");
      clickButton("Test connection");
      expect(await screen.findByText("Connection verified")).toBeTruthy();

      const probe = requests.find((request) => request.url === "/api/providers/test");
      expect(probe?.body?.api_key).toBe("sk-ant-new-key-5678");
      expect(probe?.body?.provider).toBe("anthropic");
    });
  });

  describe("test reset", () => {
    it("clears a passing test when the provider display name changes", async () => {
      await openValidationStepWithKey();
      await expectPassingTest();

      clickButton("Back");
      clickButton("Back");
      expect(await screen.findByText(/OpenAI Credentials & Plan/)).toBeTruthy();
      fireEvent.change(screen.getByLabelText(/Provider display name/), { target: { value: "OpenAI Staging" } });
      await continueTo(/Models & Pricing for OpenAI/);
      await continueTo("Review Configuration");

      expect(screen.getByText("OpenAI Staging")).toBeTruthy();
      expectTestReset();
    });

    it("clears a passing test when the API key changes", async () => {
      await openValidationStepWithKey();
      await expectPassingTest();

      clickButton("Back");
      clickButton("Back");
      expect(await screen.findByText(/OpenAI Credentials & Plan/)).toBeTruthy();
      fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: "sk-proj-rotated-9999" } });
      await continueTo(/Models & Pricing for OpenAI/);
      await continueTo("Review Configuration");

      expectTestReset();
    });

    it("clears a passing test when the plan changes", async () => {
      await openValidationStepWithKey();
      await expectPassingTest();

      clickButton("Back");
      clickButton("Back");
      expect(await screen.findByText(/OpenAI Credentials & Plan/)).toBeTruthy();
      // Same convention as the members tests: the listbox is queried even while Mantine keeps it hidden.
      const listbox = screen.getByRole("listbox", { name: "Plan kind", hidden: true });
      click(within(listbox).getByRole("option", { name: "Subscription", hidden: true }));
      await continueTo(/Models & Pricing for OpenAI/);
      await continueTo("Review Configuration");

      expect(screen.getByText(/Plan: subscription/)).toBeTruthy();
      expectTestReset();
    });

    it("clears a passing test when a custom model is added", async () => {
      await openValidationStepWithKey();
      await expectPassingTest();

      clickButton("Back");
      expect(await screen.findByText(/Models & Pricing for OpenAI/)).toBeTruthy();
      fireEvent.change(screen.getByLabelText(/Add Custom Model Identifier/), {
        target: { value: "ft:gpt-4o:acme" },
      });
      clickButton("Add");
      await continueTo("Review Configuration");

      expect(screen.getByText("ft:gpt-4o:acme")).toBeTruthy();
      expectTestReset();
    });

    it("clears a passing test when a custom model is removed", async () => {
      await openValidationStepWithKey();
      clickButton("Back");
      expect(await screen.findByText(/Models & Pricing for OpenAI/)).toBeTruthy();
      fireEvent.change(screen.getByLabelText(/Add Custom Model Identifier/), {
        target: { value: "ft:gpt-4o:acme" },
      });
      clickButton("Add");
      await continueTo("Review Configuration");
      await expectPassingTest();

      clickButton("Back");
      expect(await screen.findByText(/Models & Pricing for OpenAI/)).toBeTruthy();
      click(screen.getByRole("checkbox", { name: "Select ft:gpt-4o:acme" }));
      await continueTo("Review Configuration");

      expect(screen.queryByText("ft:gpt-4o:acme")).toBeNull();
      expectTestReset();
    });

    it("keeps a passing test when nothing changed", async () => {
      await openValidationStepWithKey();
      await expectPassingTest();

      clickButton("Back");
      expect(await screen.findByText(/Models & Pricing for OpenAI/)).toBeTruthy();
      await continueTo("Review Configuration");

      expect(screen.getByText("Connection verified")).toBeTruthy();
      expect(isDisabled(screen.getByRole("button", { name: "Connect provider" }))).toBe(false);
    });
  });

  describe("custom models", () => {
    async function openModelsStep() {
      renderAt("/providers/new");
      await screen.findByRole("heading", { level: 1, name: "Connect a provider" });
      clickButton("Ollama");
      await continueTo(/Ollama Credentials & Plan/);
      await continueTo(/Models & Pricing for Ollama/);
      return screen.getByLabelText(/Add Custom Model Identifier/) as HTMLInputElement;
    }

    it("adds a model with the Add button and lists it as selected", async () => {
      const input = await openModelsStep();
      expect(screen.getByText("Selected (2):")).toBeTruthy();

      fireEvent.change(input, { target: { value: "  qwen2.5:7b  " } });
      clickButton("Add");

      expect(screen.getByText("Selected (3):")).toBeTruthy();
      // The id is trimmed and shown in the table and in the Selected badges.
      expect(within(selectedBadges()).getByText("qwen2.5:7b")).toBeTruthy();
      expect(screen.getByText("Custom Model")).toBeTruthy();
      expect((screen.getByRole("checkbox", { name: "Select qwen2.5:7b" }) as HTMLInputElement).checked).toBe(true);
      // The input is cleared for the next entry.
      expect(input.value).toBe("");
    });

    it("adds a model by pressing Enter", async () => {
      const input = await openModelsStep();

      fireEvent.change(input, { target: { value: "mistral-nemo:12b" } });
      fireEvent.keyDown(input, { key: "Enter" });

      expect(screen.getByText("Selected (3):")).toBeTruthy();
      expect(within(selectedBadges()).getByText("mistral-nemo:12b")).toBeTruthy();
      expect(input.value).toBe("");
    });

    it("ignores blank input and duplicates", async () => {
      const input = await openModelsStep();

      fireEvent.change(input, { target: { value: "   " } });
      clickButton("Add");
      expect(screen.getByText("Selected (2):")).toBeTruthy();

      fireEvent.change(input, { target: { value: "phi4:14b" } });
      clickButton("Add");
      fireEvent.change(input, { target: { value: "phi4:14b" } });
      clickButton("Add");
      expect(screen.getByText("Selected (3):")).toBeTruthy();
      expect(within(selectedBadges()).getAllByText("phi4:14b")).toHaveLength(1);

      // A model that is already in the catalog is not added a second time either.
      fireEvent.change(input, { target: { value: "llama3.3:70b" } });
      clickButton("Add");
      expect(screen.getByText("Selected (3):")).toBeTruthy();
    });

    it("removes a custom model by unchecking it", async () => {
      const input = await openModelsStep();
      fireEvent.change(input, { target: { value: "phi4:14b" } });
      fireEvent.keyDown(input, { key: "Enter" });
      expect(within(selectedBadges()).getByText("phi4:14b")).toBeTruthy();

      click(screen.getByRole("checkbox", { name: "Select phi4:14b" }));

      expect(screen.getByText("Selected (2):")).toBeTruthy();
      expect(within(selectedBadges()).queryByText("phi4:14b")).toBeNull();
      expect(screen.queryByRole("checkbox", { name: "Select phi4:14b" })).toBeNull();
    });

    it("carries the custom model into the review and the saved provider", async () => {
      const input = await openModelsStep();
      fireEvent.change(input, { target: { value: "phi4:14b" } });
      fireEvent.keyDown(input, { key: "Enter" });
      await continueTo("Review Configuration");

      expect(screen.getByText("3 models configured")).toBeTruthy();
      expect(screen.getByText("phi4:14b")).toBeTruthy();

      clickButton("Test connection");
      expect(await screen.findByText("Connection verified")).toBeTruthy();
      clickButton("Connect provider");

      await waitFor(() => {
        expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
        expect(screen.getByText("phi4:14b")).toBeTruthy();
      });
    });
  });
});
