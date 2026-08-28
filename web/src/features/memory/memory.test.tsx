import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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

describe("Personalisation page", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("shows the profile and sample facts under Settings", async () => {
    renderAt("/settings/personalization");
    expect(await screen.findByRole("heading", { level: 1, name: "Personalisation" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Profile" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Memory" })).toBeTruthy();
    expect(screen.getByText(sampleProfile)).toBeTruthy();
    expect(screen.getByText(sampleFacts[0]!.text)).toBeTruthy();
    expect(
      screen.getByText("What Upmo remembers about you. Facts stay personal. Documents belong in Knowledge."),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Promote" })).toBeNull();
    expect(screen.queryByText(/^Team /)).toBeNull();
    expect(screen.queryByText(/^Agent /)).toBeNull();
    const settings = screen.getByRole("navigation", { name: "Settings" });
    expect(within(settings).getByRole("link", { name: "Personalisation" }).getAttribute("aria-current")).toBe(
      "page",
    );
  });

  it("adds, edits, and forgets a fact", async () => {
    renderAt("/settings/personalization");
    await screen.findByRole("heading", { level: 1, name: "Personalisation" });

    fireEvent.change(screen.getByLabelText("Add a fact"), { target: { value: "Prefers tables over prose." } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    expect(screen.getByText("Prefers tables over prose.")).toBeTruthy();

    const first = screen.getByText(sampleFacts[0]!.text).closest(".memory-row");
    expect(first).toBeTruthy();
    act(() => {
      within(first as HTMLElement).getByRole("button", { name: "Edit" }).click();
    });
    fireEvent.change(screen.getByLabelText("Fact"), { target: { value: "Prefers British spelling in every reply." } });
    act(() => {
      screen.getByRole("button", { name: "Save" }).click();
    });
    expect(screen.getByText("Prefers British spelling in every reply.")).toBeTruthy();

    const edited = screen.getByText("Prefers British spelling in every reply.").closest(".memory-row");
    act(() => {
      within(edited as HTMLElement).getByRole("button", { name: "Forget" }).click();
    });
    expect(await screen.findByRole("dialog", { name: "Forget this fact?" })).toBeTruthy();
    act(() => {
      within(screen.getByRole("dialog")).getByRole("button", { name: "Forget" }).click();
    });
    expect(screen.queryByText("Prefers British spelling in every reply.")).toBeNull();
  });

  it("saves an edited profile", async () => {
    renderAt("/settings/personalization");
    await screen.findByRole("heading", { level: 1, name: "Personalisation" });
    act(() => {
      screen.getAllByRole("button", { name: "Edit" })[0]!.click();
    });
    fireEvent.change(screen.getByLabelText("Profile"), {
      target: { value: "Prefers short answers and tables." },
    });
    act(() => {
      screen.getByRole("button", { name: "Save" }).click();
    });
    expect(screen.getByText("Prefers short answers and tables.")).toBeTruthy();
  });
});
