import { Group, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useId, useMemo, useState } from "react";
import { Button, CodeBlock, StatusAlert, Table } from "@upmo-ai/components";

import { artifactTextQueryKey, useApi } from "../../app/api";
import { ShellIcon } from "../../ui/icons";
import { parseCsv } from "./csv";
import { MarkdownView } from "./markdown-view";
import { MAX_VIEW_BYTES, formatSize, kindLabel, type ArtifactRecord } from "./types";

/** The sandbox of an HTML preview. Empty on purpose: no scripts, forms, pop-ups or same-origin access. */
export const htmlSandbox = "";

/** The most CSV rows shown. The download has the rest. */
export const MAX_CSV_ROWS = 500;

const languages: Record<string, string> = {
  py: "python",
  js: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  jsx: "jsx",
  ts: "typescript",
  tsx: "tsx",
  json: "json",
  yml: "yaml",
  yaml: "yaml",
  go: "go",
  rs: "rust",
  sql: "sql",
  css: "css",
  diff: "diff",
  patch: "diff",
  xml: "markup",
  svg: "markup",
  c: "c",
  h: "cpp",
  cc: "cpp",
  cpp: "cpp",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
};

/** The highlighting language for a file name. Unknown extensions show as plain text. */
export function languageFor(name: string): string {
  const dot = name.lastIndexOf(".");
  if (dot < 0) return "text";
  const extension = name.slice(dot + 1).toLowerCase();
  return Object.hasOwn(languages, extension) ? (languages[extension] ?? "text") : "text";
}

const createdOn = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
});

export function createdLabel(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? "Unknown" : createdOn.format(date);
}

type Viewer = "image" | "text" | "none" | "too-large";

const textKinds = new Set(["markdown", "csv", "text", "code", "html"]);

/** Which viewer opens an artifact. A kind with no viewer, or text over the limit, is a download. */
export function viewerFor(artifact: ArtifactRecord): Viewer {
  if (artifact.kind === "image") return "image";
  if (!textKinds.has(artifact.kind)) return "none";
  return artifact.size > MAX_VIEW_BYTES ? "too-large" : "text";
}

export function ArtifactView({ artifact }: { artifact: ArtifactRecord }) {
  const api = useApi();
  const titleId = useId();
  const url = api.artifacts.downloadUrl(artifact.id);
  const viewer = viewerFor(artifact);

  return (
    <section aria-labelledby={titleId} className="artifact-view">
      <Group justify="space-between" align="flex-start" wrap="wrap" gap="sm">
        <Stack gap={2}>
          <Title order={3} id={titleId} size="h4" className="artifact-view-title">
            {artifact.name}
          </Title>
          <Text size="xs" c="dimmed">
            {kindLabel(artifact.kind)} · {formatSize(artifact.size)}
          </Text>
        </Stack>
        <Button
          component="a"
          href={url}
          download={artifact.name}
          variant="default"
          leftSection={<ShellIcon concept="action.download" size={16} />}
          aria-label={`Download ${artifact.name}`}
        >
          Download
        </Button>
      </Group>
      {viewer === "image" ? <ImageView artifact={artifact} url={url} /> : null}
      {viewer === "text" ? <TextView artifact={artifact} /> : null}
      {viewer === "too-large" ? (
        <Text c="dimmed" role="status">
          This file is {formatSize(artifact.size)}, too large to show here. Download it to read it.
        </Text>
      ) : null}
      {viewer === "none" ? (
        <Text c="dimmed" role="status">
          There is no preview for {artifact.kind === "pdf" ? "PDF files" : "this kind of file"} yet. Download it to open
          it.
        </Text>
      ) : null}
    </section>
  );
}

function ImageView({ artifact, url }: { artifact: ArtifactRecord; url: string }) {
  const [failed, setFailed] = useState(false);
  if (failed) {
    return (
      <Text c="dimmed" role="status">
        This image could not be shown. Download it to open it.
      </Text>
    );
  }
  return (
    <div className="artifact-image-frame">
      <img
        className="artifact-image"
        src={url}
        alt={artifact.name}
        referrerPolicy="no-referrer"
        decoding="async"
        onError={() => setFailed(true)}
      />
    </div>
  );
}

function TextView({ artifact }: { artifact: ArtifactRecord }) {
  const api = useApi();
  const text = useQuery({
    queryKey: artifactTextQueryKey(artifact.id),
    queryFn: () => api.artifacts.text(artifact.id),
  });
  if (text.isError) {
    return <StatusAlert tone="critical">{text.error.message || "This file could not be loaded."}</StatusAlert>;
  }
  if (text.data === undefined) {
    return (
      <Text c="dimmed" role="status">
        Loading
      </Text>
    );
  }
  const body = text.data;
  if (artifact.kind === "markdown") return <MarkdownView source={body} />;
  if (artifact.kind === "csv") return <CsvView name={artifact.name} source={body} />;
  if (artifact.kind === "html") return <HtmlView name={artifact.name} source={body} />;
  if (artifact.kind === "code") {
    return <CodeBlock code={body} language={languageFor(artifact.name)} lineNumbers />;
  }
  return <CodeBlock code={body} language="text" />;
}

function CsvView({ name, source }: { name: string; source: string }) {
  const rows = useMemo(() => parseCsv(source), [source]);
  const [head, ...body] = rows;
  if (!head) {
    return (
      <Text c="dimmed" role="status">
        This file is empty.
      </Text>
    );
  }
  const shown = body.slice(0, MAX_CSV_ROWS);
  return (
    <Stack gap="xs">
      <div className="artifact-scroll" role="region" aria-label={`${name} as a table`} tabIndex={0}>
        <Table aria-label={`${name} as a table`}>
          <Table.Thead>
            <Table.Tr>
              {head.map((cell, index) => (
                <Table.Th key={index}>{cell}</Table.Th>
              ))}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {shown.map((row, index) => (
              <Table.Tr key={index}>
                {row.map((cell, column) => (
                  <Table.Td key={column}>{cell}</Table.Td>
                ))}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </div>
      {body.length > shown.length ? (
        <Text size="sm" c="dimmed" role="status">
          Showing the first {MAX_CSV_ROWS} of {body.length} rows. Download the file for the rest.
        </Text>
      ) : null}
    </Stack>
  );
}

function HtmlView({ name, source }: { name: string; source: string }) {
  return (
    <Stack gap="xs">
      <iframe
        className="artifact-frame"
        title={`${name} preview`}
        sandbox={htmlSandbox}
        srcDoc={source}
        referrerPolicy="no-referrer"
      />
      <Text size="sm" c="dimmed">
        Shown in a sandbox. Scripts, forms and pop-ups are blocked.
      </Text>
    </Stack>
  );
}
