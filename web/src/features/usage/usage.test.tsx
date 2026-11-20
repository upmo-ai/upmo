import { createMemoryHistory, type RouterHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { GroupedBarChartProps, LineChartProps } from "@upmo-ai/components";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "../../App";
import {
  createStubApi,
  type ApiClient,
  type Usage,
  type UsageBucket,
  type UsageLimits,
  type UsageRange,
  type UsageTask,
} from "../../app/api";
import { AppProviders } from "../../theme";
import { chartProps, resetChartProps } from "./chart-spy";
import { formatBucket, formatMicroUsd, formatRate, formatRatePrecise, formatTokens } from "./format";

vi.mock("@upmo-ai/components", async (importOriginal) => {
  const { withChartSpy } = await import("./chart-spy");
  return withChartSpy(await importOriginal<typeof import("@upmo-ai/components")>());
});

const noValue = "\u2014";

function statTile(label: string): HTMLElement {
  const tile = Array.from(document.querySelectorAll<HTMLElement>(".upmo-stat-tile")).find(
    (element) => element.querySelector(".upmo-stat-tile__label")?.textContent === label,
  );
  if (!tile) {
    throw new Error(`no stat tile labelled ${label}`);
  }
  return tile;
}

function tileValue(label: string): string {
  return statTile(label).querySelector(".upmo-stat-tile__value")?.textContent ?? "";
}

function taskTable(): HTMLElement {
  return screen.getByRole("table", { name: "Task breakdown" });
}

function taskGoals(): string[] {
  return within(taskTable())
    .getAllByRole("link")
    .map((link) => link.textContent ?? "");
}

function taskRow(goal: string): HTMLElement {
  return within(taskTable()).getByRole("link", { name: goal }).closest("tr")!;
}

function sortHeader(label: string): HTMLElement {
  // The first header row is the task table's own; an open model split has headers of its own below it.
  return within(taskTable().querySelector("thead")!).getByRole("columnheader", { name: label });
}

function renderAt(path: string, client?: ApiClient): RouterHistory {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
  return history;
}

function withUsage(change: (usage: Usage, range: UsageRange) => Usage, asked?: UsageRange[]): ApiClient {
  const stub = createStubApi();
  return {
    ...stub,
    usage: async (range) => {
      asked?.push(range);
      return change(await stub.usage(range), range);
    },
  };
}

function sum(values: number[]): number {
  return values.reduce((total, value) => total + value, 0);
}

function rangeParam(history: RouterHistory): string | null {
  return new URLSearchParams(history.location.search).get("range");
}

function plainTask(id: string, cost: number | null, tokens: number): UsageTask {
  return {
    task_id: id,
    goal: id,
    input: tokens,
    output: 0,
    cache_read: 0,
    cache_write: 0,
    cost_micro_usd: cost,
    by_model: [],
  };
}

describe("Usage page", () => {
  beforeEach(() => {
    localStorage.clear();
    resetChartProps();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-204 usage panels: shows the figures, charts and task table from the API", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    expect(await screen.findByRole("heading", { level: 1, name: "Usage" })).toBeTruthy();
    expect(await screen.findByText("Last 7 days. Figures come from the ledger.")).toBeTruthy();
    expect(screen.queryByText(/Sample figures/)).toBeNull();
    expect(screen.queryByText("Usage shows up here")).toBeNull();

    expect(await screen.findByRole("group", { name: "Spend" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Cost per task" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Cache hit rate" })).toBeTruthy();
    expect(screen.queryByRole("group", { name: "Tokens per task" })).toBeNull();
    expect(screen.getByRole("heading", { level: 2, name: "Tasks" })).toBeTruthy();

    const spendTable = screen.getByRole("table", { name: "Spend" });
    const unknown = week.buckets.find((bucket) => bucket.spend_micro_usd === null)!;
    const unknownRow = within(spendTable).getByRole("row", {
      name: new RegExp(formatBucket(unknown.at, "day")),
    });
    // The chart keeps the day as a gap, which its table words as No data. It is never a bar of zero.
    expect(within(unknownRow).getByText("No data")).toBeTruthy();
    expect(within(unknownRow).queryByText(formatMicroUsd(0))).toBeNull();
  });

  it("usage polish 4113376c: Spend is a vertical grouped bar chart of one bar named Spend, with unknown spend as a gap", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("table", { name: "Spend" });
    const props = chartProps<GroupedBarChartProps>("Spend");
    expect(props.orientation ?? "vertical").toBe("vertical");
    expect(props.formatValue).toBe(formatMicroUsd);
    expect(props.groups.map((group) => group.label)).toEqual(week.buckets.map((item) => formatBucket(item.at, "day")));
    expect(props.groups.every((group) => group.bars.length === 1 && group.bars[0]!.name === "Spend")).toBe(true);
    expect(props.groups.map((group) => group.bars[0]!.value)).toEqual(week.buckets.map((item) => item.spend_micro_usd));
    expect(props.groups.some((group) => group.bars[0]!.value === null)).toBe(true);
    expect(props.groups.some((group) => group.bars[0]!.display === formatMicroUsd(null))).toBe(false);
  });

  it("usage polish 4113376c: Cache hit rate is a one-series line chart with an area, in percent from 0 to 100", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("table", { name: "Cache hit rate" });
    const props = chartProps<LineChartProps>("Cache hit rate");
    expect(props.series).toHaveLength(1);
    expect(props.area).toBe(true);
    expect(props.stacked ?? false).toBe(false);
    const points = props.series![0]!.points;
    expect(points).toHaveLength(week.buckets.length);
    for (const point of points) {
      expect(point.value === null || (point.value >= 0 && point.value <= 100)).toBe(true);
    }
    expect(props.formatValue?.(66.04)).toBe("66%");
    expect(props.formatValue?.(100)).toBe("100%");
    const table = within(screen.getByRole("table", { name: "Cache hit rate" }));
    expect(table.getAllByRole("columnheader").map((cell) => cell.textContent)).toEqual(["Label", "Cache hit rate"]);
    expect(table.getAllByRole("row")[1]!.textContent).toMatch(/%$/);
  });

  it("usage polish 4113376c: Cost per task is a horizontal grouped bar chart, one bar per task", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("table", { name: "Cost per task" });
    const props = chartProps<GroupedBarChartProps>("Cost per task");
    expect(props.orientation).toBe("horizontal");
    expect(props.formatValue).toBe(formatMicroUsd);
    expect(props.groups).toHaveLength(Math.min(8, week.tasks.length));
    expect(props.groups.every((group) => group.bars.length === 1)).toBe(true);
  });

  it("usage panels: the range selector offers Today, 7, 30 and 90 days and starts on 7 days", async () => {
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    const group = screen.getByRole("radiogroup", { name: "Range" });
    expect(within(group).getAllByRole("radio").map((radio) => (radio as HTMLInputElement).value)).toEqual([
      "today",
      "7",
      "30",
      "90",
    ]);
    for (const name of ["Today", "7 days", "30 days", "90 days"]) {
      expect(within(group).getByRole("radio", { name })).toBeTruthy();
    }
    expect(within(group).getByRole("radio", { name: "7 days" })).toHaveProperty("checked", true);
  });

  it("usage panels: a range switch changes the data, the description and the URL", async () => {
    const asked: UsageRange[] = [];
    const stub = createStubApi();
    const week = await stub.usage(7);
    const month = await stub.usage(30);
    const quarter = await stub.usage(90);
    const history = renderAt("/usage", withUsage((usage) => usage, asked));
    const spent = (buckets: UsageBucket[]) => formatMicroUsd(sum(buckets.map((bucket) => bucket.spend_micro_usd ?? 0)));

    await waitFor(() => expect(tileValue("Spend")).toBe(spent(week.buckets)));
    expect(rangeParam(history)).toBeNull();

    fireEvent.click(screen.getByRole("radio", { name: "30 days" }));
    expect(await screen.findByText("Last 30 days. Figures come from the ledger.")).toBeTruthy();
    await waitFor(() => expect(tileValue("Spend")).toBe(spent(month.buckets)));
    expect(rangeParam(history)).toBe("30");
    expect(history.location.pathname).toBe("/usage");
    expect(screen.getByRole("radio", { name: "30 days" })).toHaveProperty("checked", true);
    expect(within(screen.getByRole("table", { name: "Spend" })).getAllByRole("row")).toHaveLength(
      month.buckets.length + 1,
    );
    expect(taskGoals()).toHaveLength(month.tasks.length);

    fireEvent.click(screen.getByRole("radio", { name: "90 days" }));
    expect(await screen.findByText("Last 90 days. Figures come from the ledger.")).toBeTruthy();
    await waitFor(() => expect(tileValue("Spend")).toBe(spent(quarter.buckets)));
    expect(rangeParam(history)).toBe("90");
    expect(taskGoals()).toHaveLength(quarter.tasks.length);

    fireEvent.click(screen.getByRole("radio", { name: "7 days" }));
    expect(await screen.findByText("Last 7 days. Figures come from the ledger.")).toBeTruthy();
    await waitFor(() => expect(tileValue("Spend")).toBe(spent(week.buckets)));
    expect(rangeParam(history)).toBe("7");
    expect(taskGoals()).toHaveLength(week.tasks.length);
    expect(taskGoals()).not.toContain("Weekly close");
    expect(asked).toEqual([7, 30, 90]);
  });

  it("usage panels: opens on the range in the URL and falls back to 7 days for a range it does not know", async () => {
    const asked: UsageRange[] = [];
    renderAt("/usage?range=90", withUsage((usage) => usage, asked));
    expect(await screen.findByText("Last 90 days. Figures come from the ledger.")).toBeTruthy();
    expect(screen.getByRole("radio", { name: "90 days" })).toHaveProperty("checked", true);
    expect(asked).toEqual([90]);
    cleanup();

    renderAt("/usage?range=today");
    expect(await screen.findByText("Today. Figures come from the ledger.")).toBeTruthy();
    expect(screen.getByRole("radio", { name: "Today" })).toHaveProperty("checked", true);
    cleanup();

    renderAt("/usage?range=11");
    expect(await screen.findByText("Last 7 days. Figures come from the ledger.")).toBeTruthy();
    expect(screen.getByRole("radio", { name: "7 days" })).toHaveProperty("checked", true);
  });

  it("usage panels: Today shows hourly labels and a description for the day", async () => {
    const today = await createStubApi().usage("today");
    const history = renderAt("/usage");
    await screen.findByRole("table", { name: "Spend" });

    fireEvent.click(screen.getByRole("radio", { name: "Today" }));
    expect(await screen.findByText("Today. Figures come from the ledger.")).toBeTruthy();
    expect(screen.queryByText(/Last \d+ days/)).toBeNull();
    expect(rangeParam(history)).toBe("today");

    await waitFor(() => {
      const rows = within(screen.getByRole("table", { name: "Spend" })).getAllByRole("row").slice(1);
      expect(rows).toHaveLength(today.buckets.length);
    });
    const rows = within(screen.getByRole("table", { name: "Spend" })).getAllByRole("row").slice(1);
    const labels = rows.map((row) => within(row).getAllByRole("rowheader")[0]!.textContent);
    expect(labels[0]).toBe("00:00");
    expect(labels[labels.length - 1]).toBe("11:00");
    expect(labels.every((label) => /^\d\d:00$/.test(label ?? ""))).toBe(true);
    expect(screen.getByText("00:00 to 11:00")).toBeTruthy();
    expect(within(screen.getByRole("table", { name: "Cache hit rate" })).getAllByRole("row")).toHaveLength(
      today.buckets.length + 1,
    );
    expect(within(statTile("Spend")).getByText("Excludes 1 hour with unknown cost")).toBeTruthy();
  });

  it("usage panels: the stat tiles show the figures of the range", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });

    const requests = sum(week.buckets.map((bucket) => bucket.requests));
    const errors = sum(week.buckets.map((bucket) => bucket.errors));
    const input = sum(week.buckets.map((bucket) => bucket.input));
    const output = sum(week.buckets.map((bucket) => bucket.output));
    const cacheRead = sum(week.buckets.map((bucket) => bucket.cache_read));
    const spend = sum(week.buckets.map((bucket) => bucket.spend_micro_usd ?? 0));

    const labels = Array.from(document.querySelectorAll(".upmo-stat-tile__label")).map((label) => label.textContent);
    expect(labels).toEqual([
      "Spend",
      "Requests",
      "Error rate",
      "Input tokens",
      "Output tokens",
      "Cache hit rate",
    ]);
    expect(tileValue("Spend")).toBe(formatMicroUsd(spend));
    expect(tileValue("Requests")).toBe(formatTokens(requests));
    expect(tileValue("Error rate")).toBe(formatRatePrecise(errors / requests));
    expect(tileValue("Input tokens")).toBe(formatTokens(input));
    expect(within(statTile("Input tokens")).getByText(`${formatTokens(cacheRead)} cached reads`)).toBeTruthy();
    expect(tileValue("Output tokens")).toBe(formatTokens(output));
    expect(tileValue("Cache hit rate")).toBe(formatRate(cacheRead / (input + cacheRead)));
    expect(within(statTile("Spend")).getByText("Excludes 1 day with unknown cost")).toBeTruthy();
  });

  it("usage panels: says Unknown, never zero, when every bucket in the range has unknown spend", async () => {
    const client = withUsage((usage) => ({
      ...usage,
      spend_micro_usd: null,
      buckets: usage.buckets.map((bucket) => ({ ...bucket, spend_micro_usd: null })),
      tasks: usage.tasks.map((task) => ({
        ...task,
        cost_micro_usd: null,
        by_model: task.by_model.map((part) => ({ ...part, cost_micro_usd: null })),
      })),
    }));
    renderAt("/usage", client);
    await screen.findByRole("table", { name: "Task breakdown" });
    const tile = statTile("Spend");
    expect(within(tile).getByText("Unknown")).toBeTruthy();
    expect(within(tile).getByText("Cost is unknown for every day in this period")).toBeTruthy();
    // The charts draw zero on their value axes, so look at the Spend tile and chart instead of the whole page.
    expect(within(tile).queryByText(formatMicroUsd(0))).toBeNull();
    expect(within(screen.getByRole("table", { name: "Spend" })).queryByText(formatMicroUsd(0))).toBeNull();
    expect(screen.queryByText(/Excludes/)).toBeNull();
    const chart = screen.getByRole("table", { name: "Cost per task" });
    expect(within(chart).queryByText(formatMicroUsd(0))).toBeNull();
    expect(within(taskTable()).queryByText(formatMicroUsd(0))).toBeNull();
  });

  it("usage panels: shows a zero total when known spend really is zero", async () => {
    renderAt(
      "/usage",
      withUsage((usage) => ({
        ...usage,
        spend_micro_usd: 0,
        buckets: usage.buckets.map((bucket) => ({ ...bucket, spend_micro_usd: 0 })),
      })),
    );
    await screen.findByRole("table", { name: "Task breakdown" });
    expect(within(statTile("Spend")).getByText(formatMicroUsd(0))).toBeTruthy();
    expect(screen.queryByText(/Excludes/)).toBeNull();
  });

  it("usage panels: notes the calls with no configured price and pluralises the note", async () => {
    const stub = createStubApi();
    const week = await stub.usage(7);
    expect(week.unpriced_calls).toBeGreaterThan(1);
    renderAt("/usage");
    expect(
      await screen.findByText(
        `${formatTokens(week.unpriced_calls)} calls in range had no configured price and are excluded from spend.`,
      ),
    ).toBeTruthy();
    cleanup();

    renderAt("/usage", withUsage((usage) => ({ ...usage, unpriced_calls: 1 })));
    expect(
      await screen.findByText("1 call in range had no configured price and is excluded from spend."),
    ).toBeTruthy();
    expect(screen.queryByText(/calls in range/)).toBeNull();
    cleanup();

    renderAt("/usage", withUsage((usage) => ({ ...usage, unpriced_calls: 0 })));
    await screen.findByRole("table", { name: "Task breakdown" });
    expect(screen.queryByText(/no configured price/)).toBeNull();
  });

  it("usage panels: the cost per task chart lists the tasks by cost and puts unknown cost last as Unknown", async () => {
    const month = await createStubApi().usage(30);
    expect(month.tasks.some((task) => task.cost_micro_usd === null)).toBe(true);
    renderAt("/usage?range=30");
    const chart = await screen.findByRole("table", { name: "Cost per task" });
    const rows = within(chart).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(month.tasks.length);
    const known = month.tasks
      .filter((task) => task.cost_micro_usd !== null)
      .sort((a, b) => b.cost_micro_usd! - a.cost_micro_usd!)
      .map((task) => task.goal);
    const unknown = month.tasks.filter((task) => task.cost_micro_usd === null).map((task) => task.goal);
    expect(rows.map((row) => within(row).getAllByRole("rowheader")[0]!.textContent)).toEqual([...known, ...unknown]);
    const last = rows[rows.length - 1]!;
    expect(within(last).getByText("No data")).toBeTruthy();
    expect(within(last).queryByText(formatMicroUsd(0))).toBeNull();
    // The bar is a gap, and the task table below still says Unknown for the task.
    const bars = chartProps<GroupedBarChartProps>("Cost per task").groups;
    expect(bars[bars.length - 1]!.bars[0]!.value).toBeNull();
    expect(within(taskRow(unknown[unknown.length - 1]!)).getByText("Unknown")).toBeTruthy();
  });

  it("usage panels: the cost per task chart keeps the 8 costliest tasks, then unknown cost, and the table keeps them all", async () => {
    const tasks = [
      ...Array.from({ length: 7 }, (_, index) => plainTask(`known-${index + 1}`, index + 1, 10)),
      plainTask("unknown-1", null, 10),
      plainTask("unknown-2", null, 10),
      plainTask("unknown-3", null, 10),
    ];
    renderAt("/usage", withUsage((usage) => ({ ...usage, tasks })));
    const chart = await screen.findByRole("table", { name: "Cost per task" });
    const rows = within(chart).getAllByRole("row").slice(1);
    expect(rows.map((row) => within(row).getAllByRole("rowheader")[0]!.textContent)).toEqual([
      "known-7",
      "known-6",
      "known-5",
      "known-4",
      "known-3",
      "known-2",
      "known-1",
      "unknown-1",
    ]);
    expect(within(rows[rows.length - 1]!).getByText("No data")).toBeTruthy();
    expect(taskGoals()).toHaveLength(10);
  });

  it("usage panels: lists each task with tokens, cost and share, and keeps unknown cost as Unknown", async () => {
    const week = await createStubApi().usage(7);
    const spend = sum(week.buckets.map((bucket) => bucket.spend_micro_usd ?? 0));
    renderAt("/usage");

    const table = await screen.findByRole("table", { name: "Task breakdown" });
    const headers = within(table)
      .getAllByRole("columnheader")
      .map((header) => header.textContent);
    expect(headers).toEqual([
      "Model split",
      "Task",
      "Tokens in",
      "Tokens out",
      "Cache read",
      "Cache write",
      "Tokens",
      "Cost",
      "Share",
    ]);
    expect(taskGoals()).toHaveLength(week.tasks.length);

    const known = week.tasks.find((task) => task.cost_micro_usd !== null)!;
    const knownRow = taskRow(known.goal);
    expect(within(knownRow).getByText(formatMicroUsd(known.cost_micro_usd))).toBeTruthy();
    expect(within(knownRow).getByText(formatTokens(known.input))).toBeTruthy();
    expect(within(knownRow).getByText(formatTokens(known.cache_read))).toBeTruthy();
    expect(within(knownRow).getByText(formatTokens(known.input + known.output + known.cache_read + known.cache_write))).toBeTruthy();
    expect(within(knownRow).getByText(formatRatePrecise(known.cost_micro_usd! / spend))).toBeTruthy();

    const unknown = week.tasks.find((task) => task.cost_micro_usd === null)!;
    const unknownRow = taskRow(unknown.goal);
    expect(within(unknownRow).getByText("Unknown")).toBeTruthy();
    expect(within(unknownRow).queryByText(formatMicroUsd(0))).toBeNull();
  });

  it("usage panels: the share is a dash when the cost of the task is unknown", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    const unknown = week.tasks.find((task) => task.cost_micro_usd === null)!;
    const cells = within(taskRow(unknown.goal)).getAllByRole("cell");
    expect(cells[cells.length - 1]!.textContent).toBe(noValue);
    expect(cells[cells.length - 2]!.textContent).toBe("Unknown");
    for (const task of week.tasks.filter((item) => item.cost_micro_usd !== null)) {
      const known = within(taskRow(task.goal)).getAllByRole("cell");
      expect(known[known.length - 1]!.textContent).not.toBe(noValue);
      expect(known[known.length - 1]!.textContent).toMatch(/%$/);
    }
  });

  it("usage panels: the task name links to the task page", async () => {
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    const link = within(taskTable()).getByRole("link", { name: "Daily finance digest" });
    expect(link.getAttribute("href")).toBe("/tasks/aster-digest");
  });

  it("usage panels: sorts the task table by cost, tokens and share, with aria-sort, unknown last and ties kept in order", async () => {
    const tasks = [
      plainTask("alpha", 5, 100),
      plainTask("bravo", 9, 50),
      plainTask("charlie", null, 900),
      plainTask("delta", 5, 70),
      plainTask("echo", 1, 300),
    ];
    renderAt("/usage", withUsage((usage) => ({ ...usage, tasks })));
    await screen.findByRole("table", { name: "Task breakdown" });

    const sorts = () => ["Tokens", "Cost", "Share"].map((label) => sortHeader(label).getAttribute("aria-sort"));
    expect(taskGoals()).toEqual(["bravo", "alpha", "delta", "echo", "charlie"]);
    expect(sorts()).toEqual(["none", "descending", "none"]);

    const button = (label: string) => within(sortHeader(label)).getByRole("button", { name: label });
    fireEvent.click(button("Cost"));
    expect(taskGoals()).toEqual(["echo", "alpha", "delta", "bravo", "charlie"]);
    expect(sorts()).toEqual(["none", "ascending", "none"]);

    fireEvent.click(button("Tokens"));
    expect(taskGoals()).toEqual(["charlie", "echo", "alpha", "delta", "bravo"]);
    expect(sorts()).toEqual(["descending", "none", "none"]);

    fireEvent.click(button("Tokens"));
    expect(taskGoals()).toEqual(["bravo", "delta", "alpha", "echo", "charlie"]);
    expect(sorts()).toEqual(["ascending", "none", "none"]);

    fireEvent.click(button("Share"));
    expect(taskGoals()).toEqual(["bravo", "alpha", "delta", "echo", "charlie"]);
    expect(sorts()).toEqual(["none", "none", "descending"]);

    fireEvent.click(button("Share"));
    expect(taskGoals()).toEqual(["echo", "alpha", "delta", "bravo", "charlie"]);
    expect(sorts()).toEqual(["none", "none", "ascending"]);

    fireEvent.click(button("Cost"));
    expect(taskGoals()).toEqual(["bravo", "alpha", "delta", "echo", "charlie"]);
    expect(sorts()).toEqual(["none", "descending", "none"]);
  });

  it("usage panels: tasks with equal tokens keep the order the API sent them in, ascending and descending", async () => {
    const tasks = [
      plainTask("alpha", 1, 100),
      plainTask("bravo", 1, 400),
      plainTask("charlie", 1, 100),
      plainTask("delta", 1, 400),
    ];
    renderAt("/usage", withUsage((usage) => ({ ...usage, tasks })));
    await screen.findByRole("table", { name: "Task breakdown" });
    expect(taskGoals()).toEqual(["alpha", "bravo", "charlie", "delta"]);

    const tokens = () => within(sortHeader("Tokens")).getByRole("button", { name: "Tokens" });
    fireEvent.click(tokens());
    expect(sortHeader("Tokens").getAttribute("aria-sort")).toBe("descending");
    expect(taskGoals()).toEqual(["bravo", "delta", "alpha", "charlie"]);

    fireEvent.click(tokens());
    expect(sortHeader("Tokens").getAttribute("aria-sort")).toBe("ascending");
    expect(taskGoals()).toEqual(["alpha", "charlie", "bravo", "delta"]);
  });

  it("usage panels: the model split button points at the split only while it is in the page", async () => {
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    const label = "the model split for Daily finance digest";
    const closed = within(taskRow("Daily finance digest")).getByRole("button", { name: `Show ${label}` });
    expect(closed.hasAttribute("aria-controls")).toBe(false);

    fireEvent.click(closed);
    const open = within(taskRow("Daily finance digest")).getByRole("button", { name: `Hide ${label}` });
    const target = open.getAttribute("aria-controls");
    expect(target).toBeTruthy();
    expect(document.getElementById(target!)).toBeTruthy();

    fireEvent.click(open);
    const again = within(taskRow("Daily finance digest")).getByRole("button", { name: `Show ${label}` });
    expect(again.hasAttribute("aria-controls")).toBe(false);
  });

  it("usage panels: the task table is named once, not by its scroll container too", async () => {
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    expect(screen.getAllByLabelText("Task breakdown")).toHaveLength(1);
  });

  it("usage panels: the sort headers are buttons, so the keyboard reaches and operates them", async () => {
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    for (const label of ["Tokens", "Cost", "Share"]) {
      const button = within(sortHeader(label)).getByRole("button", { name: label });
      expect(button.tagName).toBe("BUTTON");
      expect(button.getAttribute("type")).toBe("button");
      expect(button.hasAttribute("disabled")).toBe(false);
      expect(button.getAttribute("tabindex")).not.toBe("-1");
    }
    for (const label of ["Task", "Tokens in", "Tokens out", "Cache read", "Cache write"]) {
      expect(sortHeader(label).hasAttribute("aria-sort")).toBe(false);
    }
  });

  it("usage panels: orders the task table by cost, biggest first, from the API data", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    const expected = week.tasks
      .filter((task) => task.cost_micro_usd !== null)
      .sort((a, b) => b.cost_micro_usd! - a.cost_micro_usd!)
      .map((task) => task.goal)
      .concat(week.tasks.filter((task) => task.cost_micro_usd === null).map((task) => task.goal));
    expect(taskGoals()).toEqual(expected);
  });

  it("usage panels: expanding a task splits its cost by model and token kind, and the parts add up to the task", async () => {
    const week = await createStubApi().usage(7);
    const task = week.tasks.find((item) => item.goal === "Daily finance digest")!;
    expect(task.by_model.length).toBeGreaterThan(1);
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });

    const open = within(taskRow("Daily finance digest")).getByRole("button", {
      name: "Show the model split for Daily finance digest",
    });
    expect(open.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("table", { name: "Cost split for Daily finance digest" })).toBeNull();

    fireEvent.click(open);
    const split = await screen.findByRole("table", { name: "Cost split for Daily finance digest" });
    const toggle = within(taskRow("Daily finance digest")).getByRole("button", {
      name: "Hide the model split for Daily finance digest",
    });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const controlled = document.getElementById(toggle.getAttribute("aria-controls") ?? "");
    expect(controlled).toBeTruthy();
    expect(controlled!.contains(split)).toBe(true);

    expect(within(split).getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "Model",
      "Tokens in",
      "Tokens out",
      "Cache read",
      "Cache write",
      "Cost",
    ]);
    const parts = within(split).getAllByRole("row").slice(1);
    expect(parts).toHaveLength(task.by_model.length);
    const names = parts.map((row) => within(row).getAllByRole("cell")[0]!.textContent);
    expect(names).toEqual(["Claude 3.7 Sonnet", "Claude 3.5 Haiku"]);
    task.by_model.forEach((part, index) => {
      const cells = within(parts[index]!).getAllByRole("cell").map((cell) => cell.textContent);
      expect(cells.slice(1)).toEqual([
        formatTokens(part.input),
        formatTokens(part.output),
        formatTokens(part.cache_read),
        formatTokens(part.cache_write),
        formatMicroUsd(part.cost_micro_usd),
      ]);
    });
    expect(sum(task.by_model.map((part) => part.cost_micro_usd ?? 0))).toBe(task.cost_micro_usd!);

    fireEvent.click(toggle);
    expect(screen.queryByRole("table", { name: "Cost split for Daily finance digest" })).toBeNull();
    expect(
      within(taskRow("Daily finance digest"))
        .getByRole("button", { name: "Show the model split for Daily finance digest" })
        .getAttribute("aria-expanded"),
    ).toBe("false");
  });

  it("usage panels: a task with unknown cost expands to models with Unknown cost, not zero", async () => {
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    fireEvent.click(
      within(taskRow("Fix the flaky login test")).getByRole("button", { name: "Show the model split for Fix the flaky login test" }),
    );
    const split = await screen.findByRole("table", { name: "Cost split for Fix the flaky login test" });
    const part = within(split).getAllByRole("row")[1]!;
    expect(within(part).getByText("Llama 3.3 70B")).toBeTruthy();
    expect(within(part).getByText("Unknown")).toBeTruthy();
    expect(within(part).queryByText(formatMicroUsd(0))).toBeNull();
  });

  it("usage panels: keeps a task open while the table is sorted", async () => {
    renderAt("/usage");
    await screen.findByRole("table", { name: "Task breakdown" });
    fireEvent.click(
      within(taskRow("Daily finance digest")).getByRole("button", { name: "Show the model split for Daily finance digest" }),
    );
    await screen.findByRole("table", { name: "Cost split for Daily finance digest" });
    fireEvent.click(within(sortHeader("Cost")).getByRole("button", { name: "Cost" }));
    expect(screen.getByRole("table", { name: "Cost split for Daily finance digest" })).toBeTruthy();
    expect(taskGoals()[taskGoals().length - 1]).toBe("Fix the flaky login test");
  });

  it("usage panels: shows the empty states when nothing was recorded in the range", async () => {
    const client = createStubApi({ emptyUsage: true });
    const history = renderAt("/usage", client);
    expect(await screen.findByRole("heading", { level: 2, name: "No usage in this range" })).toBeTruthy();
    expect(screen.getByText("No task usage in this range.")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Tasks" })).toBeTruthy();
    expect(screen.queryByRole("table", { name: "Task breakdown" })).toBeNull();
    expect(document.querySelectorAll(".upmo-stat-tile")).toHaveLength(0);
    expect(screen.queryByRole("group", { name: "Spend" })).toBeNull();
    expect(screen.queryByText(formatMicroUsd(0))).toBeNull();
    expect(screen.queryByText("Unknown")).toBeNull();
    expect(screen.queryByText(/no configured price/)).toBeNull();

    fireEvent.click(screen.getByRole("radio", { name: "Today" }));
    expect(await screen.findByText("Today. Figures come from the ledger.")).toBeTruthy();
    await waitFor(() => expect(rangeParam(history)).toBe("today"));
    expect(screen.getByRole("heading", { level: 2, name: "No usage in this range" })).toBeTruthy();
    expect(screen.getByText("No task usage in this range.")).toBeTruthy();
  });

  it("usage panels: says so when the ledger cannot be read", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      usage: async () => {
        throw new Error("The ledger is not reachable.");
      },
    };
    renderAt("/usage", client);
    expect((await screen.findByRole("alert")).textContent).toBe("The ledger is not reachable.");
    expect(screen.queryByRole("table", { name: "Task breakdown" })).toBeNull();
  });
});

