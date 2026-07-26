import {
  MantineProvider,
  SegmentedControl,
  useComputedColorScheme,
  useMantineColorScheme,
  type CSSVariablesResolver,
  type MantineColorScheme,
} from "@mantine/core";
import { upmoTheme } from "@upmo-ai/ui";
import { useLayoutEffect, type ReactNode } from "react";

// Mantine writes these onto the document as inline variables. A stylesheet
// cannot override that, so the resolver is what makes the page ground follow
// --surface-000.
export const tokenVariables: Record<string, string> = {
  "--mantine-color-body": "var(--surface-000)",
  "--mantine-color-text": "var(--ink)",
  "--mantine-color-dimmed": "var(--ink-muted)",
  "--mantine-color-default": "var(--surface-100)",
  "--mantine-color-default-hover": "var(--surface-hover)",
  "--mantine-color-default-border": "var(--border-strong)",
  "--mantine-color-placeholder": "var(--ink-muted)",
  "--mantine-color-anchor": "var(--emerald-deep)",
  "--mantine-color-error": "var(--danger)",
  "--mantine-primary-color-filled": "var(--emerald-deep)",
  "--mantine-primary-color-filled-hover": "var(--emerald-hover)",
  "--mantine-primary-color-light": "var(--emerald-soft)",
  "--mantine-primary-color-light-color": "var(--emerald-deep)",
  "--mantine-primary-color-contrast": "var(--ink-on-brand)",
};

export const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {},
  light: tokenVariables,
  dark: tokenVariables,
});

const schemes: { label: string; value: MantineColorScheme }[] = [
  { label: "Light", value: "light" },
  { label: "Dark", value: "dark" },
  { label: "System", value: "auto" },
];

function isScheme(value: string): value is MantineColorScheme {
  return value === "light" || value === "dark" || value === "auto";
}

/** Writes the computed light or dark scheme onto the document before paint. */
function ThemeSync() {
  const scheme = useComputedColorScheme("light", { getInitialValueInEffect: false });

  useLayoutEffect(() => {
    document.documentElement.setAttribute("data-theme", scheme);
  }, [scheme]);

  return null;
}

export function ThemeSwitch() {
  const { colorScheme, setColorScheme } = useMantineColorScheme();

  return (
    <SegmentedControl
      data-testid="theme-switch"
      value={colorScheme}
      onChange={(value) => {
        if (isScheme(value)) setColorScheme(value);
      }}
      data={schemes}
    />
  );
}

export function AppProviders({ children }: { children: ReactNode }) {
  return (
    <MantineProvider theme={upmoTheme} cssVariablesResolver={cssVariablesResolver}>
      <ThemeSync />
      {children}
    </MantineProvider>
  );
}
