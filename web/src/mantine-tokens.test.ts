import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import type { MantineTheme } from "@mantine/core";

import { cssVariablesResolver, tokenVariables } from "./theme";

const src = "src";

const expected: Record<string, string> = {
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

function rules(sheet: string): { selectors: string[]; declarations: Record<string, string> }[] {
  const found: { selectors: string[]; declarations: Record<string, string> }[] = [];
  for (const match of sheet.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selectors = match[1]
      .split(",")
      .map((selector) => selector.trim())
      .filter(Boolean);
    const declarations: Record<string, string> = {};
    for (const raw of match[2].split(";")) {
      const line = raw.trim();
      if (!line) continue;
      const colon = line.indexOf(":");
      declarations[line.slice(0, colon).trim()] = line.slice(colon + 1).trim();
    }
    found.push({ selectors, declarations });
  }
  return found;
}

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...sourceFiles(path));
    else if (/\.(ts|tsx|css)$/.test(entry.name)) out.push(path);
  }
  return out;
}

describe("cssVariablesResolver", () => {
  it("maps every Mantine variable to the named token in both schemes", () => {
    const resolved = cssVariablesResolver({} as MantineTheme);
    expect(resolved.light).toEqual(tokenVariables);
    expect(resolved.dark).toEqual(tokenVariables);
    expect(resolved.variables).toEqual({});
    expect(tokenVariables).toEqual(expected);
  });
});

describe("mantine-tokens.css", () => {
  it("maps every variable to the named token in both schemes", () => {
    const parsed = rules(readFileSync(join(src, "mantine-tokens.css"), "utf8"));
    const light = parsed.find((rule) => rule.selectors.includes(":root"));
    const dark = parsed.find((rule) => rule.selectors.includes('[data-mantine-color-scheme="dark"]'));

    expect(light?.selectors).toEqual([":root", '[data-mantine-color-scheme="light"]']);
    expect(dark?.selectors).toEqual(['[data-mantine-color-scheme="dark"]']);
    expect(light?.declarations).toEqual(expected);
    expect(dark?.declarations).toEqual(expected);
    expect(parsed).toHaveLength(2);
  });
});

describe("styles", () => {
  it("imports mantine, tokens, and the override file in that order, plus Manrope", () => {
    const main = readFileSync(join(src, "main.tsx"), "utf8");
    const mantine = main.indexOf("@mantine/core/styles.css");
    const tokens = main.indexOf("@upmo-ai/tokens/tokens.css");
    const overrides = main.indexOf("./mantine-tokens.css");

    expect(mantine).toBeGreaterThanOrEqual(0);
    expect(tokens).toBeGreaterThan(mantine);
    expect(overrides).toBeGreaterThan(tokens);
    for (const weight of [400, 500, 600, 700]) {
      expect(main).toContain(`@fontsource/manrope/${weight}.css`);
    }
    expect(main).not.toContain("@fontsource-variable/manrope");
  });

  it("passes upmoTheme to MantineProvider", () => {
    const theme = readFileSync(join(src, "theme.tsx"), "utf8");
    expect(theme).toMatch(/import\s*\{[^}]*\bupmoTheme\b[^}]*\}\s*from\s*"@upmo-ai\/ui"/);
    expect(theme).toContain("theme={upmoTheme}");
  });

  it("has no colour literals under web/src", () => {
    const colour = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(/;
    for (const path of sourceFiles(src)) {
      const hit = readFileSync(path, "utf8").match(colour);
      expect(hit ? `${path} ${hit[0]}` : "").toBe("");
    }
  });
});