/** A person who is not an admin: the brain sends them no limit rows and refuses the workspace route. */
async function memberClient(): Promise<ApiClient> {
  const client = createStubApi();
  await client.me.viewAs("member");
  return client;
}

function limits(own: Partial<UsageLimits["own"]>): UsageLimits {
  return {
    period_start: "2026-11-01",
    own: { limit_micro_usd: null, workspace_limit_micro_usd: null, spent_micro_usd: 0, held_micro_usd: 0, ...own },
  };
}

function meter(name: string): HTMLElement {
  return screen.getByRole("progressbar", { name });
}

describe("Usage page limits", () => {
  beforeEach(() => {
    localStorage.clear();
    resetChartProps();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-359 limit bars: shows used of limit for each limit that is set, and the 80% words from 80% on", async () => {
    renderAt("/usage");
    const panel = (await screen.findByRole("heading", { level: 2, name: "Monthly limits" })).parentElement!;
    expect(within(panel).getByText("Month starting 1 Nov.")).toBeTruthy();
    expect(within(panel).getByText("Your limit")).toBeTruthy();
    expect(within(panel).getByText("Workspace limit")).toBeTruthy();
    // The stub's person limit is $50.00 with $42.00 spent: 84%, past the 80% mark.
    expect(meter("Your limit").getAttribute("aria-valuenow")).toBe("84");
    // Both bars show the same $42.00 spent.
    expect(within(panel).getAllByText("US$42.00", { selector: ".upmo-usage-meter__figures" })).toHaveLength(2);
    expect(within(panel).getAllByText(/\/ US\$50\.00/).length).toBe(1);
    expect(within(panel).getByText("84% of the limit is used")).toBeTruthy();
    // The workspace limit is $200.00: 21% used, so no words.
    expect(meter("Workspace limit").getAttribute("aria-valuenow")).toBe("21");
    expect(within(panel).getAllByText(/of the limit is used/)).toHaveLength(1);
  });

  it("UPM-359 limit bars: 79% is not warned about and 80% is, from the whole-micro-USD figures", async () => {
    renderAt("/usage", { ...createStubApi(), usageLimits: async () => limits({ limit_micro_usd: 100_000_000, spent_micro_usd: 79_999_999 }) });
    await screen.findByRole("progressbar", { name: "Your limit" });
    expect(screen.queryByText(/of the limit is used/)).toBeNull();
    cleanup();
    renderAt("/usage", { ...createStubApi(), usageLimits: async () => limits({ limit_micro_usd: 100_000_000, spent_micro_usd: 80_000_000 }) });
    expect(await screen.findByText("80% of the limit is used")).toBeTruthy();
  });

  it("UPM-359 limit bars: says the limit is reached at the limit, and names what is held", async () => {
    renderAt("/usage", {
      ...createStubApi(),
      usageLimits: async () => limits({ limit_micro_usd: 10_000_000, spent_micro_usd: 10_500_000, held_micro_usd: 250_000 }),
    });
    expect(await screen.findByText("Limit reached")).toBeTruthy();
    expect(meter("Your limit").getAttribute("aria-valuenow")).toBe("100");
    expect(screen.getByText("US$0.25 is held for calls in progress")).toBeTruthy();
  });

  it("UPM-359 limit bars: no limit set shows no bar and says so", async () => {
    renderAt("/usage", { ...createStubApi(), usageLimits: async () => limits({}) });
    expect(await screen.findByText("No monthly spend limit is set.")).toBeTruthy();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });

  it("UPM-359 limit bars: a limit set with nothing used is a bar at a real US$0.00", async () => {
    renderAt("/usage", { ...createStubApi(), usageLimits: async () => limits({ limit_micro_usd: 50_000_000 }) });
    const bar = await screen.findByRole("progressbar", { name: "Your limit" });
    expect(bar.getAttribute("aria-valuenow")).toBe("0");
    const panel = screen.getByRole("heading", { level: 2, name: "Monthly limits" }).parentElement!;
    expect(within(panel).getByText("US$0.00", { selector: ".upmo-usage-meter__figures" })).toBeTruthy();
    expect(within(panel).queryByText("Unknown")).toBeNull();
    expect(screen.queryByText(/of the limit is used/)).toBeNull();
    expect(screen.queryByRole("progressbar", { name: "Workspace limit" })).toBeNull();
  });

  it("UPM-359 limit bars: says so when the limits cannot be read, and the rest of the page still loads", async () => {
    renderAt("/usage", {
      ...createStubApi(),
      usageLimits: async () => {
        throw new Error("limits are down");
      },
    });
    expect((await screen.findAllByRole("alert")).map((alert) => alert.textContent)).toContain("limits are down");
    expect(await screen.findByRole("table", { name: "Task breakdown" })).toBeTruthy();
    expect(screen.queryByRole("tab", { name: "Workspace" })).toBeNull();
  });
});

