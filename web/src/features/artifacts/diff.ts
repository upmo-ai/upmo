import type { ArtifactDiffFile } from "./types";

export type Change = "added" | "deleted" | "modified";

/** What a diff did to a file: a null `before` is an added file, a null `after` a deleted one. */
export function changeOf(file: ArtifactDiffFile): Change {
  if (file.before === null) return "added";
  if (file.after === null) return "deleted";
  return "modified";
}

/** The name of a changed file in a tab or a select. Added and deleted files say so in words. */
export function fileLabel(file: ArtifactDiffFile): string {
  const change = changeOf(file);
  return change === "modified" ? file.path : `${file.path} (${change})`;
}

export function changedFiles(count: number): string {
  return count === 1 ? "1 file changed" : `${count} files changed`;
}
