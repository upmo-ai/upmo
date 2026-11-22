import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { aliases, marks } from "@upmo-ai/brands";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { type ProviderAccount, type ProviderVendor } from "../../app/api";
import { AppProviders } from "../../theme";

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

// The vendors, catalog and model list the brain answers with. The ids are
// made up: the screens read them from the brain and know none of their own.
const vendors: ProviderVendor[] = [
  { key: "anthropic", name: "Anthropic", adapter: "anthropic", default_base_url: "https://api.anthropic.com", suggested_plan: "metered" },
  { key: "openai", name: "OpenAI", adapter: "openai_compat", default_base_url: "https://api.openai.com", suggested_plan: "metered" },
  { key: "nebius", name: "Nebius", adapter: "openai_compat", default_base_url: "https://api.tokenfactory.nebius.com", suggested_plan: "metered" },
  { key: "azure", name: "Azure OpenAI", adapter: "openai_compat", default_base_url: "", suggested_plan: "metered" },
  { key: "bedrock-mantle", name: "Bedrock Mantle", adapter: "openai_compat", default_base_url: "", suggested_plan: "metered" },
  { key: "ollama", name: "Ollama", adapter: "openai_compat", default_base_url: "http://localhost:11434", suggested_plan: "local" },
];

const catalogRow = {
  vendor: "openai",
  model: "model-b",
  kind: "chat",
  inputs: ["text"],
  outputs: ["text"],
  context_window: 128000,
  input_usd_per_mtok: 2.5,
  output_usd_per_mtok: 10,
  cache_read_usd_per_mtok: null,
  cache_write_usd_per_mtok: null,
};

const listedModels = [
  { id: "model-a", display_name: "Model A", pricing: { prompt: "0.000003", completion: "0.000015" } },
  { id: "model-b" },
  { id: "model-c" },
];

const own = {
  visibility: "private" as const,
  team_ids: [] as string[],
  team_names: [] as string[],
  can_manage: true,
  can_use: true,
  is_owner: true,
};

const anthropicAccount: ProviderAccount = {
  id: "p1",
  provider: "anthropic",
  name: "Claude Team",
  adapter: "anthropic",
  plan_kind: "metered",
  base_url: "https://api.anthropic.com",
  status: "Connected",
  models: ["model-a"],
  usable: true,
  usable_models: ["model-a"],
  enabled: true,
  key_hint: "9f3c",
  key_changed_at: "2026-11-02T10:00:00Z",
  ...own,
};

const localAccount: ProviderAccount = {
  id: "p2",
  provider: "ollama",
  name: "Office Ollama",
  adapter: "openai_compat",
  plan_kind: "local",
  base_url: "http://localhost:11434",
  status: "Connected",
  models: ["model-c"],
  usable: true,
  usable_models: ["model-c"],
  enabled: true,
  ...own,
};

/** The pricing view as the brain sends it, for one model with optional override rates in micro-USD. */
function pricingView(model: string, override?: { input?: number; output?: number }, warnings: unknown[] = []) {
  const usd = (micro: number | undefined, fallback: number) => (micro === undefined ? fallback : micro / 1_000_000);
  return {
    merged: {
      model,
      input_usd_per_mtok: usd(override?.input, 3),
      output_usd_per_mtok: usd(override?.output, 15),
      sources: {
        input_usd_per_mtok: override?.input === undefined ? "provider" : "override",
        output_usd_per_mtok: override?.output === undefined ? "models_dev" : "override",
        cache_read_usd_per_mtok: "none",
        cache_write_usd_per_mtok: "other_provider",
      },
      cache_write_usd_per_mtok: 0.3,
    },
    ...(override
      ? {
          override: {
            model,
            input_micro_usd_per_mtok: override.input ?? null,
            output_micro_usd_per_mtok: override.output ?? null,
            cache_read_micro_usd_per_mtok: null,
            cache_write_micro_usd_per_mtok: null,
            updated_by: "m-ada",
            updated_at: "2026-11-10T09:00:00Z",
          },
        }
      : {}),
    warnings,
  };
}

