export type SourceKind = "markdown" | "file" | "url" | "clip";
export type SourceProvenance = "curated" | "web";
export type SourceStatus = "pending" | "ready" | "failed";

/**
 * A folder of sources. The server sends id and name today. source_count and
 * updated_at are the two fields the folder cards need on top of that.
 */
export interface KnowledgeFolder {
  id: string;
  name: string;
  source_count: number;
  updated_at: string;
}

export interface KnowledgeSource {
  id: string;
  title: string;
  kind: SourceKind;
  source_ref: string;
  provenance: SourceProvenance;
  status: SourceStatus;
  revision: number;
  updated_at: string;
  /** Short reason, present only while status is "failed". */
  error?: string;
}

/** One verbatim chunk of a source that matched a search. */
export interface KnowledgeHit {
  source_id: string;
  seq: number;
  body: string;
}

export interface MarkdownInput {
  title: string;
  body: string;
}

export interface UrlInput {
  url: string;
  title?: string;
}

export interface FileInput {
  file: File;
  title?: string;
}

export interface KnowledgeApi {
  folders: {
    list(): Promise<KnowledgeFolder[]>;
    create(name: string): Promise<KnowledgeFolder>;
    rename(id: string, name: string): Promise<KnowledgeFolder>;
    remove(id: string): Promise<void>;
  };
  sources: {
    list(folderId: string): Promise<KnowledgeSource[]>;
    addMarkdown(folderId: string, input: MarkdownInput): Promise<KnowledgeSource>;
    addUrl(folderId: string, input: UrlInput): Promise<KnowledgeSource>;
    addFile(folderId: string, input: FileInput): Promise<KnowledgeSource>;
    reingest(folderId: string, sourceId: string): Promise<KnowledgeSource>;
    remove(folderId: string, sourceId: string): Promise<void>;
  };
  search(folderId: string, query: string): Promise<KnowledgeHit[]>;
}

export function sourceCount(count: number) {
  return count === 1 ? "1 source" : `${count} sources`;
}

/** The server refuses uploads above this size. */
export const MAX_FILE_BYTES = 32 * 1024 * 1024;

/** Extensions the server converts. Anything else is refused at upload. */
export const FILE_EXTENSIONS = [".pdf", ".docx", ".md", ".txt", ".html", ".htm"] as const;

export function fileExtension(name: string): string {
  const dot = name.lastIndexOf(".");
  return dot < 0 ? "" : name.slice(dot).toLowerCase();
}

/** Why a file cannot be uploaded, or null when it can. */
export function fileProblem(file: { name: string; size: number }): string | null {
  if (!(FILE_EXTENSIONS as readonly string[]).includes(fileExtension(file.name))) {
    return "That file type is not supported. Use PDF, Word, Markdown, text or HTML.";
  }
  if (file.size === 0) return "That file is empty.";
  if (file.size > MAX_FILE_BYTES) return `That file is ${formatBytes(file.size)}. The limit is 32 MiB.`;
  return null;
}

export function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
  if (bytes >= 1024) return `${Math.round(bytes / 1024)} KiB`;
  return `${bytes} B`;
}

/** The server stores a short code. This turns it into a sentence. */
export function failureText(error: string | undefined): string {
  switch (error) {
    case "fetch failed":
      return "Upmo could not fetch that address.";
    case "url blocked":
      return "That address is not allowed.";
    case "conversion failed":
      return "Upmo could not read the contents of that source.";
    case "upload refused":
      return "The server refused that file.";
    case "storage failed":
      return "Upmo could not store that file.";
    case "ingest failed":
      return "Upmo could not index that source.";
    case undefined:
    case "":
      return "Upmo could not add that source.";
    default:
      return error;
  }
}
