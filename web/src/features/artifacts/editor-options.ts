import type { editor } from "monaco-editor/editor/editor.api";

import type { TokenReader } from "./editor-theme";

function typography(read: TokenReader): editor.IEditorOptions {
  const fontSize = Number.parseFloat(read("--text-code-size"));
  const lineHeight = Number.parseFloat(read("--text-code-line"));
  const fontFamily = read("--font-mono");
  return {
    ...(fontFamily ? { fontFamily } : {}),
    ...(Number.isFinite(fontSize) ? { fontSize } : {}),
    ...(Number.isFinite(lineHeight) ? { lineHeight } : {}),
    fontLigatures: false,
  };
}

/**
 * The options both editors share. Nothing here lets a reader change the file,
 * follow a link or run anything in it.
 */
export function readOnlyOptions(read: TokenReader): editor.IEditorOptions {
  return {
    ...typography(read),
    readOnly: true,
    // The text input is read only in the page too, so a phone shows no keyboard.
    domReadOnly: true,
    // The plain text input path: it is the one that carries the read-only mark.
    editContext: false,
    // Tab moves on to the next control, so the editor is never a keyboard trap.
    tabFocusMode: true,
    automaticLayout: true,
    minimap: { enabled: false },
    scrollBeyondLastLine: false,
    // The page keeps scrolling when the pointer is over the editor.
    scrollbar: { alwaysConsumeMouseWheel: false },
    renderLineHighlight: "none",
    glyphMargin: false,
    lineNumbersMinChars: 3,
    padding: { top: 8, bottom: 8 },
    links: false,
    contextmenu: false,
    smoothScrolling: false,
    cursorSmoothCaretAnimation: "off",
    wordWrap: "off",
    folding: true,
  };
}

/** The name of the diff editor until a file is shown, which names it after the file. */
export const diffAriaLabel = "Changed file, before and after";

/** The options of the diff editor. `sideBySide` is false for the inline view of a phone. */
export function diffOptions(read: TokenReader, sideBySide: boolean): editor.IDiffEditorConstructionOptions {
  return {
    ...readOnlyOptions(read),
    ariaLabel: diffAriaLabel,
    originalEditable: false,
    renderSideBySide: sideBySide,
    // The reader's screen decides, not Monaco's guess from the width of the box.
    useInlineViewWhenSpaceIsLimited: false,
    enableSplitViewResizing: false,
    renderOverviewRuler: false,
    // +/- marks next to the lines, so the change is not told by colour alone.
    renderIndicators: true,
    ignoreTrimWhitespace: false,
    hideUnchangedRegions: { enabled: false },
  };
}
