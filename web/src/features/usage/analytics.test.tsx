import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient, type Usage, type UsageBucket, type UsageRange } from "../../app/api";
import { AppProviders } from "../../theme";
import { formatBucket, formatLatency, formatRatePrecise, formatTokens, formatUsd } from "./format";

const panelTitles = [
  "Spend by provider",
  "Spend by model",
  "Tokens per model",
  "Token consumption",
  "Latency per provider",
  "Spend share by provider",
  "Requests and error rate",
];

const lineCharts = [
  "Spend by provider",
  "Spend by model",
  "Tokens per model",
  "Token consumption",
  "Requests and error rate",
];

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
}

function withUsage(change: (usage: Usage) => Usage): ApiClient {
  const stub = createStubApi();
  return { ...stub, usage: async (range) => change(await stub.usage(range)) };
}

/** The visually hidden data table of a chart: its header cells and one entry per row. */
function chartTable(name: string): { header: string[]; rows: { label: string; cells: string[] }[] } {
  const table = screen.getByRole("table", { name });
  return {
    header: within(table)
      .getAllByRole("columnheader")
      .map((cell) => cell.textContent ?? ""),
    rows: within(table)
      .getAllByRole("row")
      .slice(1)
      .map((row) => ({
        label: within(row).getByRole("rowheader").textContent ?? "",
        cells: within(row)
          .getAllByRole("cell")
          .map((cell) => cell.textContent ?? ""),
      })),
  };
}

function cellOf(name: string, label: string, column: string): string {
  const { header, rows } = chartTable(name);
  const index = header.indexOf(column) - 1;
  const row = rows.find((item) => item.label === label);
  if (index < 0 || !row) {
    throw new Error(`no ${column} cell for ${label} in ${name}`);
  }
  return row.cells[index] ?? "";
}

/** The panel (card) that holds the heading of the given title. */
function panelOf(title: string): HTMLElement {
  const panel = screen.getByRole("heading", { level: 2, name: title }).parentElement?.parentElement;
  if (!panel) {
    throw new Error(`no panel for ${title}`);
  }
  return panel;
}

/** The three rendered layers of "Token consumption" for one bucket label, as numbers. */
function tokenLayers(label: string): number[] {
  return ["Input excluding tool definitions", "Output", "Tool definitions (est.)"].map((column) =>
    Number(cellOf("Token consumption", label, column).replaceAll(",", "")),
  );
}

function bucket(extra: Partial<UsageBucket>): UsageBucket {
  return {
    at: "2026-11-01",
    requests: 0,
    errors: 0,
    input: 0,
    output: 0,
    cache_read: 0,
    cache_write: 0,
    tool_defs_est: 0,
    spend_usd: 0,
    by_provider: {},
    by_model: {},
    ...extra,
  };
}

