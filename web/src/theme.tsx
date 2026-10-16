import { SegmentedControl, UpmoProvider, type UpmoThemeName } from "@upmo-ai/components";
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

export type ThemeChoice = UpmoThemeName | "system";

export const themeStorageKey = "upmo-theme";
// Before the design system provider, Mantine stored the choice under this key,
// with "auto" for System.
const legacyStorageKey = "mantine-color-scheme-value";

const choices: { label: string; value: ThemeChoice }[] = [
  { label: "Light", value: "light" },
  { label: "Dark", value: "dark" },
  { label: "High contrast", value: "hc" },
  { label: "System", value: "system" },
];

function isChoice(value: string | null): value is ThemeChoice {
  return value === "light" || value === "dark" || value === "hc" || value === "system";
}

function readChoice(): ThemeChoice {
  try {
    const stored = localStorage.getItem(themeStorageKey);
    if (isChoice(stored)) return stored;
    const legacy = localStorage.getItem(legacyStorageKey);
    if (legacy === null) return "system";
    localStorage.removeItem(legacyStorageKey);
    const migrated = legacy === "auto" ? "system" : legacy;
    if (!isChoice(migrated)) return "system";
    localStorage.setItem(themeStorageKey, migrated);
    return migrated;
  } catch {
    return "system";
  }
}

function storeChoice(choice: ThemeChoice) {
  try {
    localStorage.setItem(themeStorageKey, choice);
  } catch {
    // Storage is blocked: the choice still applies for this page.
  }
}

const ThemeChoiceContext = createContext<{
  choice: ThemeChoice;
  setChoice: (choice: ThemeChoice) => void;
} | null>(null);

export function useThemeChoice() {
  const value = useContext(ThemeChoiceContext);
  if (!value) throw new Error("useThemeChoice needs AppProviders");
  return value;
}

export function ThemeSwitch() {
  const { choice, setChoice } = useThemeChoice();

  return (
    <SegmentedControl
      data-testid="theme-switch"
      value={choice}
      onChange={(value) => {
        if (isChoice(value)) setChoice(value);
      }}
      data={choices}
    />
  );
}

export function AppProviders({ children }: { children: ReactNode }) {
  const [choice, setChoiceState] = useState<ThemeChoice>(readChoice);
  const setChoice = useCallback((next: ThemeChoice) => {
    setChoiceState(next);
    storeChoice(next);
  }, []);
  const value = useMemo(() => ({ choice, setChoice }), [choice, setChoice]);

  return (
    <ThemeChoiceContext value={value}>
      <UpmoProvider theme={choice}>{children}</UpmoProvider>
    </ThemeChoiceContext>
  );
}
