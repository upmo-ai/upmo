import type { UpmoThemeName } from "@upmo-ai/components";

/**
 * The boundary to the Monaco editor. Everything that touches Monaco sits behind
 * it, in monaco-surface.ts, which is loaded on first use so the editor adds
 * nothing to the first bundle. The views in code-view.tsx use only these types,
 * and tests replace the module.
 */

/** One file in a read-only editor. */
export interface CodeSurfaceFile {
  /** Names the editor for assistive technology. */
  label: string;
  text: string;
  language: string;
}

/** A read-only editor, shown over one file at a time. */
export interface CodeSurface {
  /** Swaps the file in the editor that is already there. */
  show(file: CodeSurfaceFile): void;
  setTheme(theme: UpmoThemeName): void;
  dispose(): void;
}

/** One side-by-side or inline comparison of a file before and after. */
export interface DiffSurfaceFile {
  /** Names the editor for assistive technology. */
  label: string;
  language: string;
  /** The text before the change. Empty for an added file. */
  original: string;
  /** The text after the change. Empty for a deleted file. */
  modified: string;
}

/** A read-only diff editor, shown for one file at a time. */
export interface DiffSurface {
  show(file: DiffSurfaceFile): void;
  /** Two panes when true, one inline pane when false. */
  setSideBySide(sideBySide: boolean): void;
  setTheme(theme: UpmoThemeName): void;
  dispose(): void;
}

export interface SurfaceModule {
  mountCode(host: HTMLElement, input: CodeSurfaceFile & { theme: UpmoThemeName }): CodeSurface;
  mountDiff(host: HTMLElement, input: { sideBySide: boolean; theme: UpmoThemeName }): DiffSurface;
}

/** Loads the editor. The editor and its worker come from this app's own bundle, never from a CDN. */
export function loadSurface(): Promise<SurfaceModule> {
  return import("./monaco-surface");
}
