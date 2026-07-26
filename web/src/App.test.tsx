import { useMantineColorScheme, useMantineTheme } from "@mantine/core";
import { upmoTheme } from "@upmo-ai/ui";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "./App";
import { AppProviders } from "./theme";

function SchemeValue() {
  const { colorScheme } = useMantineColorScheme();
  return <span data-testid="color-scheme">{colorScheme}</span>;
}

function renderApp() {
  render(
    <AppProviders>
      <SchemeValue />
      <App />
    </AppProviders>,
  );
}

function choose(name: "Light" | "Dark" | "System") {
  const radio = within(screen.getByTestId("theme-switch")).getByRole("radio", { name });
  act(() => {
    radio.click();
  });
}

function preferDark() {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: query.includes("prefers-color-scheme: dark"),
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}

describe("App", () => {
  let matchMedia: typeof window.matchMedia;

  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
    document.documentElement.removeAttribute("data-mantine-color-scheme");
    matchMedia = window.matchMedia;
  });

  afterEach(() => {
    cleanup();
    window.matchMedia = matchMedia;
  });

  it("renders the placeholder heading", () => {
    renderApp();
    expect(screen.getByRole("heading", { name: "Upmo" })).toBeTruthy();
    expect(screen.getByText("Always up.")).toBeTruthy();
    expect(screen.getByText("Web app placeholder")).toBeTruthy();
    expect(screen.getByTestId("theme-switch")).toBeTruthy();
    expect(screen.getByText("Light")).toBeTruthy();
    expect(screen.getByText("Dark")).toBeTruthy();
    expect(screen.getByText("System")).toBeTruthy();
  });

  it("provides upmoTheme", () => {
    function Probe() {
      const theme = useMantineTheme();
      return (
        <pre data-testid="theme-probe">
          {JSON.stringify({
            primaryColor: theme.primaryColor,
            fontFamily: theme.fontFamily,
            fontFamilyMonospace: theme.fontFamilyMonospace,
            primaryShade: theme.primaryShade,
            defaultRadius: theme.defaultRadius,
            white: theme.white,
            black: theme.black,
            emerald: theme.colors.emerald,
          })}
        </pre>
      );
    }
    render(
      <AppProviders>
        <Probe />
      </AppProviders>,
    );
    expect(JSON.parse(screen.getByTestId("theme-probe").textContent ?? "")).toEqual({
      primaryColor: upmoTheme.primaryColor,
      fontFamily: upmoTheme.fontFamily,
      fontFamilyMonospace: upmoTheme.fontFamilyMonospace,
      primaryShade: upmoTheme.primaryShade,
      defaultRadius: upmoTheme.defaultRadius,
      white: upmoTheme.white,
      black: upmoTheme.black,
      emerald: upmoTheme.colors?.emerald,
    });
  });

  it("sets the dark scheme", () => {
    renderApp();
    choose("Dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(screen.getByTestId("color-scheme").textContent).toBe("dark");
    expect(localStorage.getItem("mantine-color-scheme-value")).toBe("dark");
    expect(Object.keys(localStorage)).toEqual(["mantine-color-scheme-value"]);
  });

  it("sets the light scheme", () => {
    renderApp();
    choose("Dark");
    choose("Light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(screen.getByTestId("color-scheme").textContent).toBe("light");
  });

  it("follows the computed scheme when System is chosen", () => {
    preferDark();
    renderApp();
    choose("System");
    expect(screen.getByTestId("color-scheme").textContent).toBe("auto");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(localStorage.getItem("mantine-color-scheme-value")).toBe("auto");
    expect(Object.keys(localStorage)).toEqual(["mantine-color-scheme-value"]);
  });
});