// Puts the page in live mode: GET /api/tasks answers, so the shared client talks
// to fetch instead of the in-memory stub. Every request is recorded so tests
// can assert on the payloads the screens send.
function stubProviderApi(
  options: {
    accounts?: ProviderAccount[];
    testResponse?: object;
    canShare?: boolean;
    uses?: object;
    usesFail?: string[];
    pricing?: (url: string, method: string, body: Record<string, unknown> | null) => object;
  } = {},
) {
  const accounts = (options.accounts ?? [anthropicAccount, localAccount]).map((account) => ({ ...account }));
  const testResponse = options.testResponse ?? { ok: true, models: listedModels };
  const requests: RecordedRequest[] = [];
  let serial = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : null;
      requests.push({ url, method, body });
      // The shared client asks GET /api/tasks first to decide whether a brain is there.
      if (url === "/api/tasks" && method === "GET") return Response.json([]);
      if (url === "/api/members" && method === "GET") {
        return Response.json([{ id: "m-ada", name: "Ada", email: "ada@example.com", role: { system: "owner" }, teams: [] }]);
      }
      if (url === "/api/providers/sharing") return Response.json({ can_share: options.canShare ?? false });
      if (url === "/api/providers/vendors") return Response.json(vendors);
      if (url.startsWith("/api/providers/catalog")) {
        return Response.json(url.endsWith("vendor=openai") ? [catalogRow] : []);
      }
      if (url === "/api/providers/test" && method === "POST") return Response.json(testResponse);
      if (url === "/api/providers" && method === "GET") return Response.json(accounts);
      if (url === "/api/providers" && method === "POST") {
        serial += 1;
        const made = { ...anthropicAccount, ...body, id: `made-${serial}`, status: "Not connected", key_hint: undefined };
        accounts.push(made as ProviderAccount);
        return Response.json(made);
      }
      const account = accounts.find((item) => url.startsWith(`/api/providers/${item.id}`));
      if (account) {
        const rest = url.slice(`/api/providers/${account.id}`.length);
        if (rest === "" && method === "PUT") {
          Object.assign(account, body);
          if (body?.enabled === false) account.usable_models = [];
          return Response.json(account);
        }
        if (rest === "" && method === "DELETE") {
          accounts.splice(accounts.indexOf(account), 1);
          return new Response(null, { status: 204 });
        }
        if (rest === "/models") return Response.json(listedModels.slice(0, 1));
        if (rest === "/pricing-map" && method === "PUT") {
          if (Object.hasOwn(body ?? {}, "pricing_provider")) account.pricing_provider = String(body?.pricing_provider ?? "");
          account.pricing_models = { ...(account.pricing_models ?? {}) };
          const models = body?.models as Record<string, string | null> | undefined;
          for (const [model, mapped] of Object.entries(models ?? {})) {
            if (mapped === null) delete account.pricing_models[model];
            else account.pricing_models[model] = mapped;
          }
          return new Response(null, { status: 204 });
        }
        if (rest === "/uses" && options.usesFail?.includes(account.id)) {
          return Response.json({ error: "uses unavailable" }, { status: 500 });
        }
        if (rest === "/uses") return Response.json(options.uses ?? { routes: [], agents: [], automations: [] });
        if (rest.startsWith("/pricing") || rest === "/overrides") {
          const model = rest.startsWith("/pricing") ? decodeURIComponent(rest.split("model=")[1] ?? "") : String(body?.model);
          return Response.json(options.pricing?.(url, method, body) ?? pricingView(model));
        }
      }
      return new Response(null, { status: 404 });
    }),
  );
  return requests;
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

function continueButton() {
  return screen.getByRole("button", { name: "Continue" });
}

function isDisabled(element: HTMLElement) {
  return element.getAttribute("disabled") !== null;
}

/** Walks the wizard for a vendor to the Test step. */
async function openTestStep(vendor: string, key?: string) {
  renderAt("/settings/providers/new");
  expect(await screen.findByRole("heading", { level: 1, name: "Connect a provider" })).toBeTruthy();
  click(await screen.findByRole("button", { name: vendor }));
  await continueTo(new RegExp(`${vendor} Credentials & Plan`));
  if (key !== undefined) fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: key } });
  await continueTo("Connection Verification");
}

async function passTest() {
  clickButton("Test connection");
  expect(await screen.findByText("Connection verified")).toBeTruthy();
}

/** Walks the wizard through a passing test to the models list. */
async function openModelsStep(vendor = "OpenAI", key: string | undefined = "sk-proj-test1234") {
  await openTestStep(vendor, key);
  await passTest();
  await continueTo(new RegExp(`Models & Pricing for ${vendor}`));
}

function selectedBadges() {
  const label = screen.getByText(/^Selected \(\d+\):$/);
  return label.parentElement as HTMLElement;
}

