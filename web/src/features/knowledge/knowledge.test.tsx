import { createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "../../App";
import { AppProviders } from "../../theme";
import { sampleFolders } from "./sample";

function renderAt(path: string) {
  const history = createMemoryHistory({ initialEntries: [path] });
  render(
    <AppProviders>
      <App history={history} />
    </AppProviders>,
  );
}

describe("Knowledge page", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
  });

  it("lists folders as cards from the sample", async () => {
    renderAt("/knowledge");
    expect(await screen.findByRole("heading", { level: 1, name: "Knowledge" })).toBeTruthy();
    const product = screen.getByRole("heading", { level: 2, name: sampleFolders[0]!.name });
    expect(product).toBeTruthy();
    expect(screen.getByText("Folders · 6")).toBeTruthy();
    expect(within(product.closest("article") as HTMLElement).getByText(/2 sources · updated/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add source" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "New folder" })).toBeTruthy();
    expect(screen.queryByText("Give Upmo something to read")).toBeNull();
    expect(screen.queryByText("Handbook.pdf")).toBeNull();
  });

  it("adds a folder from the header action", async () => {
    renderAt("/knowledge");
    await screen.findByRole("heading", { level: 1, name: "Knowledge" });
    act(() => {
      screen.getByRole("button", { name: "New folder" }).click();
    });
    expect(screen.getByText("Folders · 7")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "New folder" })).toBeTruthy();
    expect(screen.getByText("Nothing in this folder yet.")).toBeTruthy();
  });
});