describe("Usage page workspace tab", () => {
  beforeEach(() => {
    localStorage.clear();
    resetChartProps();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-359 access: a member sees no Workspace tab and no way to ask for the workspace's usage", async () => {
    const asked: string[] = [];
    const client = await memberClient();
    renderAt("/usage", {
      ...client,
      workspaceUsage: async (range, group) => {
        asked.push(`${range} ${group}`);
        return client.workspaceUsage(range, group);
      },
    });
    await screen.findByRole("table", { name: "Task breakdown" });
    expect(screen.queryByRole("tab", { name: "Workspace" })).toBeNull();
    expect(screen.queryByRole("tablist")).toBeNull();
    expect(screen.queryByText("Export CSV")).toBeNull();
    expect(asked).toEqual([]);
  });

  it("UPM-359 access: an admin sees the tab, groups the workspace and gets the server's CSV for that group", async () => {
    const asked: string[] = [];
    const client = createStubApi();
    renderAt("/usage?range=30", {
      ...client,
      workspaceUsage: async (range, group) => {
        asked.push(`${range} ${group}`);
        return client.workspaceUsage(range, group);
      },
    });
    const tab = await screen.findByRole("tab", { name: "Workspace" });
    expect(screen.getByRole("tab", { name: "Your usage" }).getAttribute("aria-selected")).toBe("true");
    // The workspace is not read until the tab is opened.
    expect(asked).toEqual([]);
    fireEvent.click(tab);
    const table = await screen.findByRole("table", { name: "Workspace usage" });
    expect(asked).toEqual(["30 person"]);
    expect(within(table).getByRole("columnheader", { name: "Person" })).toBeTruthy();
    expect(within(table).getByText("Sam")).toBeTruthy();
    expect(within(table).getByText("Lee")).toBeTruthy();
    const csv = screen.getByRole("link", { name: "Export CSV" });
    expect(csv.getAttribute("href")).toBe("/api/usage/workspace?range=30&group=person&format=csv");
    expect(csv.hasAttribute("download")).toBe(true);

    fireEvent.click(screen.getByRole("combobox", { name: "Group by" }));
    const listbox = screen.getByRole("listbox", { name: "Group by", hidden: true });
    fireEvent.click(within(listbox).getByRole("option", { name: "Team", hidden: true }));
    await waitFor(() => expect(asked).toEqual(["30 person", "30 team"]));
    expect(await screen.findByText("Finance")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Export CSV" }).getAttribute("href")).toBe(
      "/api/usage/workspace?range=30&group=team&format=csv",
    );
  });

  it("UPM-359 workspace rows: unknown spend is Unknown and an estimate says so, a not billed value is its own column", async () => {
    const client = createStubApi();
    renderAt("/usage", {
      ...client,
      workspaceUsage: async (range, group) => ({
        ...(await client.workspaceUsage(range, group)),
        spend_micro_usd: 7_000_000,
        estimated: true,
        list_price_micro_usd: 3_500_000,
        rows: [
          {
            id: "a",
            name: "Ada",
            requests: 4,
            errors: 0,
            input: 10,
            output: 5,
            cache_read: 0,
            cache_write: 0,
            reasoning: 0,
            tokens: 15,
            spend_micro_usd: null,
            estimated: false,
            unpriced_calls: 4,
            list_price_micro_usd: 3_500_000,
          },
          {
            id: "b",
            name: "Bo",
            requests: 1,
            errors: 0,
            input: 1,
            output: 1,
            cache_read: 0,
            cache_write: 0,
            reasoning: 0,
            tokens: 2,
            spend_micro_usd: 0,
            estimated: false,
            unpriced_calls: 0,
            list_price_micro_usd: null,
          },
          {
            id: "c",
            name: "Cy",
            requests: 1,
            errors: 0,
            input: 1,
            output: 1,
            cache_read: 0,
            cache_write: 0,
            reasoning: 0,
            tokens: 2,
            spend_micro_usd: 2_500_000,
            estimated: true,
            unpriced_calls: 0,
            list_price_micro_usd: null,
          },
        ],
      }),
    });
    fireEvent.click(await screen.findByRole("tab", { name: "Workspace" }));
    const table = await screen.findByRole("table", { name: "Workspace usage" });
    const cells = (name: string) =>
      within(within(table).getByText(name).closest("tr")!)
        .getAllByRole("cell")
        .map((cell) => cell.textContent);
    expect(cells("Ada")).toEqual(["Ada", "4", "15", "Unknown", "US$3.50"]);
    expect(cells("Bo")).toEqual(["Bo", "1", "2", "US$0.00", "\u2014"]);
    expect(cells("Cy")).toEqual(["Cy", "1", "2", "US$2.50 (estimated)", "\u2014"]);
    expect(tileValue("Workspace spend")).toBe("US$7.00 (estimated)");
    expect(tileValue("Not billed")).toBe("US$3.50");
  });

  it("UPM-359 workspace: says so when nothing was recorded and when the route refuses", async () => {
    const empty = createStubApi({ emptyUsage: true });
    renderAt("/usage", empty);
    fireEvent.click(await screen.findByRole("tab", { name: "Workspace" }));
    expect(await screen.findByRole("heading", { level: 2, name: "No workspace usage in this range" })).toBeTruthy();
    cleanup();

    const refused = createStubApi();
    renderAt("/usage", {
      ...refused,
      workspaceUsage: async () => {
        throw new Error("Only people who may view usage can see the workspace's usage.");
      },
    });
    fireEvent.click(await screen.findByRole("tab", { name: "Workspace" }));
    expect((await screen.findAllByRole("alert")).map((alert) => alert.textContent)).toContain(
      "Only people who may view usage can see the workspace's usage.",
    );
  });
});