describe("Usage analytics panels", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it.each(["today", "7", "30", "90"])(
    "usage analytics: every panel renders from the stub on range %s",
    async (range) => {
      const usage = await createStubApi().usage(range === "today" ? "today" : (Number(range) as UsageRange));
      renderAt(`/usage?range=${range}`);
      for (const title of panelTitles) {
        expect(await screen.findByRole("heading", { level: 2, name: title })).toBeTruthy();
        expect(screen.getByRole("group", { name: title })).toBeTruthy();
      }
      for (const title of lineCharts) {
        expect(chartTable(title).rows).toHaveLength(usage.buckets.length);
      }
      expect(chartTable("Latency per provider").rows.length).toBeGreaterThan(0);
    },
  );

  it("usage analytics: the new panels sit after Cache hit rate and before Tasks, in order", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Requests and error rate" });
    expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual([
      ...panelTitles,
      "Tasks",
    ]);
    const cache = screen.getByRole("group", { name: "Cache hit rate" });
    const first = screen.getByRole("heading", { level: 2, name: "Spend by provider" });
    expect(cache.compareDocumentPosition(first) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("usage analytics: latency and spend share sit in one row, apart from the other panels", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Latency per provider" });
    const row = screen.getByRole("heading", { name: "Latency per provider" }).closest(".usage-pair");
    expect(row).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Spend share by provider" }).closest(".usage-pair")).toBe(row);
    expect(screen.getByRole("heading", { name: "Spend by provider" }).closest(".usage-pair")).toBeNull();
    expect(screen.getByRole("heading", { name: "Requests and error rate" }).closest(".usage-pair")).toBeNull();
  });

  it("usage analytics: spend by provider has a line per priced provider, global ones marked, with the API's figures", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Spend by provider" });
    const { header, rows } = chartTable("Spend by provider");
    expect(header).toEqual(["Label", "Anthropic (global)", "OpenAI (global)", "Mistral"]);
    const known = week.buckets.find((item) => item.spend_usd !== null)!;
    expect(cellOf("Spend by provider", formatBucket(known.at, "day"), "Anthropic (global)")).toBe(
      formatUsd(known.by_provider.anthropic?.spend_usd),
    );
    expect(cellOf("Spend by provider", formatBucket(known.at, "day"), "Mistral")).toBe(
      formatUsd(known.by_provider.mistral?.spend_usd),
    );
    expect(rows).toHaveLength(week.buckets.length);
    expect(within(screen.getByRole("group", { name: "Spend by provider" })).queryByText(/Ollama/)).toBeNull();
  });

  it("usage analytics: spend by model has a line per priced model and the API's figures", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Spend by model" });
    const { header } = chartTable("Spend by model");
    expect(header).toEqual([
      "Label",
      "Claude 3.7 Sonnet",
      "Claude 3.5 Haiku",
      "GPT-4.1",
      "GPT-4.1 mini",
      "Mistral Large",
    ]);
    const known = week.buckets.find((item) => item.spend_usd !== null)!;
    expect(cellOf("Spend by model", formatBucket(known.at, "day"), "GPT-4.1 mini")).toBe(
      formatUsd(known.by_model["gpt-4.1-mini"]?.spend_usd),
    );
  });

  it("usage analytics: tokens per model are token counts of every model that took calls, unpriced ones included", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Tokens per model" });
    expect(chartTable("Tokens per model").header).toContain("Llama 3.3 70B");
    const known = week.buckets.find((item) => item.by_model["claude-3-7-sonnet"] !== undefined)!;
    expect(cellOf("Tokens per model", formatBucket(known.at, "day"), "Claude 3.7 Sonnet")).toBe(
      formatTokens(known.by_model["claude-3-7-sonnet"]!.tokens),
    );
    const day = week.buckets.find((item) => item.by_model["llama3.3:70b"] === undefined)!;
    expect(cellOf("Tokens per model", formatBucket(day.at, "day"), "Llama 3.3 70B")).toBe("0");
  });

  it("usage analytics: token consumption stacks input without tool definitions, output and tool definitions to input plus output", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Token consumption" });
    expect(chartTable("Token consumption").header).toEqual([
      "Label",
      "Input excluding tool definitions",
      "Output",
      "Tool definitions (est.)",
    ]);
    for (const item of week.buckets) {
      const label = formatBucket(item.at, "day");
      expect(tokenLayers(label)).toEqual([
        item.input - item.tool_defs_est,
        item.output,
        item.tool_defs_est,
      ]);
      expect(tokenLayers(label).reduce((sum, value) => sum + value, 0)).toBe(item.input + item.output);
    }
    const chart = screen.getByRole("group", { name: "Token consumption" });
    expect(chart.querySelectorAll(".upmo-linechart__area").length).toBeGreaterThan(0);
    expect(screen.getByText("Tool definitions are an estimate and are counted out of input.")).toBeTruthy();
    expect(screen.queryByText(/already part of input/)).toBeNull();
    expect(screen.queryByRole("radiogroup", { name: "Token consumption view" })).toBeNull();
  });

  it("usage analytics: the token consumption stack still adds up to input plus output when tool definitions exceed input", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      buckets: [
        bucket({ at: "2026-11-01", requests: 1, input: 100, output: 40, tool_defs_est: 150 }),
        bucket({ at: "2026-11-02", requests: 1, input: 100, output: 40, tool_defs_est: 100 }),
        bucket({ at: "2026-11-03", requests: 1, input: 100, output: 40, tool_defs_est: 30 }),
      ],
    }));
    renderAt("/usage", client);
    await screen.findByRole("group", { name: "Token consumption" });
    expect(tokenLayers("1 Nov")).toEqual([0, 40, 100]);
    expect(tokenLayers("2 Nov")).toEqual([0, 40, 100]);
    expect(tokenLayers("3 Nov")).toEqual([70, 40, 30]);
    for (const label of ["1 Nov", "2 Nov", "3 Nov"]) {
      expect(tokenLayers(label).reduce((sum, value) => sum + value, 0)).toBe(140);
    }
  });

  it("usage analytics: the Lines and Stacked toggle starts on Lines and changes only its own chart", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Spend by provider" });
    const areas = (name: string) =>
      screen.getByRole("group", { name }).querySelectorAll(".upmo-linechart__area").length;
    const providerView = screen.getByRole("radiogroup", { name: "Spend by provider view" });
    const modelView = screen.getByRole("radiogroup", { name: "Spend by model view" });
    expect(within(providerView).getAllByRole("radio").map((radio) => radio.getAttribute("value"))).toEqual([
      "lines",
      "stacked",
    ]);
    expect(within(providerView).getByRole("radio", { name: "Lines" })).toHaveProperty("checked", true);
    expect(areas("Spend by provider")).toBe(0);

    fireEvent.click(within(providerView).getByRole("radio", { name: "Stacked" }));
    expect(within(providerView).getByRole("radio", { name: "Stacked" })).toHaveProperty("checked", true);
    expect(areas("Spend by provider")).toBeGreaterThan(0);
    expect(areas("Spend by model")).toBe(0);
    expect(within(modelView).getByRole("radio", { name: "Lines" })).toHaveProperty("checked", true);

    fireEvent.click(within(modelView).getByRole("radio", { name: "Stacked" }));
    expect(areas("Spend by model")).toBeGreaterThan(0);
    fireEvent.click(within(providerView).getByRole("radio", { name: "Lines" }));
    expect(areas("Spend by provider")).toBe(0);
    expect(areas("Spend by model")).toBeGreaterThan(0);
  });

  it("usage analytics: a priced row that made no calls in a bucket with unknown spend is zero there, not a gap", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Spend by provider" });
    const unknown = week.buckets.find((item) => item.spend_usd === null)!;
    const label = formatBucket(unknown.at, "day");
    const check = () => {
      for (const name of ["Spend by provider", "Spend by model"]) {
        const row = chartTable(name).rows.find((item) => item.label === label)!;
        expect(row.cells.every((cell) => cell === formatUsd(0))).toBe(true);
      }
    };
    check();
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Spend by provider view" })).getByRole("radio", { name: "Stacked" }));
    check();
  });

  it("usage analytics: unknown spend of a row that took calls is a gap in the spend lines, stacked or not", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      buckets: [
        bucket({
          at: "2026-11-01",
          requests: 3,
          spend_usd: 1,
          by_provider: {
            anthropic: { spend_usd: null, requests: 1, errors: 0, tokens: 10 },
            mistral: { spend_usd: 1, requests: 2, errors: 0, tokens: 20 },
          },
          by_model: {
            "claude-3-7-sonnet": { spend_usd: null, tokens: 10 },
            "mistral-large": { spend_usd: 1, tokens: 20 },
          },
        }),
      ],
    }));
    renderAt("/usage", client);
    await screen.findByRole("group", { name: "Spend by provider" });
    const check = () => {
      expect(cellOf("Spend by provider", "1 Nov", "Anthropic (global)")).toBe("No data");
      expect(cellOf("Spend by provider", "1 Nov", "Mistral")).toBe(formatUsd(1));
      expect(cellOf("Spend by model", "1 Nov", "Claude 3.7 Sonnet")).toBe("No data");
      expect(cellOf("Spend by model", "1 Nov", "Mistral Large")).toBe(formatUsd(1));
    };
    check();
    fireEvent.click(within(screen.getByRole("radiogroup", { name: "Spend by provider view" })).getByRole("radio", { name: "Stacked" }));
    check();
  });

  it("usage analytics: a provider with unknown spend in a known bucket is a gap, one with no calls there is zero", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      buckets: [
        bucket({
          at: "2026-11-01",
          requests: 3,
          spend_usd: 1,
          by_provider: {
            anthropic: { spend_usd: null, requests: 1, errors: 0, tokens: 10 },
            mistral: { spend_usd: 1, requests: 2, errors: 0, tokens: 20 },
          },
          by_model: { "mistral-large": { spend_usd: 1, tokens: 20 } },
        }),
        bucket({ at: "2026-11-02", requests: 0, spend_usd: null }),
      ],
    }));
    renderAt("/usage", client);
    await screen.findByRole("group", { name: "Spend by provider" });
    expect(cellOf("Spend by provider", "1 Nov", "Anthropic (global)")).toBe("No data");
    expect(cellOf("Spend by provider", "1 Nov", "OpenAI (global)")).toBe(formatUsd(0));
    expect(cellOf("Spend by provider", "1 Nov", "Mistral")).toBe(formatUsd(1));
    expect(cellOf("Spend by provider", "2 Nov", "Mistral")).toBe(formatUsd(0));
  });

  it("usage analytics: latency per provider shows p50, p95 and p99 as readable durations for providers with calls", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Latency per provider" });
    const { header, rows } = chartTable("Latency per provider");
    expect(header).toEqual(["Label", "p50", "p95", "p99"]);
    expect(rows.map((row) => row.label)).toEqual(["Anthropic (global)", "OpenAI (global)", "Mistral", "Ollama"]);
    const anthropic = week.providers.find((provider) => provider.id === "anthropic")!;
    expect(rows[0]!.cells).toEqual([
      formatLatency(anthropic.latency_ms.p50),
      formatLatency(anthropic.latency_ms.p95),
      formatLatency(anthropic.latency_ms.p99),
    ]);
    expect(rows[0]!.cells[0]).toMatch(/^\d(\.\d)? s$/);
  });

  it("usage analytics: latency leaves out a provider that took no calls", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      providers: usage.providers.map((provider) =>
        provider.id === "ollama" ? { ...provider, requests: 0, errors: 0 } : provider,
      ),
    }));
    renderAt("/usage", client);
    await screen.findByRole("group", { name: "Latency per provider" });
    expect(chartTable("Latency per provider").rows.map((row) => row.label)).not.toContain("Ollama");
  });

  it("usage analytics: the spend share donut leaves out unpriced providers and names them", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Spend share by provider" });
    const donut = screen.getByRole("group", { name: "Spend share by provider" });
    const rows = within(screen.getByRole("table", { name: "Spend share by provider" })).getAllByRole("row").slice(1);
    expect(rows.map((row) => within(row).getByRole("rowheader").textContent)).toEqual([
      "Anthropic (global)",
      "OpenAI (global)",
      "Mistral",
    ]);
    expect(within(donut).queryByText(/Ollama/)).toBeNull();
    expect(within(panelOf("Spend share by provider")).getByText("No price for: Ollama")).toBeTruthy();
    const total = week.providers.reduce((sum, provider) => sum + (provider.spend_usd ?? 0), 0);
    expect(within(donut).getAllByText(formatUsd(total)).length).toBeGreaterThan(0);
  });

  it("usage analytics: the spend share lists every unpriced provider, and says so when none has a price", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      providers: usage.providers.map((provider) =>
        provider.id === "mistral" || provider.id === "ollama" ? { ...provider, spend_usd: null } : provider,
      ),
    }));
    renderAt("/usage", client);
    await screen.findByRole("group", { name: "Spend share by provider" });
    expect(within(panelOf("Spend share by provider")).getByText("No price for: Mistral, Ollama")).toBeTruthy();
    cleanup();

    const none = withUsage((usage) => ({
      ...usage,
      providers: usage.providers.map((provider) => ({ ...provider, spend_usd: null })),
    }));
    renderAt("/usage", none);
    await screen.findByRole("heading", { name: "Spend share by provider" });
    await waitFor(() => expect(screen.getByText("No priced spend in this range.")).toBeTruthy());
    expect(screen.queryByRole("group", { name: "Spend share by provider" })).toBeNull();
    expect(
      within(panelOf("Spend share by provider")).getByText(
        "No price for: Anthropic (global), OpenAI (global), Mistral, Ollama",
      ),
    ).toBeTruthy();
  });

  it("usage analytics: spend by provider names the providers that took calls but have no price, under the chart", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Spend by provider" });
    expect(within(panelOf("Spend by provider")).getByText("No price for: Ollama")).toBeTruthy();
    expect(within(panelOf("Spend by model")).queryByText(/Ollama/)).toBeNull();
    cleanup();

    const several = withUsage((usage) => ({
      ...usage,
      providers: usage.providers.map((provider) =>
        provider.id === "mistral" || provider.id === "ollama" ? { ...provider, spend_usd: null } : provider,
      ),
    }));
    renderAt("/usage", several);
    await screen.findByRole("group", { name: "Spend by provider" });
    expect(within(panelOf("Spend by provider")).getByText("No price for: Mistral, Ollama")).toBeTruthy();
    expect(chartTable("Spend by provider").header).toEqual(["Label", "Anthropic (global)", "OpenAI (global)"]);
  });

  it("usage analytics: spend by provider with no priced provider says there is no spend to draw and still names them", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      providers: usage.providers.map((provider) => ({ ...provider, spend_usd: null })),
    }));
    renderAt("/usage", client);
    await screen.findByRole("heading", { level: 2, name: "Spend by provider" });
    const panel = panelOf("Spend by provider");
    await waitFor(() => expect(within(panel).getByText("No spend by provider in this range.")).toBeTruthy());
    expect(screen.queryByRole("group", { name: "Spend by provider" })).toBeNull();
    expect(
      within(panel).getByText("No price for: Anthropic (global), OpenAI (global), Mistral, Ollama"),
    ).toBeTruthy();
  });

  it("usage analytics: spend by model names the models that took tokens but have no price, under the chart", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Spend by model" });
    expect(within(panelOf("Spend by model")).getByText("No price for: Llama 3.3 70B")).toBeTruthy();
    cleanup();

    const several = withUsage((usage) => ({
      ...usage,
      models: usage.models.map((model) =>
        model.id === "gpt-4.1-mini" || model.id === "llama3.3:70b" ? { ...model, spend_usd: null } : model,
      ),
    }));
    renderAt("/usage", several);
    await screen.findByRole("group", { name: "Spend by model" });
    expect(within(panelOf("Spend by model")).getByText("No price for: GPT-4.1 mini, Llama 3.3 70B")).toBeTruthy();
    expect(chartTable("Spend by model").header).not.toContain("GPT-4.1 mini");
  });

  it("usage analytics: spend by model with no priced model says there is no spend to draw and still names them", async () => {
    const week = await createStubApi().usage(7);
    const client = withUsage((usage) => ({
      ...usage,
      models: usage.models.map((model) => ({ ...model, spend_usd: null })),
    }));
    renderAt("/usage", client);
    await screen.findByRole("heading", { level: 2, name: "Spend by model" });
    const panel = panelOf("Spend by model");
    await waitFor(() => expect(within(panel).getByText("No spend by model in this range.")).toBeTruthy());
    expect(screen.queryByRole("group", { name: "Spend by model" })).toBeNull();
    const names = week.models.filter((model) => model.tokens > 0).map((model) => model.name);
    expect(within(panel).getByText(`No price for: ${names.join(", ")}`)).toBeTruthy();
  });

  it("usage analytics: requests and error rate plots the error rate as a percent of requests", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      buckets: [
        bucket({ at: "2026-11-01", requests: 10, errors: 1 }),
        bucket({ at: "2026-11-02", requests: 8, errors: 1 }),
        bucket({ at: "2026-11-03", requests: 0, errors: 0 }),
      ],
    }));
    renderAt("/usage", client);
    await screen.findByRole("group", { name: "Requests and error rate" });
    expect(chartTable("Requests and error rate").header).toEqual(["Label", "Requests", "Error rate"]);
    expect(cellOf("Requests and error rate", "1 Nov", "Error rate")).toBe("10%");
    expect(cellOf("Requests and error rate", "2 Nov", "Error rate")).toBe("12.5%");
    expect(cellOf("Requests and error rate", "3 Nov", "Error rate")).toBe("0%");
    expect(cellOf("Requests and error rate", "1 Nov", "Requests")).toBe("10");
  });

  it("usage analytics: the stub's error rate is a percent in every bucket", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("group", { name: "Requests and error rate" });
    for (const item of week.buckets) {
      const label = formatBucket(item.at, "day");
      expect(cellOf("Requests and error rate", label, "Error rate")).toBe(
        formatRatePrecise(item.requests === 0 ? 0 : item.errors / item.requests),
      );
      expect(cellOf("Requests and error rate", label, "Requests")).toBe(formatTokens(item.requests));
    }
  });

  it("usage analytics: Today labels every analytics chart by the hour", async () => {
    const today = await createStubApi().usage("today");
    renderAt("/usage?range=today");
    await screen.findByRole("group", { name: "Requests and error rate" });
    for (const title of lineCharts) {
      const labels = chartTable(title).rows.map((row) => row.label);
      expect(labels).toHaveLength(today.buckets.length);
      expect(labels.every((label) => /^\d\d:00$/.test(label))).toBe(true);
    }
  });

  it("usage analytics: a range with no usage shows only the page empty state and none of the panels", async () => {
    renderAt("/usage", createStubApi({ emptyUsage: true }));
    expect(await screen.findByRole("heading", { level: 2, name: "No usage in this range" })).toBeTruthy();
    for (const title of panelTitles) {
      expect(screen.queryByRole("heading", { level: 2, name: title })).toBeNull();
      expect(screen.queryByRole("group", { name: title })).toBeNull();
    }
    expect(screen.getByText("No task usage in this range.")).toBeTruthy();
    expect(screen.queryByText("No spend by provider in this range.")).toBeNull();
    expect(screen.queryByText("No tokens in this range.")).toBeNull();
    expect(screen.queryByText("No latency to show in this range.")).toBeNull();
    expect(screen.queryByRole("radiogroup", { name: "Spend by provider view" })).toBeNull();
    expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual([
      "No usage in this range",
      "Tasks",
    ]);
  });

  it("usage analytics: a range with tasks but no calls shows the empty states in the panels", async () => {
    const client = withUsage((usage) => ({ ...usage, buckets: [], providers: [], models: [] }));
    renderAt("/usage", client);
    await screen.findByRole("table", { name: "Task breakdown" });
    for (const title of panelTitles) {
      expect(screen.queryByRole("group", { name: title })).toBeNull();
    }
    expect(screen.getByText("No latency to show in this range.")).toBeTruthy();
  });
});
