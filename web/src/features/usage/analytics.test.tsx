import { readFileSync } from "node:fs";
import { join } from "node:path";

import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type {
  ComboChartProps,
  DonutChartProps,
  GroupedBarChartProps,
  LineChartProps,
} from "@upmo-ai/components";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient, type Usage, type UsageBucket, type UsageRange } from "../../app/api";
import { AppProviders } from "../../theme";
import { chartProps, resetChartProps } from "./chart-spy";
import {
  formatBucket,
  formatLatency,
  formatPercentPoints,
  formatRatePrecise,
  formatTokensCompact,
  formatUsd,
} from "./format";

vi.mock("@upmo-ai/components", async (importOriginal) => {
  const { withChartSpy } = await import("./chart-spy");
  return withChartSpy(await importOriginal<typeof import("@upmo-ai/components")>());
});

/** Every chart panel in reading order: two to a row on a wide page, the last two across the whole row. */
const panelTitles = [
  "Spend",
  "Cache hit rate",
  "Spend by provider",
  "Spend by model",
  "Tokens per model",
  "Token consumption",
  "Latency per provider",
  "Spend share by provider",
  "Requests and error rate",
  "Cost per task",
];

const lineCharts = [
  "Spend",
  "Cache hit rate",
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

/** The three layers "Token consumption" was given for one bucket label, as numbers. */
function tokenLayers(label: string): number[] {
  const { series } = chartProps<LineChartProps>("Token consumption");
  return (series ?? []).map((layer) => {
    const point = layer.points.find((item) => item.label === label);
    if (!point || point.value === null) {
      throw new Error(`no ${layer.name} value for ${label}`);
    }
    return point.value;
  });
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
    resetChartProps();
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

  it("usage polish 4113376c: the panels come in the new order, then Tasks", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Cost per task" });
    expect(screen.getAllByRole("heading", { level: 2 }).map((heading) => heading.textContent)).toEqual([
      ...panelTitles,
      "Tasks",
    ]);
    const tasks = screen.getByRole("heading", { level: 2, name: "Tasks" });
    const last = screen.getByRole("group", { name: "Cost per task" });
    expect(last.compareDocumentPosition(tasks) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("usage polish 4113376c: every chart panel is a child of one grid, and only the last two span the row", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Cost per task" });
    const grid = panelOf("Spend").parentElement!;
    expect(grid.classList.contains("usage-panels")).toBe(true);
    expect(Array.from(grid.children)).toEqual(panelTitles.map((title) => panelOf(title)));
    const wide = Array.from(grid.children)
      .filter((panel) => panel.classList.contains("usage-wide"))
      .map((panel) => panel.querySelector("h2")?.textContent);
    expect(wide).toEqual(["Requests and error rate", "Cost per task"]);
    // The tiles stay outside the panel grid, in a row of their own.
    expect(grid.contains(document.querySelector(".usage-figures"))).toBe(false);
  });

  it("usage polish 4113376c: the layout reads the content width, six tiles and two panels across from 928px, three tiles from 640px", () => {
    const css = readFileSync(join("src", "features", "usage", "usage.css"), "utf8");
    expect(css).toMatch(/\.usage-page\s*\{[^}]*container:\s*usage\s*\/\s*inline-size/);
    const blocks = css.split("@container usage ").slice(1);
    const from = (min: string) => blocks.find((block) => block.startsWith(`(min-width: ${min})`)) ?? "";
    // 58rem and 40rem are 928px and 640px. A 1280px screen leaves about 990px of content beside the sidebar.
    expect(from("58rem")).toMatch(/\.usage-figures\s*\{[^}]*repeat\(6,/);
    expect(from("58rem")).toMatch(/\.usage-panels\s*\{[^}]*repeat\(2,/);
    expect(from("58rem")).toMatch(/\.usage-wide\s*\{[^}]*grid-column:\s*1\s*\/\s*-1/);
    expect(from("40rem")).toMatch(/\.usage-figures\s*\{[^}]*repeat\(3,/);
    // Below 640px: two tiles across and one panel column.
    const base = css.slice(0, css.indexOf("@container"));
    expect(base).toMatch(/\.usage-figures\s*\{[^}]*repeat\(2,/);
    expect(base).toMatch(/\.usage-panels\s*\{[^}]*grid-template-columns:\s*minmax\(0,\s*1fr\)\s*;/);
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
      formatTokensCompact(known.by_model["claude-3-7-sonnet"]!.tokens),
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
    expect(chartProps<LineChartProps>("Token consumption").stacked).toBe(true);
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
    const stacked = (name: string) => chartProps<LineChartProps>(name).stacked ?? false;
    const providerView = screen.getByRole("radiogroup", { name: "Spend by provider view" });
    const modelView = screen.getByRole("radiogroup", { name: "Spend by model view" });
    expect(within(providerView).getAllByRole("radio").map((radio) => radio.getAttribute("value"))).toEqual([
      "lines",
      "stacked",
    ]);
    expect(within(providerView).getByRole("radio", { name: "Lines" })).toHaveProperty("checked", true);
    expect(stacked("Spend by provider")).toBe(false);

    fireEvent.click(within(providerView).getByRole("radio", { name: "Stacked" }));
    expect(within(providerView).getByRole("radio", { name: "Stacked" })).toHaveProperty("checked", true);
    expect(stacked("Spend by provider")).toBe(true);
    expect(stacked("Spend by model")).toBe(false);
    expect(within(modelView).getByRole("radio", { name: "Lines" })).toHaveProperty("checked", true);

    fireEvent.click(within(modelView).getByRole("radio", { name: "Stacked" }));
    expect(stacked("Spend by model")).toBe(true);
    fireEvent.click(within(providerView).getByRole("radio", { name: "Lines" }));
    expect(stacked("Spend by provider")).toBe(false);
    expect(stacked("Spend by model")).toBe(true);
    // The token stack is stacked by design and has no toggle.
    expect(stacked("Token consumption")).toBe(true);
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
      expect(cellOf("Requests and error rate", label, "Requests")).toBe(formatTokensCompact(item.requests));
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

  it("usage polish 4113376c: with calls but no tasks the cost chart says so and the rest still draw", async () => {
    renderAt("/usage", withUsage((usage) => ({ ...usage, tasks: [] })));
    await screen.findByRole("group", { name: "Requests and error rate" });
    expect(screen.getByText("No task costs in this range.")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "Cost per task" })).toBeNull();
    expect(screen.getByRole("heading", { level: 2, name: "Cost per task" })).toBeTruthy();
  });

  it("usage analytics: a range with tasks but no calls shows the empty states in the panels", async () => {
    const client = withUsage((usage) => ({ ...usage, buckets: [], providers: [], models: [] }));
    renderAt("/usage", client);
    await screen.findByRole("table", { name: "Task breakdown" });
    // The cost chart is about tasks, which this range has; every other chart is about calls, which it has not.
    for (const title of panelTitles.filter((name) => name !== "Cost per task")) {
      expect(screen.queryByRole("group", { name: title })).toBeNull();
    }
    expect(screen.getByRole("group", { name: "Cost per task" })).toBeTruthy();
    expect(screen.getByText("No latency to show in this range.")).toBeTruthy();
    expect(screen.getByText("No spend to chart in this range.")).toBeTruthy();
    expect(screen.getByText("No cache reads in this range.")).toBeTruthy();
    expect(screen.queryByText("No task costs in this range.")).toBeNull();
  });

  it("usage polish 4113376c: every chart gets a formatter that keeps numbers short", async () => {
    renderAt("/usage");
    await screen.findByRole("group", { name: "Cost per task" });
    const tokens = [
      chartProps<LineChartProps>("Tokens per model").formatValue,
      chartProps<LineChartProps>("Token consumption").formatValue,
      chartProps<ComboChartProps>("Requests and error rate").formatBar,
    ];
    for (const format of tokens) {
      expect(format?.(536_440.143)).toBe("536k");
      expect(format?.(2_330_160)).toBe("2.3M");
      expect(format?.(1_200_000_000)).toBe("1.2B");
    }
    const dollars = [
      chartProps<GroupedBarChartProps>("Spend").formatValue,
      chartProps<GroupedBarChartProps>("Cost per task").formatValue,
      chartProps<LineChartProps>("Spend by provider").formatValue,
      chartProps<LineChartProps>("Spend by model").formatValue,
      chartProps<DonutChartProps>("Spend share by provider").formatValue,
    ];
    for (const format of dollars) {
      expect(format?.(1.1666)).toBe("US$1.17");
    }
    expect(chartProps<GroupedBarChartProps>("Latency per provider").formatValue?.(850)).toBe(formatLatency(850));
    expect(chartProps<LineChartProps>("Cache hit rate").formatValue?.(66.04)).toBe("66%");
    expect(chartProps<LineChartProps>("Cache hit rate").formatValue?.(12.5)).toBe(formatPercentPoints(12.5));
    expect(chartProps<ComboChartProps>("Requests and error rate").formatLine?.(3.64)).toBe("3.6%");
  });

  it("usage polish 4113376c: the charts keep the DS smooth curve and the hidden tables show no long decimals", async () => {
    renderAt("/usage?range=30");
    await screen.findByRole("group", { name: "Cost per task" });
    for (const title of ["Cache hit rate", "Spend by provider", "Spend by model", "Tokens per model", "Token consumption"]) {
      expect(chartProps<LineChartProps>(title).curve).toBeUndefined();
    }
    for (const title of lineCharts) {
      for (const row of chartTable(title).rows) {
        for (const cell of row.cells) {
          expect(cell).not.toMatch(/\d\.\d{3,}/);
        }
      }
    }
  });
});