describe("Usage page money and kinds of work", () => {
  beforeEach(() => {
    localStorage.clear();
    resetChartProps();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-359 an unknown cost is Unknown on the tile and in the kinds of work, a real zero is US$0.00", async () => {
    renderAt(
      "/usage",
      withUsage((usage) => ({
        ...usage,
        spend_micro_usd: null,
        sources: [
          { source: "chat", requests: 5, tokens: 500, spend_micro_usd: null },
          { source: "task", requests: 2, tokens: 90, spend_micro_usd: 0 },
        ],
      })),
    );
    await screen.findByRole("table", { name: "Kind of work" });
    expect(tileValue("Spend")).toBe("Unknown");
    const kinds = screen.getByRole("table", { name: "Kind of work" });
    const cells = (name: string) =>
      within(within(kinds).getByText(name).closest("tr")!)
        .getAllByRole("cell")
        .map((cell) => cell.textContent);
    expect(cells("Chat")).toEqual(["Chat", "5", "500", "Unknown"]);
    expect(cells("Tasks")).toEqual(["Tasks", "2", "90", "US$0.00"]);
  });

  it("UPM-359 the spend tile shows the server's total in whole micro-USD, pinned", async () => {
    renderAt("/usage", withUsage((usage) => ({ ...usage, spend_micro_usd: 12_345_678 })));
    await screen.findByRole("table", { name: "Kind of work" });
    expect(tileValue("Spend")).toBe("US$12.35");
  });

  it("UPM-359 an estimated spend says so on the tile and on every kind of work", async () => {
    renderAt(
      "/usage",
      withUsage((usage) => ({
        ...usage,
        estimated: true,
        spend_micro_usd: 4_000_000,
        sources: [{ source: "chat", requests: 5, tokens: 500, spend_micro_usd: 4_000_000 }],
      })),
    );
    await screen.findByRole("table", { name: "Kind of work" });
    expect(statTile("Spend").textContent).toMatch(/Includes estimated costs/);
    expect(within(screen.getByRole("table", { name: "Kind of work" })).getByText("US$4.00 (estimated)")).toBeTruthy();
  });

  it("UPM-359 not billed: subscription and local calls show at list price as their own figure, and none shows no tile", async () => {
    renderAt("/usage", withUsage((usage) => ({ ...usage, list_price_micro_usd: 3_500_000 })));
    await screen.findByRole("table", { name: "Kind of work" });
    expect(tileValue("Not billed")).toBe("US$3.50");
    expect(statTile("Not billed").textContent).toMatch(/Subscription and local calls at list price/);
    expect(tileValue("Spend")).not.toBe("US$3.50");
    cleanup();
    renderAt("/usage", withUsage((usage) => ({ ...usage, list_price_micro_usd: null })));
    await screen.findByRole("table", { name: "Kind of work" });
    expect(() => statTile("Not billed")).toThrow("no stat tile labelled Not billed");
  });

  it("UPM-359 kind of work: lists chat, tasks and automations from the API, and says so with none", async () => {
    renderAt("/usage");
    const kinds = await screen.findByRole("table", { name: "Kind of work" });
    expect(within(kinds).getAllByRole("row").slice(1).map((row) => within(row).getAllByRole("cell")[0]!.textContent)).toEqual([
      "Chat",
      "Tasks",
      "Automations",
    ]);
    cleanup();
    renderAt("/usage", withUsage((usage) => ({ ...usage, sources: [] })));
    expect(await screen.findByText("No usage by kind of work in this range.")).toBeTruthy();
  });

  it("UPM-359 time: Today's buckets are labelled with the hour the server sent, on a day the clocks change", async () => {
    renderAt(
      "/usage?range=today",
      withUsage((usage) => ({
        ...usage,
        time_zone: "Europe/Berlin",
        bucket: "hour",
        buckets: [
          { ...usage.buckets[0]!, at: "2026-03-29T01:00:00+01:00" },
          { ...usage.buckets[1]!, at: "2026-03-29T03:00:00+02:00" },
        ],
      })),
    );
    await screen.findByRole("table", { name: "Spend" });
    const rows = within(screen.getByRole("table", { name: "Spend" })).getAllByRole("row").slice(1);
    expect(rows.map((row) => within(row).getByRole("rowheader").textContent)).toEqual(["01:00", "03:00"]);
  });
});