async function sentBody(requests: RecordedRequest[], method: string, url: string) {
  await waitFor(() => {
    expect(requests.some((request) => request.method === method && request.url === url)).toBe(true);
  });
  const found = requests.find((request) => request.method === method && request.url === url);
  if (!found?.body) throw new Error(`${method} ${url} has no body`);
  return found.body;
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

describe("providers", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists the accounts the brain sends, with the key hint line", async () => {
    stubProviderApi();
    renderAt("/settings/providers");

    expect(await screen.findByTestId("provider-card-p1")).toBeTruthy();
    expect(screen.getByTestId("provider-card-p2")).toBeTruthy();
    expect(screen.getByText("Key ending 9f3c, set 2 Nov 2026")).toBeTruthy();
  });

  it("sends /providers to /settings/providers", async () => {
    stubProviderApi();
    renderAt("/providers");

    expect(await screen.findByTestId("provider-card-p1")).toBeTruthy();
    const link = screen.getAllByRole("link", { name: "Providers" })[0];
    expect(link?.getAttribute("href")).toBe("/settings/providers");
  });

  it("shows each provider status on its card with Edit and Disconnect buttons", async () => {
    const statuses: ProviderAccount["status"][] = ["Connected", "Not connected", "Rate limited", "Key rejected", "Offline"];
    stubProviderApi({
      accounts: statuses.map((status, index) => ({
        ...anthropicAccount,
        id: `p${index}`,
        name: `OpenAI ${index}`,
        status,
      })),
    });
    renderAt("/settings/providers");

    for (const [index, status] of statuses.entries()) {
      const card = await screen.findByTestId(`provider-card-p${index}`);
      expect(within(card).getByText(status)).toBeTruthy();
      expect(within(card).getByRole("button", { name: "Edit" })).toBeTruthy();
      expect(within(card).getByRole("button", { name: "Disconnect" })).toBeTruthy();
    }
  });

  it("shows when the workspace turned an account off and when a model was retired", async () => {
    stubProviderApi({
      accounts: [
        {
          ...anthropicAccount,
          status: "Turned off by your workspace",
          enabled_models: [
            { id: "model-a", listed: true, retired: true },
            { id: "model-b", listed: true, retired: false },
          ],
        },
      ],
    });
    renderAt("/settings/providers");

    const card = await screen.findByTestId("provider-card-p1");
    expect(within(card).getByText("Not connected")).toBeTruthy();
    expect(screen.getByText("Turned off by your workspace")).toBeTruthy();
    expect(screen.getByText("Retired")).toBeTruthy();
    expect(screen.getByText("model-a no longer listed by the provider.")).toBeTruthy();
  });

  it("shows the empty state when no provider is connected", async () => {
    stubProviderApi({ accounts: [] });
    renderAt("/settings/providers");

    expect(await screen.findByRole("heading", { level: 3, name: "Connect a provider" })).toBeTruthy();
    expect(screen.queryByPlaceholderText("Search providers...")).toBeNull();
  });

  it("filters the provider cards and shows the empty search state", async () => {
    stubProviderApi();
    renderAt("/settings/providers");

    const search = await screen.findByPlaceholderText("Search providers...");
    fireEvent.change(search, { target: { value: "zzz" } });
    expect(await screen.findByText(/No providers found matching/)).toBeTruthy();
    expect(screen.queryByTestId("provider-card-p1")).toBeNull();

    fireEvent.change(search, { target: { value: "model-c" } });
    expect(await screen.findByTestId("provider-card-p2")).toBeTruthy();
    expect(screen.queryByTestId("provider-card-p1")).toBeNull();
  });

  it("offers the brain's vendors as tiles, marks the first and filters them", async () => {
    stubProviderApi();
    renderAt("/settings/providers/new");

    const anthropic = await screen.findByRole("button", { name: "Anthropic" });
    expect(anthropic.getAttribute("aria-pressed")).toBe("true");
    click(screen.getByRole("button", { name: "OpenAI" }));
    expect(screen.getByRole("button", { name: "OpenAI" }).getAttribute("aria-pressed")).toBe("true");

    const filter = screen.getByPlaceholderText("Filter providers...");
    fireEvent.change(filter, { target: { value: "zzz" } });
    expect(await screen.findByText(/No providers found matching/)).toBeTruthy();
    fireEvent.change(filter, { target: { value: "neb" } });
    expect(await screen.findByRole("button", { name: "Nebius" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Anthropic" })).toBeNull();
  });

  it("has a brand mark for every vendor except Nebius and Bedrock Mantle", () => {
    const aliasOf: Record<string, string> = aliases;
    const missing = vendors
      .map(({ key }) => key)
      .filter((key) => !Object.hasOwn(marks, aliasOf[key] ?? key));

    expect(missing).toEqual(["nebius", "bedrock-mantle"]);
  });

  it("suggests the vendor's plan with one line under each choice", async () => {
    stubProviderApi();
    renderAt("/settings/providers/new");
    click(await screen.findByRole("button", { name: "Ollama" }));
    await continueTo(/Ollama Credentials & Plan/);

    expect((screen.getByRole("radio", { name: /Local \(suggested\)/ }) as HTMLInputElement).checked).toBe(true);
    expect(screen.getByRole("radio", { name: /^Metered/ })).toBeTruthy();
    expect(screen.getByText("Pay as you go, priced per token.")).toBeTruthy();
    expect(screen.getByText("A flat plan you pay the provider for.")).toBeTruthy();
    expect(screen.getByText("Self-hosted. A key is optional.")).toBeTruthy();
  });

  it("adds an account: test, pick from the listed models with their price and source, save, then test the saved account", async () => {
    const requests = stubProviderApi();
    await openTestStep("OpenAI", "sk-proj-test1234");
    expect(isDisabled(continueButton())).toBe(true);
    await passTest();
    expect(screen.getByText("The provider listed 3 models.")).toBeTruthy();
    await continueTo(/Models & Pricing for OpenAI/);

    // The provider's own rates first, then models.dev, else unknown.
    const rowOf = (id: string) => screen.getByRole("checkbox", { name: `Select ${id}` }).closest("tr") as HTMLElement;
    expect(within(rowOf("model-a")).getByText("in $3.00 · out $15.00 / MTok")).toBeTruthy();
    expect(within(rowOf("model-a")).getByText("Provider")).toBeTruthy();
    expect(within(rowOf("model-b")).getByText("in $2.50 · out $10.00 / MTok")).toBeTruthy();
    expect(within(rowOf("model-b")).getByText("models.dev")).toBeTruthy();
    expect(within(rowOf("model-c")).getByText("Unpriced")).toBeTruthy();
    expect(within(rowOf("model-c")).getByText("Unknown")).toBeTruthy();

    // Nothing is ticked until the person ticks it.
    expect(isDisabled(screen.getByRole("button", { name: "Connect provider" }))).toBe(true);
    click(screen.getByRole("checkbox", { name: "Select model-a" }));
    click(screen.getByRole("checkbox", { name: "Select model-b" }));
    expect(screen.getByText("Selected (2):")).toBeTruthy();
    clickButton("Connect provider");

    const body = await sentBody(requests, "POST", "/api/providers");
    expect(body.models).toEqual(["model-a", "model-b"]);
    expect(body.adapter).toBe("openai_compat");
    expect(body.api_key).toBe("sk-proj-test1234");
    // The saved account is tested once by id, so the brain stores its status and models.
    await waitFor(() => {
      const probes = requests.filter((request) => request.url === "/api/providers/test");
      expect(probes.at(-1)?.body).toEqual({ provider: "openai", id: "made-1" });
    });
    expect(await screen.findByRole("heading", { level: 1, name: "Providers" })).toBeTruthy();
  });

  it("searches the listed models", async () => {
    stubProviderApi();
    await openModelsStep();

    fireEvent.change(screen.getByLabelText("Search models"), { target: { value: "zzz" } });
    expect(await screen.findByText("No models match “zzz”.")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Search models"), { target: { value: "Model A" } });
    expect(screen.getByRole("checkbox", { name: "Select model-a" })).toBeTruthy();
    expect(screen.queryByRole("checkbox", { name: "Select model-b" })).toBeNull();
  });

  it("keeps the models step locked until a test passes, and shows a failing test's sentence", async () => {
    stubProviderApi({ testResponse: { ok: false, error: "The key was rejected." } });
    await openTestStep("OpenAI", "sk-proj-test1234");

    expect(screen.getByText("You must run a passing connection test before choosing models.")).toBeTruthy();
    clickButton("Test connection");
    expect(await screen.findByText("Connection failed")).toBeTruthy();
    expect(screen.getByText("The key was rejected.")).toBeTruthy();
    expect(isDisabled(continueButton())).toBe(true);
  });

  it("falls back to a generic message when the failing test has no error text", async () => {
    stubProviderApi({ testResponse: { ok: false } });
    await openTestStep("Ollama");

    clickButton("Test connection");
    expect(await screen.findByText("Connection failed")).toBeTruthy();
    expect(screen.getByText("Failed to reach provider. Check your API key or endpoint URL.")).toBeTruthy();
  });

  it("clears a passing test when a credential changes, and keeps it when nothing did", async () => {
    stubProviderApi();
    await openTestStep("OpenAI", "sk-proj-test1234");
    await passTest();

    clickButton("Back");
    expect(await screen.findByText(/OpenAI Credentials & Plan/)).toBeTruthy();
    await continueTo("Connection Verification");
    expect(screen.getByText("Connection verified")).toBeTruthy();

    for (const change of [
      () => fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: "sk-proj-rotated-9999" } }),
      () => fireEvent.change(screen.getByLabelText(/Base URL/), { target: { value: "https://proxy.example.com" } }),
      () => click(screen.getByRole("radio", { name: /^Subscription/ })),
    ]) {
      clickButton("Back");
      expect(await screen.findByText(/OpenAI Credentials & Plan/)).toBeTruthy();
      change();
      await continueTo("Connection Verification");
      expect(screen.getByText("Ready to test")).toBeTruthy();
      expect(isDisabled(continueButton())).toBe(true);
      await passTest();
    }
  });

  describe("step gates", () => {
    it.each([
      ["Azure OpenAI", "https://{resource}.openai.azure.com/openai/v1", "https://endpoint.openai.azure.com/openai/v1", "Required. Paste the Azure OpenAI base URL including /openai/v1, without the chat path."],
      ["Bedrock Mantle", "https://bedrock-mantle.{region}.api.aws/v1", "https://bedrock-mantle.us-east-1.api.aws/v1", "Required. Paste the Bedrock Mantle base URL including /v1, without the chat path."],
    ])("requires the documented base URL for %s", async (vendor, placeholder, endpoint, help) => {
      stubProviderApi();
      renderAt("/settings/providers/new");
      click(await screen.findByRole("button", { name: vendor }));
      await continueTo(new RegExp(`${vendor} Credentials & Plan`));

      const baseUrl = screen.getByLabelText(/Base URL/) as HTMLInputElement;
      expect(baseUrl.placeholder).toBe(placeholder);
      expect(screen.getByText(help)).toBeTruthy();
      expect(screen.queryByText(/api.example.com/)).toBeNull();
      expect(isDisabled(continueButton())).toBe(true);
      fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: "sk-test-key" } });
      expect(isDisabled(continueButton())).toBe(true);
      fireEvent.change(baseUrl, { target: { value: endpoint } });
      expect(isDisabled(continueButton())).toBe(false);
    });

    it("keeps Continue disabled on Credentials until a required API key is typed", async () => {
      stubProviderApi();
      renderAt("/settings/providers/new");
      click(await screen.findByRole("button", { name: "OpenAI" }));
      await continueTo(/OpenAI Credentials & Plan/);

      expect(isDisabled(continueButton())).toBe(true);
      const apiKey = screen.getByLabelText(/^API Key/);
      fireEvent.change(apiKey, { target: { value: "sk-proj-test1234" } });
      expect(isDisabled(continueButton())).toBe(false);
      // Whitespace is not a key.
      fireEvent.change(apiKey, { target: { value: "   " } });
      expect(isDisabled(continueButton())).toBe(true);
    });

    it("keeps Continue disabled when the provider display name is empty", async () => {
      stubProviderApi();
      renderAt("/settings/providers/new");
      click(await screen.findByRole("button", { name: "Ollama" }));
      await continueTo(/Ollama Credentials & Plan/);

      expect(isDisabled(continueButton())).toBe(false);
      fireEvent.change(screen.getByLabelText(/Provider display name/), { target: { value: "  " } });
      expect(isDisabled(continueButton())).toBe(true);
    });

    it("keeps Continue disabled for a local provider with an empty Base URL", async () => {
      stubProviderApi();
      renderAt("/settings/providers/new");
      click(await screen.findByRole("button", { name: "Ollama" }));
      await continueTo(/Ollama Credentials & Plan/);

      // Ollama needs no key, so the Base URL is the only credential field.
      expect(screen.queryByLabelText(/^API Key/)).toBeNull();
      const baseUrl = screen.getByLabelText(/Base URL/);
      fireEvent.change(baseUrl, { target: { value: "   " } });
      expect(isDisabled(continueButton())).toBe(true);
      fireEvent.change(baseUrl, { target: { value: "http://localhost:11434" } });
      expect(isDisabled(continueButton())).toBe(false);
    });

    it("keeps Connect disabled on Models when no model is ticked", async () => {
      stubProviderApi();
      await openModelsStep();

      const connect = screen.getByRole("button", { name: "Connect provider" });
      expect(isDisabled(connect)).toBe(true);
      click(screen.getByRole("checkbox", { name: "Select model-c" }));
      expect(isDisabled(connect)).toBe(false);
      click(screen.getByRole("checkbox", { name: "Select model-c" }));
      expect(isDisabled(connect)).toBe(true);
    });

    it("does not let a later step be reached by clicking it", async () => {
      stubProviderApi();
      renderAt("/settings/providers/new");
      expect(await screen.findByText("Select Provider")).toBeTruthy();

      click(screen.getByRole("button", { name: /Select models/ }));
      expect(screen.getByText("Select Provider")).toBeTruthy();
      expect(screen.queryByText(/Models & Pricing for/)).toBeNull();

      // Going back through the stepper stays possible.
      click(screen.getByRole("button", { name: "OpenAI" }));
      await continueTo(/OpenAI Credentials & Plan/);
      click(screen.getByRole("button", { name: /Choose service/ }));
      expect(await screen.findByText("Select Provider")).toBeTruthy();
    });
  });

  describe("edit", () => {
    async function saveEdit(apiKey?: string) {
      const requests = stubProviderApi({ accounts: [anthropicAccount] });
      renderAt("/settings/providers/p1/edit");

      expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
      if (apiKey !== undefined) {
        fireEvent.change(screen.getByLabelText(/^API Key/), { target: { value: apiKey } });
      }
      await continueTo("Connection Verification");
      await passTest();
      await continueTo(/Models & Pricing for Anthropic/);
      clickButton("Save changes");
      return { body: await sentBody(requests, "PUT", "/api/providers/p1"), requests };
    }

    it("starts at Credentials with the provider step locked and names the saved key", async () => {
      stubProviderApi({ accounts: [anthropicAccount] });
      renderAt("/settings/providers/p1/edit");

      expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
      expect(screen.queryByText("Select Provider")).toBeNull();
      expect(screen.getByRole("button", { name: /Choose service/ }).getAttribute("aria-disabled")).toBe("true");
      expect(screen.getByText(/Key ending 9f3c, set 2 Nov 2026/)).toBeTruthy();
    });

    it("uses an aliased account model for its listed price and source badge", async () => {
      const aliased = { ...anthropicAccount, pricing_provider: "openai", pricing_models: { "model-a": "model-b" } };
      stubProviderApi({ accounts: [aliased], testResponse: { ok: true, models: [{ id: "model-a" }] } });
      renderAt("/settings/providers/p1/edit");
      expect(await screen.findByText(/Anthropic Credentials & Plan/)).toBeTruthy();
      await continueTo("Connection Verification");
      await passTest();
      await continueTo(/Models & Pricing for Anthropic/);

      const row = screen.getByRole("checkbox", { name: "Select model-a" }).closest("tr") as HTMLElement;
      expect(within(row).getByText("in $2.50 · out $10.00 / MTok")).toBeTruthy();
      expect(within(row).getByText("models.dev")).toBeTruthy();
    });

    it("tests the saved key by id, keeps the enabled models ticked and sends no api_key", async () => {
      const { body, requests } = await saveEdit();

      expect(Object.hasOwn(body, "api_key")).toBe(false);
      expect(body.name).toBe("Claude Team");
      expect(body.models).toEqual(["model-a"]);
      const probe = requests.find((request) => request.url === "/api/providers/test");
      expect(probe?.body?.id).toBe("p1");
    });

    it("does not send api_key when only whitespace was typed", async () => {
      const { body } = await saveEdit("   ");
      expect(Object.hasOwn(body, "api_key")).toBe(false);
    });

    it("sends a newly typed key to the test by value and with the save", async () => {
      const { body, requests } = await saveEdit("sk-ant-new-key-5678");

      expect(body.api_key).toBe("sk-ant-new-key-5678");
      const probe = requests.find((request) => request.url === "/api/providers/test");
      expect(probe?.body?.api_key).toBe("sk-ant-new-key-5678");
      expect(Object.hasOwn(probe?.body ?? {}, "id")).toBe(false);
    });
  });

  describe("custom models", () => {
    it("adds a model by id with the Add button or Enter, and ignores blanks and duplicates", async () => {
      stubProviderApi();
      await openModelsStep();
      const input = screen.getByLabelText(/Add Custom Model Identifier/) as HTMLInputElement;

      fireEvent.change(input, { target: { value: "  custom-1  " } });
      clickButton("Add");
      expect(within(selectedBadges()).getByText("custom-1")).toBeTruthy();
      expect(screen.getByText("Not listed by the provider")).toBeTruthy();
      expect(input.value).toBe("");

      fireEvent.change(input, { target: { value: "custom-2" } });
      fireEvent.keyDown(input, { key: "Enter" });
      fireEvent.change(input, { target: { value: "custom-2" } });
      clickButton("Add");
      fireEvent.change(input, { target: { value: "   " } });
      clickButton("Add");
      expect(screen.getByText("Selected (2):")).toBeTruthy();

      // Unticking a model the provider did not list removes its row.
      click(screen.getByRole("checkbox", { name: "Select custom-1" }));
      expect(screen.getByText("Selected (1):")).toBeTruthy();
      expect(screen.queryByRole("checkbox", { name: "Select custom-1" })).toBeNull();
    });
  });

  describe("on and off", () => {
    it("asks before turning an account off, lists what uses it, then sends enabled false", async () => {
      const requests = stubProviderApi({ uses: { routes: ["daily"], agents: ["Helper"], automations: [] } });
      renderAt("/settings/providers");

      click(await screen.findByRole("switch", { name: "Claude Team on" }));
      const dialog = await screen.findByRole("dialog");
      expect(await within(dialog).findByText("Route daily")).toBeTruthy();
      expect(within(dialog).getByText("Agent Helper")).toBeTruthy();
      click(within(dialog).getByRole("button", { name: "Turn off" }));

      expect(await sentBody(requests, "PUT", "/api/providers/p1")).toEqual({ enabled: false });
      expect(await screen.findByText("Turned off")).toBeTruthy();
    });

    it("keeps the account on when the person cancels", async () => {
      const requests = stubProviderApi();
      renderAt("/settings/providers");

      click(await screen.findByRole("switch", { name: "Claude Team on" }));
      const dialog = await screen.findByRole("dialog");
      expect(await within(dialog).findByText("Nothing uses it now.")).toBeTruthy();
      click(within(dialog).getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(requests.some((request) => request.method === "PUT")).toBe(false);
    });

    it("keeps Turn off disabled while what uses an account cannot be checked", async () => {
      const requests = stubProviderApi({ usesFail: ["p1"] });
      renderAt("/settings/providers");

      click(await screen.findByRole("switch", { name: "Claude Team on" }));
      let dialog = await screen.findByRole("dialog");
      expect(await within(dialog).findByText("Could not check what uses it")).toBeTruthy();
      const turnOff = within(dialog).getByRole("button", { name: "Turn off" });
      expect(isDisabled(turnOff)).toBe(true);
      click(turnOff);
      click(within(dialog).getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(requests.some((request) => request.method === "PUT" && request.body?.enabled === false)).toBe(false);

      // An account whose uses load can be turned off.
      click(screen.getByRole("switch", { name: "Office Ollama on" }));
      dialog = await screen.findByRole("dialog");
      expect(await within(dialog).findByText("Nothing uses it now.")).toBeTruthy();
      expect(isDisabled(within(dialog).getByRole("button", { name: "Turn off" }))).toBe(false);
    });

    it("turns an account on with no question", async () => {
      const requests = stubProviderApi({ accounts: [{ ...anthropicAccount, enabled: false, usable: false, usable_models: [] }] });
      renderAt("/settings/providers");

      click(await screen.findByRole("switch", { name: "Claude Team on" }));
      expect(await sentBody(requests, "PUT", "/api/providers/p1")).toEqual({ enabled: true });
    });
  });

  describe("prices", () => {
    it("shows each enabled model's price and source to a member, with no control to change it", async () => {
      stubProviderApi({ canShare: false });
      renderAt("/settings/providers/p1/prices");

      const row = await screen.findByTestId("price-row-model-a");
      expect(await within(row).findByText("$3.00")).toBeTruthy();
      expect(within(row).getByText("Provider")).toBeTruthy();
      expect(within(row).getByText("$15.00")).toBeTruthy();
      expect(within(row).getByText("models.dev")).toBeTruthy();
      expect(within(row).getByText("Unknown")).toBeTruthy();
      expect(within(row).getByText("Estimate")).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Set price for model-a" })).toBeNull();
      expect(screen.queryByRole("combobox", { name: "Pricing provider" })).toBeNull();
      expect(screen.queryByRole("textbox", { name: "Pricing model for model-a" })).toBeNull();
    });

    it("lets settings managers map the account vendor and model, sending the pricing map body", async () => {
      const requests = stubProviderApi({ canShare: true });
      renderAt("/settings/providers/p1/prices");

      const pricingProvider = await screen.findByRole("combobox", { name: "Pricing provider" });
      fireEvent.change(pricingProvider, { target: { value: "OpenAI" } });
      fireEvent.keyDown(pricingProvider, { key: "ArrowDown" });
      fireEvent.keyDown(pricingProvider, { key: "Enter" });
      click(screen.getByRole("button", { name: "Save pricing provider" }));
      expect(await sentBody(requests, "PUT", "/api/providers/p1/pricing-map")).toEqual({ pricing_provider: "openai", models: {} });

      click(screen.getByRole("button", { name: "Clear pricing provider" }));
      click(screen.getByRole("button", { name: "Save pricing provider" }));
      await waitFor(() => expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map" && request.method === "PUT")).toHaveLength(2));
      expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map").at(-1)?.body).toEqual({ pricing_provider: "", models: {} });

      const choosePricingProvider = screen.getByRole("combobox", { name: "Pricing provider" });
      fireEvent.change(choosePricingProvider, { target: { value: "OpenAI" } });
      fireEvent.keyDown(choosePricingProvider, { key: "ArrowDown" });
      fireEvent.keyDown(choosePricingProvider, { key: "Enter" });
      fireEvent.keyDown(choosePricingProvider, { key: "Escape" });
      fireEvent.click(choosePricingProvider);
      click(document.querySelector('[data-combobox-option][value="openai"]') as HTMLElement);
      click(screen.getByRole("button", { name: "Save pricing provider" }));
      await waitFor(() => expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map" && request.method === "PUT")).toHaveLength(3));
      expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map").at(-1)?.body).toEqual({ pricing_provider: "", models: {} });

      fireEvent.change(await screen.findByLabelText("Pricing model for model-a"), { target: { value: "upstream/model-a" } });
      click(screen.getByRole("button", { name: "Save mapping for model-a" }));
      await waitFor(() => expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map" && request.method === "PUT")).toHaveLength(4));
      expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map").at(-1)?.body).toEqual({
        models: { "model-a": "upstream/model-a" },
      });
      await waitFor(() => expect((screen.getByLabelText("Pricing model for model-a") as HTMLInputElement).value).toBe("upstream/model-a"));

      fireEvent.change(await screen.findByLabelText("Pricing model for model-a"), { target: { value: "   " } });
      click(screen.getByRole("button", { name: "Save mapping for model-a" }));
      await waitFor(() => expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map" && request.method === "PUT")).toHaveLength(5));
      expect(requests.filter((request) => request.url === "/api/providers/p1/pricing-map").at(-1)?.body).toEqual({ models: { "model-a": null } });
      await waitFor(() => expect((screen.getByLabelText("Pricing model for model-a") as HTMLInputElement).value).toBe(""));
    });

    it("lets a person with settings:manage set a price in micro-USD, then shows Your price, who set it and the warning", async () => {
      const warning = {
        field: "input_micro_usd_per_mtok",
        override_micro_usd_per_mtok: 2_900_000,
        base_micro_usd_per_mtok: 3_000_000,
        base_source: "provider",
      };
      const requests = stubProviderApi({
        canShare: true,
        pricing: (_url, method, body) =>
          method === "PUT"
            ? pricingView(String(body?.model), { input: Number(body?.input_micro_usd_per_mtok) }, [
                { ...warning, override_micro_usd_per_mtok: Number(body?.input_micro_usd_per_mtok) },
              ])
            : pricingView("model-a"),
      });
      renderAt("/settings/providers/p1/prices");

      expect(await within(await screen.findByTestId("price-row-model-a")).findByText("$3.00")).toBeTruthy();
      click(screen.getByRole("button", { name: "Set price for model-a" }));
      const dialog = await screen.findByRole("dialog");
      fireEvent.change(within(dialog).getByLabelText("Input (USD per 1M tokens)"), { target: { value: "0.29" } });
      click(within(dialog).getByRole("button", { name: "Save price" }));

      expect(await sentBody(requests, "PUT", "/api/providers/p1/overrides")).toEqual({
        model: "model-a",
        input_micro_usd_per_mtok: 290_000,
        output_micro_usd_per_mtok: null,
        cache_read_micro_usd_per_mtok: null,
        cache_write_micro_usd_per_mtok: null,
      });
      const row = await screen.findByTestId("price-row-model-a");
      expect(await within(row).findAllByText("Your price")).toHaveLength(2);
      expect(within(row).getByText("$0.29")).toBeTruthy();
      expect(within(row).getByText("Set by Ada on 10 Nov 2026")).toBeTruthy();
      expect(within(row).getByText("Input $0.29 is more than 20% away from the provider's price of $3.00.")).toBeTruthy();
    });

    it("refuses a price that is not one, and removes a price that was set", async () => {
      const requests = stubProviderApi({
        canShare: true,
        pricing: (_url, method) => (method === "DELETE" ? pricingView("model-a") : pricingView("model-a", { input: 1_000_000 })),
      });
      renderAt("/settings/providers/p1/prices");

      expect(await within(await screen.findByTestId("price-row-model-a")).findByText("$1.00")).toBeTruthy();
      click(screen.getByRole("button", { name: "Set price for model-a" }));
      const dialog = await screen.findByRole("dialog");
      const input = within(dialog).getByLabelText("Input (USD per 1M tokens)") as HTMLInputElement;
      expect(input.value).toBe("1");
      fireEvent.change(input, { target: { value: "1,50" } });
      click(within(dialog).getByRole("button", { name: "Save price" }));
      expect(await within(dialog).findByText("Input: enter a price in USD, like 2.50, with at most six decimals.")).toBeTruthy();
      expect(requests.some((request) => request.method === "PUT")).toBe(false);

      click(within(dialog).getByRole("button", { name: "Remove your price" }));
      expect(await sentBody(requests, "DELETE", "/api/providers/p1/overrides")).toEqual({ model: "model-a" });
      const row = screen.getByTestId("price-row-model-a");
      await waitFor(() => expect(within(row).queryByText("Your price")).toBeNull());
    });
  });

  it("can disconnect a provider", async () => {
    stubProviderApi();
    renderAt("/settings/providers");

    const card = await screen.findByTestId("provider-card-p2");
    click(within(card).getByRole("button", { name: "Disconnect" }));
    const confirm = await screen.findByRole("dialog");
    expect(within(confirm).getByText(/Agents and tasks using models from Office Ollama will pause/)).toBeTruthy();
    click(within(confirm).getByRole("button", { name: "Disconnect provider" }));

    await waitFor(() => {
      expect(screen.queryByTestId("provider-card-p2")).toBeNull();
    });
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
    renderAt("/settings/providers");

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
    for (const mark of [anthropic, openai, gemini]) {
      expect(mark.classList.contains("tabler-icon")).toBe(false);
    }
    // A provider without a mark gets the generic server glyph.
    expect(unknown.classList.contains("tabler-icon-server")).toBe(true);
  });
});
