import { Stack, Text, useMantineTheme } from "@mantine/core";
import { useMediaQuery } from "@mantine/hooks";
import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from "react";
import { Select, Tabs, useUpmoTheme, type UpmoThemeName } from "@upmo-ai/components";

import { changedFiles, fileLabel } from "./diff";
import { languageFor } from "./editor-language";
import { loadSurface, type CodeSurface, type CodeSurfaceFile, type DiffSurface } from "./surface";
import type { ArtifactDiffFile } from "./types";

type Phase = "loading" | "ready" | "failed";

/**
 * The frame an editor is drawn in. The Monaco editor fills `hostRef`, which
 * stays in the page the whole time. While the editor loads there is a status
 * line, and if it never loads there is a message in its place, never a blank frame.
 */
function EditorFrame({
  hostRef,
  phase,
  tall,
}: {
  hostRef: RefObject<HTMLDivElement | null>;
  phase: Phase;
  tall?: boolean;
}) {
  return (
    <>
      {phase === "loading" ? (
        <Text c="dimmed" role="status">
          Loading the viewer
        </Text>
      ) : null}
      {phase === "failed" ? (
        <Text c="dimmed" role="status">
          The viewer could not be loaded. Download the file to read it.
        </Text>
      ) : null}
      <div
        ref={hostRef}
        className="artifact-editor"
        data-phase={phase}
        data-tall={tall ? "true" : undefined}
        hidden={phase === "failed"}
      />
    </>
  );
}

/**
 * Passes a change of a setting (the theme, the layout) on to a mounted editor.
 * The editor is mounted with the setting as it was, so only a later change is
 * passed on. `take` is called when mounting: it returns the latest value.
 */
function useSetting<S, V>(surface: S | null, value: V, apply: (surface: S, value: V) => void) {
  const latest = useRef(value);
  const applied = useRef(value);
  useEffect(() => {
    latest.current = value;
    if (!surface || Object.is(applied.current, value)) return;
    applied.current = value;
    apply(surface, value);
  }, [surface, value, apply]);
  return useCallback(() => {
    applied.current = latest.current;
    return latest.current;
  }, []);
}

const applyCodeTheme = (surface: CodeSurface, theme: UpmoThemeName) => surface.setTheme(theme);
const applyDiffTheme = (surface: DiffSurface, theme: UpmoThemeName) => surface.setTheme(theme);
const applyLayout = (surface: DiffSurface, sideBySide: boolean) => surface.setSideBySide(sideBySide);
const applyCodeFile = (surface: CodeSurface, file: CodeSurfaceFile) => surface.show(file);

/**
 * A file in a read-only editor with syntax highlighting chosen from its name.
 * The editor stays when the file changes: the file in it is swapped.
 */
export function CodeView({ name, text }: { name: string; text: string }) {
  const host = useRef<HTMLDivElement>(null);
  const [surface, setSurface] = useState<CodeSurface | null>(null);
  const [failed, setFailed] = useState(false);
  const language = languageFor(name);
  const file = useMemo(() => ({ label: name, text, language }), [name, text, language]);
  const takeTheme = useSetting(surface, useUpmoTheme(), applyCodeTheme);
  const takeFile = useSetting(surface, file, applyCodeFile);

  useEffect(() => {
    const element = host.current;
    if (!element) return;
    let cancelled = false;
    let mounted: CodeSurface | undefined;
    loadSurface()
      .then((module) => {
        if (cancelled) return;
        mounted = module.mountCode(element, { ...takeFile(), theme: takeTheme() });
        setSurface(mounted);
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
      mounted?.dispose();
      setSurface(null);
    };
  }, [takeFile, takeTheme]);

  return <EditorFrame hostRef={host} phase={failed ? "failed" : surface ? "ready" : "loading"} />;
}

/**
 * The changed files of a diff in a read-only diff editor: two panes on a wide
 * screen and one inline pane on a phone, with a tab (wide) or a select (phone)
 * to pick the file.
 */
export function DiffView({ files }: { files: ArtifactDiffFile[] }) {
  const host = useRef<HTMLDivElement>(null);
  // The md breakpoint, as the shell and chat use. Below it the diff is inline.
  const mantine = useMantineTheme();
  const wide = useMediaQuery(`(min-width: ${mantine.breakpoints.md})`, true, {
    getInitialValueInEffect: false,
  });
  const [index, setIndex] = useState(0);
  const [surface, setSurface] = useState<DiffSurface | null>(null);
  const [failed, setFailed] = useState(false);
  // The files can change under the reader (a refetch), so the pick stays inside them.
  const selected = Math.min(index, Math.max(files.length - 1, 0));
  const current = files[selected];

  const takeTheme = useSetting(surface, useUpmoTheme(), applyDiffTheme);
  const takeLayout = useSetting(surface, wide, applyLayout);

  useEffect(() => {
    const element = host.current;
    if (!element) return;
    let cancelled = false;
    let mounted: DiffSurface | undefined;
    loadSurface()
      .then((module) => {
        if (cancelled) return;
        mounted = module.mountDiff(element, { sideBySide: takeLayout(), theme: takeTheme() });
        setSurface(mounted);
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
      mounted?.dispose();
      setSurface(null);
    };
  }, [takeLayout, takeTheme]);

  useEffect(() => {
    if (!surface || !current) return;
    surface.show({
      label: `${current.path}, before and after`,
      language: languageFor(current.path),
      original: current.before ?? "",
      modified: current.after ?? "",
    });
  }, [surface, current]);

  return (
    <Stack gap="xs">
      <Text size="sm" c="dimmed">
        {changedFiles(files.length)}
      </Text>
      {wide ? (
        <Tabs value={String(selected)} onChange={(value) => value !== null && setIndex(Number(value))}>
          <Tabs.List aria-label="Changed files" className="artifact-diff-tabs">
            {files.map((file, position) => (
              <Tabs.Tab key={position} value={String(position)}>
                {fileLabel(file)}
              </Tabs.Tab>
            ))}
          </Tabs.List>
        </Tabs>
      ) : (
        <Select
          label="Changed file"
          allowDeselect={false}
          value={String(selected)}
          data={files.map((file, position) => ({ value: String(position), label: fileLabel(file) }))}
          onChange={(value) => {
            if (value !== null) setIndex(Number(value));
          }}
        />
      )}
      <EditorFrame hostRef={host} phase={failed ? "failed" : surface ? "ready" : "loading"} tall />
    </Stack>
  );
}
