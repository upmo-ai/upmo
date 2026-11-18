/**
 * How the server classifies an artifact. It decides which viewer opens it.
 * "binary" is every file with no viewer. SVG is never "image": the server
 * sends it as an attachment, so it is "code" (or "binary"). "diff" is a set of
 * changed files, read from GET /api/artifacts/{id}/diff.
 */
export const artifactKinds = [
  "image",
  "markdown",
  "csv",
  "text",
  "code",
  "pdf",
  "html",
  "diff",
  "binary",
] as const;

export type ArtifactKind = (typeof artifactKinds)[number];

/** One file a task produced. GET /api/tasks/{id}/artifacts answers a list of these. */
export interface ArtifactRecord {
  id: string;
  name: string;
  kind: ArtifactKind;
  mime: string;
  /** Size in bytes. */
  size: number;
  /** ISO 8601 time the artifact was saved. */
  created_at: string;
}

/**
 * One changed file of a diff artifact. A null `before` is a file the change
 * added, a null `after` is a file it deleted.
 */
export interface ArtifactDiffFile {
  path: string;
  before: string | null;
  after: string | null;
}

/** The body of GET /api/artifacts/{id}/diff. An empty list is a diff with no changes. */
export interface ArtifactDiff {
  files: ArtifactDiffFile[];
}

export interface ArtifactsApi {
  /** GET /api/tasks/{id}/artifacts, oldest first. An empty list when the task made none. */
  list(taskId: string): Promise<ArtifactRecord[]>;
  /**
   * GET /api/artifacts/{id}/text, read as UTF-8 text. Rejects when the file
   * is missing (404) or cannot be read.
   */
  text(id: string): Promise<string>;
  /**
   * GET /api/artifacts/{id}/diff, for an artifact of kind "diff". Rejects when
   * the stored diff is missing (404) or cannot be read.
   */
  diff(id: string): Promise<ArtifactDiff>;
  /**
   * GET /api/artifacts/{id}/download. The server sends it with nosniff, inline
   * for images and PDFs and as an attachment for the rest. Safe as an img src
   * and as the href of a download link.
   */
  downloadUrl(id: string): string;
}

const kindLabels: Record<ArtifactKind, string> = {
  image: "Image",
  markdown: "Markdown",
  csv: "CSV",
  text: "Text",
  code: "Code",
  pdf: "PDF",
  html: "HTML",
  diff: "Diff",
  binary: "File",
};

/** An unknown kind from a newer server reads as a plain file. */
export function kindLabel(kind: string): string {
  return kindLabels[kind as ArtifactKind] ?? kindLabels.binary;
}

/** The most text a viewer reads. A bigger file is offered as a download. */
export const MAX_VIEW_BYTES = 256 * 1024;

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const kb = bytes / 1024;
  if (kb < 1024) return `${kb < 10 ? kb.toFixed(1) : Math.round(kb)} KB`;
  const mb = kb / 1024;
  return `${mb < 10 ? mb.toFixed(1) : Math.round(mb)} MB`;
}

export function artifactCount(count: number) {
  return count === 1 ? "1 artifact" : `${count} artifacts`;
}
