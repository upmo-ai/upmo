import type { UpmoThemeName } from "@upmo-ai/components";
import type { editor } from "monaco-editor/editor/editor.api";

/** Reads a design token such as "--ink" as it is on the page now. An empty string when it is not set. */
export type TokenReader = (token: string) => string;

/** Reads tokens from the document root, where the app theme puts them. */
export function pageTokens(): TokenReader {
  const style = getComputedStyle(document.documentElement);
  return (token) => style.getPropertyValue(token).trim();
}

const hex = /^#([0-9a-f]{6})$/i;
const shortHex = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i;
const rgb = /^rgba?\(\s*(\d+)[\s,]+(\d+)[\s,]+(\d+)/;
const alias = /^var\(\s*(--[\w-]+)\s*\)$/;

/**
 * A token's colour as #rrggbb. Monaco takes hex colours, not var(), so the
 * editor is given the value the page's theme has for the token. A token that
 * is not a plain colour gives undefined and Monaco keeps its own default.
 */
export function tokenColor(read: TokenReader, token: string, depth = 0): string | undefined {
  const value = read(token);
  const linked = alias.exec(value);
  if (linked?.[1]) return depth < 4 ? tokenColor(read, linked[1], depth + 1) : undefined;
  if (hex.test(value)) return value.toLowerCase();
  const short = shortHex.exec(value);
  if (short) return `#${short[1]}${short[1]}${short[2]}${short[2]}${short[3]}${short[3]}`.toLowerCase();
  const channels = rgb.exec(value);
  if (!channels) return undefined;
  return `#${[channels[1], channels[2], channels[3]]
    .map((channel) => Math.min(255, Number(channel)).toString(16).padStart(2, "0"))
    .join("")}`;
}

/** A UI colour: Monaco colour id, design token, optional alpha as two hex digits. */
export type ColorSpec = readonly [id: string, token: string, alpha?: string];

export const uiColors: readonly ColorSpec[] = [
  ["editor.background", "--surface-200"],
  ["editor.foreground", "--ink"],
  ["editorGutter.background", "--surface-200"],
  ["editorLineNumber.foreground", "--ink-muted"],
  ["editorLineNumber.activeForeground", "--ink"],
  ["editorCursor.foreground", "--ink"],
  ["editor.selectionBackground", "--surface-selected"],
  ["editor.inactiveSelectionBackground", "--surface-selected"],
  ["editor.selectionHighlightBackground", "--surface-hover"],
  ["editor.lineHighlightBackground", "--surface-hover", "80"],
  ["editor.findMatchBackground", "--warning-soft"],
  ["editor.findMatchHighlightBackground", "--warning-soft", "99"],
  ["editorIndentGuide.background1", "--border"],
  ["editorIndentGuide.activeBackground1", "--border-strong"],
  ["editorWhitespace.foreground", "--border"],
  ["editorWidget.background", "--surface-100"],
  ["editorWidget.foreground", "--ink"],
  ["editorWidget.border", "--border-strong"],
  ["editorHoverWidget.background", "--surface-100"],
  ["input.background", "--surface-100"],
  ["input.foreground", "--ink"],
  ["input.border", "--border-strong"],
  ["inputOption.activeBorder", "--focus"],
  ["inputOption.activeBackground", "--surface-selected"],
  ["focusBorder", "--focus"],
  ["contrastBorder", "--border-strong"],
  ["contrastActiveBorder", "--focus"],
  ["scrollbarSlider.background", "--mist", "99"],
  ["scrollbarSlider.hoverBackground", "--caret-idle", "99"],
  ["scrollbarSlider.activeBackground", "--caret-idle"],
  ["foldingBackground", "--surface-hover", "40"],
  ["diffEditor.insertedLineBackground", "--success-soft"],
  ["diffEditor.removedLineBackground", "--danger-soft"],
  ["diffEditor.insertedTextBackground", "--success", "40"],
  ["diffEditor.removedTextBackground", "--danger", "40"],
  ["diffEditorGutter.insertedLineBackground", "--success-soft"],
  ["diffEditorGutter.removedLineBackground", "--danger-soft"],
  ["diffEditor.diagonalFill", "--border"],
  ["diffEditor.border", "--border"],
];

/** A syntax colour: Monarch token, design token, optional font style. */
export type SyntaxSpec = readonly [token: string, color: string, fontStyle?: string];

// The same token to colour choices as the design system's code block.
export const syntaxColors: readonly SyntaxSpec[] = [
  ["", "--ink"],
  ["comment", "--ink-muted", "italic"],
  ["delimiter", "--ink-secondary"],
  ["string", "--chart-1"],
  ["string.key", "--chart-4"],
  ["attribute.value", "--chart-1"],
  ["number", "--chart-6"],
  ["constant", "--chart-6"],
  ["keyword", "--chart-2"],
  ["type", "--chart-5"],
  ["predefined", "--chart-5"],
  ["function", "--chart-5"],
  ["tag", "--chart-4"],
  ["metatag", "--chart-4"],
  ["attribute.name", "--chart-3"],
  ["regexp", "--chart-3"],
  ["operator", "--chart-3"],
  ["variable", "--chart-3"],
  ["annotation", "--chart-3"],
];

/** Monaco's closest base for each app theme. High contrast in Upmo is a light scheme. */
export const baseOf: Record<UpmoThemeName, editor.BuiltinTheme> = {
  light: "vs",
  dark: "vs-dark",
  hc: "hc-light",
};

/**
 * The editor theme for an app theme. Every colour is read from a design token,
 * so light, dark and high contrast follow the tokens and nothing is hard-coded.
 */
export function themeData(theme: UpmoThemeName, read: TokenReader): editor.IStandaloneThemeData {
  const colors: Record<string, string> = {};
  for (const [id, token, alpha] of uiColors) {
    const color = tokenColor(read, token);
    if (color) colors[id] = alpha ? `${color}${alpha}` : color;
  }
  const rules: editor.ITokenThemeRule[] = [];
  for (const [token, color, fontStyle] of syntaxColors) {
    const foreground = tokenColor(read, color)?.slice(1);
    if (foreground) rules.push({ token, foreground, ...(fontStyle ? { fontStyle } : {}) });
  }
  return { base: baseOf[theme], inherit: true, rules, colors };
}
