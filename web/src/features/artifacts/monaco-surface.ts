/* oxlint-disable import/no-unassigned-import, import/default */
// The Monaco editor, read only, for the artifact viewer. This module is loaded
// on first use (see surface.ts) and is the only place that imports Monaco.
//
// Only the parts a read-only viewer needs are imported: the editor and diff
// editor, copy, find, folding and the read-only notice. There are no language
// services, so nothing in a file is run, completed or followed. Each language
// brings a tokenizer for highlighting and nothing else.
import type { UpmoThemeName } from "@upmo-ai/components";
import { createTokenizationSupport } from "monaco-editor/languages/features/json/tokenization";
import { editor, languages, type Environment } from "monaco-editor/editor/editor.api";
import EditorWorker from "monaco-editor/editor/editor.worker?worker";
import "monaco-editor/features/clipboard/register";
import "monaco-editor/features/codicon/register";
import "monaco-editor/features/diffEditor/register";
import "monaco-editor/features/find/register";
import "monaco-editor/features/folding/register";
import "monaco-editor/features/readOnlyMessage/register";
import "monaco-editor/languages/definitions/cpp/register";
import "monaco-editor/languages/definitions/csharp/register";
import "monaco-editor/languages/definitions/css/register";
import "monaco-editor/languages/definitions/dockerfile/register";
import "monaco-editor/languages/definitions/go/register";
import "monaco-editor/languages/definitions/html/register";
import "monaco-editor/languages/definitions/ini/register";
import "monaco-editor/languages/definitions/java/register";
import "monaco-editor/languages/definitions/javascript/register";
import "monaco-editor/languages/definitions/kotlin/register";
import "monaco-editor/languages/definitions/less/register";
import "monaco-editor/languages/definitions/markdown/register";
import "monaco-editor/languages/definitions/php/register";
import "monaco-editor/languages/definitions/powershell/register";
import "monaco-editor/languages/definitions/python/register";
import "monaco-editor/languages/definitions/ruby/register";
import "monaco-editor/languages/definitions/rust/register";
import "monaco-editor/languages/definitions/scss/register";
import "monaco-editor/languages/definitions/shell/register";
import "monaco-editor/languages/definitions/sql/register";
import "monaco-editor/languages/definitions/swift/register";
import "monaco-editor/languages/definitions/typescript/register";
import "monaco-editor/languages/definitions/xml/register";
import "monaco-editor/languages/definitions/yaml/register";

import { diffOptions, readOnlyOptions } from "./editor-options";
import { pageTokens, themeData } from "./editor-theme";
import type { CodeSurface, CodeSurfaceFile, DiffSurface, DiffSurfaceFile } from "./surface";

let prepared = false;

/** Points Monaco at the worker in this bundle and adds the one language it has no plain tokenizer for. */
function prepare() {
  if (prepared) return;
  prepared = true;
  const environment: Environment = { getWorker: () => new EditorWorker() };
  (globalThis as { MonacoEnvironment?: Environment }).MonacoEnvironment = environment;
  // JSON has a tokenizer apart from its language service, which needs a worker we do not ship.
  languages.register({ id: "json", extensions: [".json", ".jsonc"], aliases: ["JSON"], mimetypes: ["application/json"] });
  languages.setTokensProvider("json", createTokenizationSupport(true));
}

/** Monaco's theme is global, so setting it for one editor sets it for all. */
function applyTheme(theme: UpmoThemeName) {
  const name = `upmo-${theme}`;
  editor.defineTheme(name, themeData(theme, pageTokens()));
  editor.setTheme(name);
}

/** Fira Code loads after the editor may have measured, so measure again when fonts are ready. */
function remeasureWhenFontsLoad() {
  void document.fonts?.ready.then(() => editor.remeasureFonts());
}

function mountCode(host: HTMLElement, input: CodeSurfaceFile & { theme: UpmoThemeName }): CodeSurface {
  prepare();
  applyTheme(input.theme);
  let model = editor.createModel(input.text, input.language);
  const instance = editor.create(host, {
    ...readOnlyOptions(pageTokens()),
    model,
    ariaLabel: input.label,
    lineNumbers: "on",
  });
  remeasureWhenFontsLoad();
  return {
    show(file: CodeSurfaceFile) {
      const next = editor.createModel(file.text, file.language);
      instance.setModel(next);
      instance.updateOptions({ ariaLabel: file.label });
      model.dispose();
      model = next;
    },
    setTheme: applyTheme,
    dispose() {
      instance.dispose();
      model.dispose();
    },
  };
}

function mountDiff(host: HTMLElement, input: { sideBySide: boolean; theme: UpmoThemeName }): DiffSurface {
  prepare();
  applyTheme(input.theme);
  const instance = editor.createDiffEditor(host, diffOptions(pageTokens(), input.sideBySide));
  remeasureWhenFontsLoad();
  let models: editor.ITextModel[] = [];
  return {
    show(file: DiffSurfaceFile) {
      const original = editor.createModel(file.original, file.language);
      const modified = editor.createModel(file.modified, file.language);
      instance.updateOptions({ ariaLabel: file.label });
      instance.setModel({ original, modified });
      for (const model of models) model.dispose();
      models = [original, modified];
    },
    setSideBySide(sideBySide) {
      instance.updateOptions({ renderSideBySide: sideBySide });
    },
    setTheme: applyTheme,
    dispose() {
      // The editor lets go of its models first, then the models go.
      instance.dispose();
      for (const model of models) model.dispose();
      models = [];
    },
  };
}

export { mountCode, mountDiff };
