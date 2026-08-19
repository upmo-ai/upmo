import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";
import { formatRate, formatTokens, formatUsd } from "./format";
import { knownSpend, periodCacheHit, sampleDays, sampleTasks, totalTokens } from "./sample";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
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

  it("shows spend, tokens per task and cache hit rate from the sample", async () => {
    renderAt("/usage");
    expect(await screen.findByRole("heading", { level: 1, name: "Usage" })).toBeTruthy();
    expect(screen.queryByText("Usage shows up here")).toBeNull();
    expect(screen.getByText("Last 7 days. Sample figures until the ledger exists.")).toBeTruthy();

    const spend = formatUsd(knownSpend(sampleDays));
    const tokens = formatTokens(totalTokens(sampleDays));
    const hit = formatRate(periodCacheHit(sampleDays));
    expect(screen.getByText(spend)).toBeTruthy();
    expect(screen.getByText(tokens)).toBeTruthy();
    expect(screen.getAllByText(hit).length).toBeGreaterThan(0);

    expect(screen.getByRole("figure", { name: "Spend" })).toBeTruthy();
    expect(screen.getByRole("figure", { name: "Tokens per task" })).toBeTruthy();
    expect(screen.getByRole("figure", { name: "Cache hit rate" })).toBeTruthy();

    const spendChart = screen.getByRole("figure", { name: "Spend" });
    expect(within(spendChart).getByText("Unknown")).toBeTruthy();
    expect(within(spendChart).getByText("Tue 28")).toBeTruthy();

    const tokensChart = screen.getByRole("figure", { name: "Tokens per task" });
    expect(within(tokensChart).getByText(sampleTasks[0]!.title)).toBeTruthy();
  });
});
