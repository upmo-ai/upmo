import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { createStubApi, type ApiClient } from "../../app/api";
import { AppProviders } from "../../theme";
import { knownSpend, periodCacheHit, totalTokens } from "./figures";
import { formatDay, formatRate, formatTokens, formatUsd } from "./format";

function statTile(label: string): HTMLElement {
  const tile = Array.from(document.querySelectorAll<HTMLElement>(".upmo-stat-tile")).find(
    (element) => element.querySelector(".upmo-stat-tile__label")?.textContent === label,
  );
  if (!tile) {
    throw new Error(`no stat tile labelled ${label}`);
  }
  return tile;
}

function taskGoals(): string[] {
  const table = screen.getByRole("table", { name: "Task breakdown" });
  return within(table)
    .getAllByRole("row")
    .slice(1)
    .map((row) => within(row).getAllByRole("cell")[0]!.textContent ?? "");
}

function renderAt(path: string, client?: ApiClient) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} client={client} />
    </AppProviders>,
  );
}

describe("Usage page", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("UPM-204 shows spend, tokens per task and cache hit rate from the API", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");
    expect(await screen.findByRole("heading", { level: 1, name: "Usage" })).toBeTruthy();
    expect(screen.queryByText("Usage shows up here")).toBeNull();
    expect(await screen.findByText("Last 7 days. Figures come from the ledger.")).toBeTruthy();
    expect(screen.queryByText(/Sample figures/)).toBeNull();

    const spend = formatUsd(knownSpend(week.days));
    const tokens = formatTokens(totalTokens(week.days));
    const hit = formatRate(periodCacheHit(week.days));
    expect(await screen.findByText(spend)).toBeTruthy();
    expect(screen.getByText(tokens)).toBeTruthy();
    expect(screen.getAllByText(hit).length).toBeGreaterThan(0);

    expect(screen.getByText("Excludes 1 day with unknown cost")).toBeTruthy();

    expect(screen.getByRole("group", { name: "Spend" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Tokens per task" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Cache hit rate" })).toBeTruthy();

    const spendTable = screen.getByRole("table", { name: "Spend" });
    const unknownDay = week.days.find((day) => day.spend_usd === null)!;
    const unknownRow = within(spendTable).getByRole("row", { name: new RegExp(formatDay(unknownDay.date)) });
    expect(within(unknownRow).getByText("Unknown")).toBeTruthy();

    const tokensTable = screen.getByRole("table", { name: "Tokens per task" });
    expect(within(tokensTable).getByText(week.tasks[0]!.goal)).toBeTruthy();
  });

  it("UPM-204 lists each task with tokens in and out, cache and cost, and keeps unknown cost as Unknown", async () => {
    const week = await createStubApi().usage(7);
    renderAt("/usage");

    const table = await screen.findByRole("table", { name: "Task breakdown" });
    const headers = within(table)
      .getAllByRole("columnheader")
      .map((header) => header.textContent);
    expect(headers).toEqual(["Task", "Tokens in", "Tokens out", "Cache read", "Cache write", "Cost"]);

    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(week.tasks.length);

    const known = week.tasks.find((task) => task.cost_usd !== null)!;
    const knownRow = within(table).getByRole("row", { name: new RegExp(known.goal) });
    expect(within(knownRow).getByText(formatUsd(known.cost_usd))).toBeTruthy();
    expect(within(knownRow).getByText(formatTokens(known.input))).toBeTruthy();
    expect(within(knownRow).getByText(formatTokens(known.output))).toBeTruthy();
    expect(within(knownRow).getByText(formatTokens(known.cache_read))).toBeTruthy();

    const unknown = week.tasks.find((task) => task.cost_usd === null)!;
    const unknownRow = within(table).getByRole("row", { name: new RegExp(unknown.goal) });
    expect(within(unknownRow).getByText("Unknown")).toBeTruthy();
    expect(within(unknownRow).queryByText(formatUsd(0))).toBeNull();

    expect(taskGoals()).toEqual([
      "Weekly review",
      "Draft the report",
      "Index the handbook",
      "Summarise the board pack",
    ]);
  });

  it("UPM-204 orders the whole task table by total tokens, cache included", async () => {
    const stub = createStubApi();
    const task = (id: string, input: number, output: number, cacheRead: number, cacheWrite: number) => ({
      task_id: id,
      goal: id,
      input,
      output,
      cache_read: cacheRead,
      cache_write: cacheWrite,
      cost_usd: 1,
    });
    const client: ApiClient = {
      ...stub,
      usage: async (days) => ({
        ...(await stub.usage(days)),
        tasks: [
          task("small", 10, 10, 0, 0),
          task("cache-write", 1, 1, 0, 100),
          task("cache-read", 1, 1, 50, 0),
          task("large", 100, 100, 100, 100),
        ],
      }),
    };
    renderAt("/usage", client);
    await screen.findByRole("table", { name: "Task breakdown" });
    expect(taskGoals()).toEqual(["large", "cache-write", "cache-read", "small"]);
  });

  it("UPM-204 puts the task panel title under the page title as a heading", async () => {
    renderAt("/usage");
    expect(await screen.findByRole("heading", { level: 1, name: "Usage" })).toBeTruthy();
    expect(await screen.findByRole("heading", { level: 2, name: "Tasks" })).toBeTruthy();
  });

  it("UPM-204 says Unknown, never zero, when every day in the period has unknown spend", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      usage: async (days) => {
        const usage = await stub.usage(days);
        return { ...usage, days: usage.days.map((day) => ({ ...day, spend_usd: null })) };
      },
    };
    renderAt("/usage", client);
    await screen.findByRole("table", { name: "Task breakdown" });
    const tile = statTile("Spend");
    expect(within(tile).getByText("Unknown")).toBeTruthy();
    expect(within(tile).getByText("Cost is unknown for every day in this period")).toBeTruthy();
    expect(screen.queryByText(formatUsd(0))).toBeNull();
    expect(screen.queryByText(/Excludes/)).toBeNull();
  });

  it("UPM-204 shows a zero total when known spend really is zero", async () => {
    const stub = createStubApi();
    const client: ApiClient = {
      ...stub,
      usage: async (days) => {
        const usage = await stub.usage(days);
        return { ...usage, days: usage.days.map((day) => ({ ...day, spend_usd: 0 })) };
      },
    };
    renderAt("/usage", client);
    await screen.findByRole("table", { name: "Task breakdown" });
    expect(within(statTile("Spend")).getByText(formatUsd(0))).toBeTruthy();
  });

  it("UPM-204 changes the figures and the task table when the period changes", async () => {
    const stub = createStubApi();
    const asked: number[] = [];
    const client: ApiClient = {
      ...stub,
      usage: async (days) => {
        asked.push(days);
        return stub.usage(days);
      },
    };
    const week = await stub.usage(7);
    const month = await stub.usage(30);
    const quarter = await stub.usage(90);
    expect(month.tasks.length).toBeGreaterThan(week.tasks.length);
    expect(quarter.tasks.length).toBeGreaterThan(month.tasks.length);

    renderAt("/usage", client);
    expect(await screen.findByText(formatUsd(knownSpend(week.days)))).toBeTruthy();
    expect(screen.getByRole("radio", { name: "7 days" })).toHaveProperty("checked", true);

    fireEvent.click(screen.getByRole("radio", { name: "30 days" }));
    expect(await screen.findByText("Last 30 days. Figures come from the ledger.")).toBeTruthy();
    expect(await screen.findByText(formatUsd(knownSpend(month.days)))).toBeTruthy();
    expect(screen.getByText("Excludes 3 days with unknown cost")).toBeTruthy();
    expect(within(screen.getByRole("table", { name: "Task breakdown" })).getAllByRole("row")).toHaveLength(
      month.tasks.length + 1,
    );
    expect(within(screen.getByRole("table", { name: "Spend" })).getAllByRole("row")).toHaveLength(
      month.days.length + 1,
    );

    fireEvent.click(screen.getByRole("radio", { name: "90 days" }));
    expect(await screen.findByText("Last 90 days. Figures come from the ledger.")).toBeTruthy();
    await waitFor(() => {
      expect(within(screen.getByRole("table", { name: "Task breakdown" })).getAllByRole("row")).toHaveLength(
        quarter.tasks.length + 1,
      );
    });
    expect(screen.getByText(formatUsd(knownSpend(quarter.days)))).toBeTruthy();
    expect(asked).toEqual([7, 30, 90]);

    fireEvent.click(screen.getByRole("radio", { name: "7 days" }));
    expect(await screen.findByText("Last 7 days. Figures come from the ledger.")).toBeTruthy();
    expect(screen.getByRole("radio", { name: "7 days" })).toHaveProperty("checked", true);
    await waitFor(() => {
      expect(within(statTile("Spend")).getByText(formatUsd(knownSpend(week.days)))).toBeTruthy();
    });
    expect(screen.queryByText(formatUsd(knownSpend(quarter.days)))).toBeNull();
    expect(screen.getByText("Excludes 1 day with unknown cost")).toBeTruthy();
    expect(screen.queryByText("Excludes 3 days with unknown cost")).toBeNull();
    expect(within(screen.getByRole("table", { name: "Task breakdown" })).getAllByRole("row")).toHaveLength(
      week.tasks.length + 1,
    );
    expect(within(screen.getByRole("table", { name: "Spend" })).getAllByRole("row")).toHaveLength(
      week.days.length + 1,
    );
    expect(taskGoals()).not.toContain("Prepare the quarter close pack");
    expect(asked).toEqual([7, 30, 90]);
  });

  it("UPM-204 says so when the ledger cannot be read", async () => {
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
