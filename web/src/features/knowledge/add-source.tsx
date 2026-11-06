import { Group, Stack, Text, Title } from "@mantine/core";
import { useRef, useState, type DragEvent } from "react";
import { Button, Input, StatusAlert, Tabs, Textarea } from "@upmo-ai/components";

import {
  KNOWLEDGE_FOLDERS_KEY,
  KNOWLEDGE_SEARCH_KEY,
  knowledgeSourcesQueryKey,
  useApi,
  useWrite,
} from "../../app/api";
import { ShellIcon } from "../../ui/icons";
import { FILE_EXTENSIONS, fileProblem, formatBytes } from "./types";

const accept = FILE_EXTENSIONS.join(",");

function sourceKeys(folderId: string) {
  return [KNOWLEDGE_FOLDERS_KEY, knowledgeSourcesQueryKey(folderId), KNOWLEDGE_SEARCH_KEY] as const;
}

function FilePanel({ folderId }: { folderId: string }) {
  const api = useApi();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [title, setTitle] = useState("");
  const [dragging, setDragging] = useState(false);
  const [error, setError] = useState("");

  const add = useWrite(
    (chosen: File) => api.knowledge.sources.addFile(folderId, { file: chosen, title }),
    sourceKeys(folderId),
    {
      shown: true,
      onSuccess: () => {
        setFile(null);
        setTitle("");
        setError("");
        if (input.current) input.current.value = "";
      },
      onError: (cause) => setError(cause.message),
    },
  );

  const choose = (next: File | undefined) => {
    setError("");
    setFile(next ?? null);
  };

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    setDragging(false);
    choose(event.dataTransfer.files[0]);
  };

  const problem = file ? fileProblem(file) : null;

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (file && !problem) add.mutate(file);
      }}
    >
      <Stack gap="md">
        <div
          className="knowledge-drop"
          data-dragging={dragging || undefined}
          onDragOver={(event) => {
            event.preventDefault();
            setDragging(true);
          }}
          onDragLeave={(event) => {
            const into = event.relatedTarget;
            if (into instanceof Node && event.currentTarget.contains(into)) return;
            setDragging(false);
          }}
          onDrop={onDrop}
        >
          <ShellIcon concept="object.source" size={24} />
          <Text>Drop a file here, or choose one.</Text>
          <Button type="button" variant="default" onClick={() => input.current?.click()}>
            Choose file
          </Button>
          <input
            ref={input}
            type="file"
            hidden
            aria-label="File to upload"
            accept={accept}
            onChange={(event) => choose(event.currentTarget.files?.[0])}
          />
          <Text size="sm" c="dimmed">
            PDF, Word, Markdown, text or HTML. Up to 32 MiB.
          </Text>
        </div>
        {file ? (
          <Group gap="xs" justify="space-between" wrap="nowrap" className="knowledge-chosen">
            <Text fw={600} className="knowledge-chosen-name">
              {file.name}
            </Text>
            <Text size="sm" c="dimmed">
              {formatBytes(file.size)}
            </Text>
            <Button
              type="button"
              variant="subtle"
              size="compact-sm"
              onClick={() => {
                choose(undefined);
                if (input.current) input.current.value = "";
              }}
            >
              Clear file
            </Button>
          </Group>
        ) : null}
        {problem ? <StatusAlert tone="critical">{problem}</StatusAlert> : null}
        {error ? <StatusAlert tone="critical">{error}</StatusAlert> : null}
        <Input
          label="Title (optional)"
          description="Defaults to the file name."
          value={title}
          onChange={(event) => setTitle(event.currentTarget.value)}
        />
        <div>
          <Button type="submit" loading={add.isPending} disabled={!file || problem !== null}>
            Add file
          </Button>
        </div>
      </Stack>
    </form>
  );
}

function UrlPanel({ folderId }: { folderId: string }) {
  const api = useApi();
  const [url, setUrl] = useState("");
  const [title, setTitle] = useState("");
  const [error, setError] = useState("");

  const add = useWrite(
    () => api.knowledge.sources.addUrl(folderId, { url, title }),
    sourceKeys(folderId),
    {
      shown: true,
      onSuccess: () => {
        setUrl("");
        setTitle("");
        setError("");
      },
      onError: (cause) => setError(cause.message),
    },
  );

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (url.trim() !== "") add.mutate();
      }}
    >
      <Stack gap="md">
        <Input
          label="Web address"
          description="Upmo fetches the page and keeps a copy you can search."
          placeholder="https://example.com/handbook"
          inputMode="url"
          autoComplete="off"
          value={url}
          error={error || undefined}
          onChange={(event) => {
            setError("");
            setUrl(event.currentTarget.value);
          }}
        />
        <Input
          label="Title (optional)"
          description="Defaults to the site name."
          value={title}
          onChange={(event) => setTitle(event.currentTarget.value)}
        />
        <div>
          <Button type="submit" loading={add.isPending} disabled={url.trim() === ""}>
            Add web page
          </Button>
        </div>
      </Stack>
    </form>
  );
}

function MarkdownPanel({ folderId }: { folderId: string }) {
  const api = useApi();
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [error, setError] = useState("");

  const add = useWrite(
    () => api.knowledge.sources.addMarkdown(folderId, { title, body }),
    sourceKeys(folderId),
    {
      shown: true,
      onSuccess: () => {
        setTitle("");
        setBody("");
        setError("");
      },
      onError: (cause) => setError(cause.message),
    },
  );

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (title.trim() !== "" && body.trim() !== "") add.mutate();
      }}
    >
      <Stack gap="md">
        {error ? <StatusAlert tone="critical">{error}</StatusAlert> : null}
        <Input
          label="Title"
          value={title}
          onChange={(event) => {
            setError("");
            setTitle(event.currentTarget.value);
          }}
        />
        <Textarea
          label="Markdown"
          description="Paste the text as it is. Upmo keeps it word for word."
          minRows={6}
          value={body}
          onChange={(event) => {
            setError("");
            setBody(event.currentTarget.value);
          }}
        />
        <div>
          <Button
            type="submit"
            loading={add.isPending}
            disabled={title.trim() === "" || body.trim() === ""}
          >
            Add Markdown
          </Button>
        </div>
      </Stack>
    </form>
  );
}

export function AddSource({ folderId }: { folderId: string }) {
  return (
    <section className="knowledge-add" aria-labelledby="knowledge-add-title">
      <Title order={2} id="knowledge-add-title" size="h3" mb="sm">
        Add a source
      </Title>
      <Tabs defaultValue="file" keepMounted>
        <Tabs.List>
          <Tabs.Tab value="file">File</Tabs.Tab>
          <Tabs.Tab value="url">Web page</Tabs.Tab>
          <Tabs.Tab value="markdown">Markdown</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="file" pt="md">
          <FilePanel folderId={folderId} />
        </Tabs.Panel>
        <Tabs.Panel value="url" pt="md">
          <UrlPanel folderId={folderId} />
        </Tabs.Panel>
        <Tabs.Panel value="markdown" pt="md">
          <MarkdownPanel folderId={folderId} />
        </Tabs.Panel>
      </Tabs>
    </section>
  );
}
