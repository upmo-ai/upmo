/**
 * The editor languages the artifact viewer highlights. monaco-surface.ts
 * registers a tokenizer for each one, and a test keeps the two in step.
 */
export const editorLanguages = [
  "python",
  "javascript",
  "typescript",
  "json",
  "yaml",
  "go",
  "rust",
  "sql",
  "css",
  "scss",
  "less",
  "html",
  "xml",
  "cpp",
  "shell",
  "markdown",
  "java",
  "ruby",
  "php",
  "csharp",
  "kotlin",
  "swift",
  "ini",
  "powershell",
  "dockerfile",
] as const;

export type EditorLanguage = (typeof editorLanguages)[number];

/** Shown when a file name says nothing about its language. */
export const PLAIN_TEXT = "plaintext";

const byExtension: Record<string, EditorLanguage> = {
  py: "python",
  pyw: "python",
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  jsx: "javascript",
  ts: "typescript",
  tsx: "typescript",
  mts: "typescript",
  cts: "typescript",
  json: "json",
  jsonc: "json",
  yml: "yaml",
  yaml: "yaml",
  go: "go",
  rs: "rust",
  sql: "sql",
  css: "css",
  scss: "scss",
  less: "less",
  html: "html",
  htm: "html",
  xml: "xml",
  svg: "xml",
  c: "cpp",
  h: "cpp",
  cc: "cpp",
  cpp: "cpp",
  cxx: "cpp",
  hpp: "cpp",
  sh: "shell",
  bash: "shell",
  zsh: "shell",
  md: "markdown",
  markdown: "markdown",
  java: "java",
  rb: "ruby",
  php: "php",
  cs: "csharp",
  kt: "kotlin",
  swift: "swift",
  ini: "ini",
  cfg: "ini",
  conf: "ini",
  ps1: "powershell",
};

const byName: Record<string, EditorLanguage> = {
  dockerfile: "dockerfile",
};

/**
 * The highlighting language for a file or path, chosen from its name alone.
 * Nothing in the file is read to decide. An unknown name shows as plain text.
 */
export function languageFor(name: string): string {
  const base = name.slice(name.lastIndexOf("/") + 1).toLowerCase();
  if (Object.hasOwn(byName, base)) return byName[base] ?? PLAIN_TEXT;
  const dot = base.lastIndexOf(".");
  if (dot < 0) return PLAIN_TEXT;
  const extension = base.slice(dot + 1);
  return Object.hasOwn(byExtension, extension) ? (byExtension[extension] ?? PLAIN_TEXT) : PLAIN_TEXT;
}
