import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";
import { sampleFacts, sampleProfile } from "./sample";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

describe("Memory page", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("shows the profile and sample facts", async () => {
    renderAt("/memory");
    expect(await screen.findByRole("heading", { level: 1, name: "Memory" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Profile" })).toBeTruthy();
    expect(screen.getByText(sampleProfile)).toBeTruthy();
    expect(screen.getByText(sampleFacts[0]!.text)).toBeTruthy();
    expect(screen.getByText("Personal by default. Sample facts until memory exists.")).toBeTruthy();
  });

  it("asks before promote and forget", async () => {
    renderAt("/memory");
    await screen.findByRole("heading", { level: 1, name: "Memory" });
    const first = screen.getByText(sampleFacts[0]!.text).closest(".memory-row");
    expect(first).toBeTruthy();
    act(() => {
      within(first as HTMLElement).getByRole("button", { name: "Promote" }).click();
    });
    const promoteDialog = await screen.findByRole("dialog", { name: "Promote this fact?" });
    act(() => {
      within(promoteDialog).getByRole("button", { name: "Promote" }).click();
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(within(first as HTMLElement).getByText("Team · Finance")).toBeTruthy();

    act(() => {
      within(first as HTMLElement).getByRole("button", { name: "Forget" }).click();
    });
    expect(await screen.findByRole("dialog", { name: "Forget this fact?" })).toBeTruthy();
    act(() => {
      within(screen.getByRole("dialog")).getByRole("button", { name: "Forget" }).click();
    });
    expect(screen.queryByText(sampleFacts[0]!.text)).toBeNull();
  });
});
