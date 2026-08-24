import { createMemoryHistory } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
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

  it("lists folders and sources from the sample", async () => {
    renderAt("/knowledge");
    expect(await screen.findByRole("heading", { level: 1, name: "Knowledge" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: sampleFolders[0]!.name })).toBeTruthy();
    expect(screen.getByText(sampleFolders[0]!.sources[0]!.name)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add source" })).toBeTruthy();
    expect(screen.queryByText("Give Upmo something to read")).toBeNull();
  });
});
