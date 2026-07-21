import { MantineProvider } from "@mantine/core";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { App } from "./App";
import { theme } from "./theme";

describe("App", () => {
  it("renders the placeholder heading", () => {
    render(
      <MantineProvider theme={theme}>
        <App />
      </MantineProvider>,
    );
    expect(screen.getByRole("heading", { name: "Upmo" })).toBeTruthy();
    expect(screen.getByText("Always up.")).toBeTruthy();
  });
});
