// A stand-in for monaco-surface.ts in tests. jsdom cannot run Monaco, so the
// tests replace that module with this one: it writes the text it is given into
// the page and records what the views asked of it. The real editor is covered
// by e2e/artifacts.mjs.
import type { UpmoThemeName } from "@upmo-ai/components";

import type { CodeSurface, CodeSurfaceFile, DiffSurface, DiffSurfaceFile } from "./surface";

export interface FakeCode {
  /** The file now in the editor: the one at mount, then each one shown. */
  label: string;
  text: string;
  language: string;
  /** Every file in the editor, in order, the first being the one at mount. */
  files: CodeSurfaceFile[];
  /** The theme at mount, then each change. */
  themes: UpmoThemeName[];
  disposed: boolean;
}

export interface FakeDiff {
  /** The layout at mount, then each change. */
  sideBySide: boolean[];
  themes: UpmoThemeName[];
  /** Every file shown, in order. */
  shown: DiffSurfaceFile[];
  disposed: boolean;
}

export const mounted = { code: [] as FakeCode[], diff: [] as FakeDiff[] };

/** Set `failMount` to make the editor fail to start, as a chunk that does not load would. */
export const behavior = { failMount: false };

export function resetFake() {
  mounted.code = [];
  mounted.diff = [];
  behavior.failMount = false;
}

export function mountCode(host: HTMLElement, input: CodeSurfaceFile & { theme: UpmoThemeName }): CodeSurface {
  if (behavior.failMount) throw new Error("The editor did not start.");
  const record: FakeCode = {
    label: input.label,
    text: input.text,
    language: input.language,
    files: [{ label: input.label, text: input.text, language: input.language }],
    themes: [input.theme],
    disposed: false,
  };
  mounted.code.push(record);
  const view = document.createElement("pre");
  view.setAttribute("data-fake-editor", "code");
  view.textContent = input.text;
  host.append(view);
  return {
    show(file) {
      record.label = file.label;
      record.text = file.text;
      record.language = file.language;
      record.files.push({ ...file });
      view.textContent = file.text;
    },
    setTheme(theme) {
      record.themes.push(theme);
    },
    dispose() {
      record.disposed = true;
      view.remove();
    },
  };
}

export function mountDiff(host: HTMLElement, input: { sideBySide: boolean; theme: UpmoThemeName }): DiffSurface {
  if (behavior.failMount) throw new Error("The editor did not start.");
  const record: FakeDiff = { sideBySide: [input.sideBySide], themes: [input.theme], shown: [], disposed: false };
  mounted.diff.push(record);
  const view = document.createElement("div");
  view.setAttribute("data-fake-editor", "diff");
  host.append(view);
  return {
    show(file) {
      record.shown.push(file);
      view.textContent = `${file.original}\n${file.modified}`;
    },
    setSideBySide(sideBySide) {
      record.sideBySide.push(sideBySide);
    },
    setTheme(theme) {
      record.themes.push(theme);
    },
    dispose() {
      record.disposed = true;
      view.remove();
    },
  };
}
