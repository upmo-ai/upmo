import { useMantineColorScheme, useMantineTheme } from "@mantine/core";
import { upmoTheme } from "@upmo-ai/theme";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { App } from "./App";
import { AppProviders, themeStorageKey } from "./theme";

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

function choose(name: "Light" | "Dark" | "High contrast" | "System") {
  const radio = within(screen.getByTestId("theme-switch")).getByRole("radio", { name });
  act(() => {
    radio.click();
  });
}

function prefer(...features: string[]) {
  window.matchMedia = (query: string): MediaQueryList =>
    ({
      matches: features.some((feature) => query.includes(feature)),
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

  it("renders the home heading and the four theme choices", async () => {
    renderApp();
    expect(await screen.findByRole("heading", { name: "Home" })).toBeTruthy();
    const themeSwitch = screen.getByTestId("theme-switch");
    expect(within(themeSwitch).getAllByRole("radio").map((radio) => radio.getAttribute("value"))).toEqual([
      "light",
      "dark",
      "hc",
      "system",
    ]);
    for (const name of ["Light", "Dark", "High contrast", "System"]) {
      expect(within(themeSwitch).getByText(name)).toBeTruthy();
    }
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

  it("sets the dark theme and stores the choice", async () => {
    renderApp();
    await screen.findByTestId("theme-switch");
    choose("Dark");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(screen.getByTestId("color-scheme").textContent).toBe("dark");
    expect(localStorage.getItem(themeStorageKey)).toBe("dark");
    expect(Object.keys(localStorage)).toEqual([themeStorageKey]);
  });

  it("sets the light theme", async () => {
    renderApp();
    await screen.findByTestId("theme-switch");
    choose("Dark");
    choose("Light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(screen.getByTestId("color-scheme").textContent).toBe("light");
  });

  it("sets the high contrast theme on a light Mantine scheme", async () => {
    renderApp();
    await screen.findByTestId("theme-switch");
    choose("High contrast");
    expect(document.documentElement.getAttribute("data-theme")).toBe("hc");
    expect(screen.getByTestId("color-scheme").textContent).toBe("light");
    expect(localStorage.getItem(themeStorageKey)).toBe("hc");
  });

  it("follows the OS when System is chosen", async () => {
    prefer("prefers-color-scheme: dark");
    renderApp();
    await screen.findByTestId("theme-switch");
    choose("Dark");
    choose("System");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(localStorage.getItem(themeStorageKey)).toBe("system");
    expect(Object.keys(localStorage)).toEqual([themeStorageKey]);
  });

  it("picks high contrast for System when the OS asks for more contrast", async () => {
    prefer("prefers-color-scheme: dark", "prefers-contrast: more");
    renderApp();
    await screen.findByTestId("theme-switch");
    expect(document.documentElement.getAttribute("data-theme")).toBe("hc");
  });

  it("restores a stored choice", async () => {
    localStorage.setItem(themeStorageKey, "hc");
    renderApp();
    const themeSwitch = await screen.findByTestId("theme-switch");
    expect(document.documentElement.getAttribute("data-theme")).toBe("hc");
    expect((within(themeSwitch).getByRole("radio", { name: "High contrast" }) as HTMLInputElement).checked).toBe(true);
  });

  it("moves the old stored auto choice to System", async () => {
    prefer("prefers-color-scheme: dark");
    localStorage.setItem("mantine-color-scheme-value", "auto");
    renderApp();
    const themeSwitch = await screen.findByTestId("theme-switch");
    expect((within(themeSwitch).getByRole("radio", { name: "System" }) as HTMLInputElement).checked).toBe(true);
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(localStorage.getItem(themeStorageKey)).toBe("system");
    expect(Object.keys(localStorage)).toEqual([themeStorageKey]);
  });

  it("keeps an old stored dark choice", async () => {
    localStorage.setItem("mantine-color-scheme-value", "dark");
    renderApp();
    await screen.findByTestId("theme-switch");
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(localStorage.getItem(themeStorageKey)).toBe("dark");
  });
});
