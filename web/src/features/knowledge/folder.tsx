import { Group, Stack, Text, Title, VisuallyHidden } from "@mantine/core";
import { IconArrowLeft } from "@tabler/icons-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useState } from "react";
import {
  Badge,
  Button,
  ConfirmDialog,
  DataTable,
  EmptyState,
  Input,
  PageHeader,
  Spinner,
  StatusAlert,
  type DataTableColumn,
} from "@upmo-ai/components";

import {
  KNOWLEDGE_FOLDERS_KEY,
  KNOWLEDGE_SEARCH_KEY,
  knowledgeFoldersQueryKey,
  knowledgeSearchQueryKey,
  knowledgeSourcesQueryKey,
  useApi,
  useWrite,
  type KnowledgeFolder,
  type KnowledgeSource,
} from "../../app/api";
import { CaretIcon, RowAction } from "../../ui/icons";
import { AddSource } from "./add-source";
import { FolderDialog } from "./folder-dialog";
import { failureText, sourceCount, type SourceKind, type SourceProvenance } from "./types";

import "./knowledge.css";

/** How often the source list is read again while a source is still pending. */
export const PENDING_POLL_MS = 1000;

const updatedOn = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
});

const kindLabel: Record<SourceKind, string> = {
  markdown: "Markdown",
  file: "File",
  url: "Web page",
  clip: "Web clip",
};

const provenanceLabel: Record<SourceProvenance, string> = {
  curated: "Curated",
  web: "From the web",
};

function StatusBadge({ status }: { status: KnowledgeSource["status"] }) {
  if (status === "pending") {
    return (
      <Badge color="gray" leftSection={<Spinner size={12} aria-hidden />}>
        Pending
      </Badge>
    );
  }
  return status === "ready" ? <Badge color="teal">Ready</Badge> : <Badge color="red">Failed</Badge>;
}

function pendingNote(count: number) {
  return count === 1 ? "1 source is still being added." : `${count} sources are still being added.`;
}

export function KnowledgeFolderPage() {
  const params = useParams({ strict: false });
  const folderId = typeof params.folderId === "string" ? params.folderId : "";
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const folders = useQuery({
    queryKey: knowledgeFoldersQueryKey(),
    queryFn: () => api.knowledge.folders.list(),
  });
  const folder = folders.data?.find((item) => item.id === folderId);
  const sources = useQuery({
    queryKey: knowledgeSourcesQueryKey(folderId),
    queryFn: () => api.knowledge.sources.list(folderId),
    enabled: Boolean(folder),
    refetchOnMount: "always",
    refetchInterval: (query) =>
      query.state.data?.some((item) => item.status === "pending") ? PENDING_POLL_MS : false,
  });

  const [renaming, setRenaming] = useState(false);
  const [renameError, setRenameError] = useState("");
  const [deletingFolder, setDeletingFolder] = useState(false);
  const [deleteSourceId, setDeleteSourceId] = useState<string | null>(null);
  const [leaving, setLeaving] = useState(false);

  const sourceKeys = [
    KNOWLEDGE_FOLDERS_KEY,
    knowledgeSourcesQueryKey(folderId),
    KNOWLEDGE_SEARCH_KEY,
  ] as const;

  const rename = useWrite((name: string) => api.knowledge.folders.rename(folderId, name), [KNOWLEDGE_FOLDERS_KEY], {
    shown: true,
    onSuccess: () => setRenaming(false),
    onError: (cause) => setRenameError(cause.message),
  });

  const removeFolder = useWrite(() => api.knowledge.folders.remove(folderId), [], {
    onSuccess: async () => {
      setLeaving(true);
      queryClient.setQueryData<KnowledgeFolder[]>(knowledgeFoldersQueryKey(), (current) =>
        current?.filter((item) => item.id !== folderId),
      );
      queryClient.removeQueries({ queryKey: knowledgeSourcesQueryKey(folderId) });
      await navigate({ to: "/knowledge" });
      await queryClient.invalidateQueries({ queryKey: KNOWLEDGE_FOLDERS_KEY });
    },
  });

  const reingest = useWrite((sourceId: string) => api.knowledge.sources.reingest(folderId, sourceId), sourceKeys);

  const removeSource = useWrite((sourceId: string) => api.knowledge.sources.remove(folderId, sourceId), sourceKeys, {
    onSuccess: () => setDeleteSourceId(null),
  });

  const list = sources.data ?? [];
  const pending = list.filter((item) => item.status === "pending").length;
  const deleting = list.find((item) => item.id === deleteSourceId);

  if (leaving) return null;
  if (folders.isError) {
    return <StatusAlert tone="critical">{folders.error.message || "The folder could not be loaded."}</StatusAlert>;
  }
  if (!folders.data) return null;
  if (!folder) {
    return (
      <Stack gap="md" className="knowledge-page">
        <Title order={1}>Folder not found</Title>
        <Text c="dimmed">This folder is not in the workspace.</Text>
        <div>
          <Button component={Link} to="/knowledge" variant="default">
            Back to Knowledge
          </Button>
        </div>
      </Stack>
    );
  }

  const columns: DataTableColumn<KnowledgeSource>[] = [
    {
      key: "title",
      header: "Source",
      render: (item) => (
        <Stack gap={2}>
          <Text fw={600}>{item.title}</Text>
          {item.source_ref !== "" && item.source_ref !== item.title ? (
            <Text size="xs" c="dimmed" className="knowledge-ref">
              {item.source_ref}
            </Text>
          ) : null}
          {item.status === "failed" ? (
            <Text size="sm" c="red">
              {failureText(item.error)}
            </Text>
          ) : null}
        </Stack>
      ),
    },
    {
      key: "kind",
      header: "Type",
      render: (item) => (
        <Stack gap={2}>
          <Text size="sm">{kindLabel[item.kind]}</Text>
          <Text size="xs" c="dimmed">
            {provenanceLabel[item.provenance]}
          </Text>
        </Stack>
      ),
    },
    {
      key: "status",
      header: "Status",
      render: (item) => <StatusBadge status={item.status} />,
    },
    {
      key: "revision",
      header: "Revision",
      numeric: true,
      render: (item) => (item.revision > 0 ? item.revision : "Not yet"),
    },
    {
      key: "updated",
      header: "Updated",
      render: (item) => (
        <Text size="sm" c="dimmed">
          {updatedOn.format(new Date(item.updated_at))}
        </Text>
      ),
    },
    {
      key: "actions",
      header: <VisuallyHidden>Actions</VisuallyHidden>,
      render: (item) => (
        <Group gap="xs" wrap="nowrap" justify="flex-end">
          {item.status === "failed" ? (
            <Button
              size="compact-sm"
              variant="default"
              aria-label={`Retry ${item.title}`}
              disabled={reingest.isPending}
              loading={reingest.isPending && reingest.variables === item.id}
              onClick={() => reingest.mutate(item.id)}
            >
              Retry
            </Button>
          ) : null}
          {item.status === "ready" ? (
            <Button
              size="compact-sm"
              variant="default"
              aria-label={`Re-ingest ${item.title}`}
              disabled={reingest.isPending}
              loading={reingest.isPending && reingest.variables === item.id}
              onClick={() => reingest.mutate(item.id)}
            >
              Re-ingest
            </Button>
          ) : null}
          <RowAction
            label={`Delete ${item.title}`}
            concept="action.delete"
            onClick={() => setDeleteSourceId(item.id)}
          />
        </Group>
      ),
    },
  ];

  return (
    <Stack gap="lg" className="knowledge-page">
      <div>
        <Button
          component={Link}
          to="/knowledge"
          variant="subtle"
          size="compact-sm"
          leftSection={<IconArrowLeft size={16} aria-hidden="true" />}
        >
          All folders
        </Button>
      </div>
      <PageHeader
        title={folder.name}
        description={sources.data ? sourceCount(list.length) : undefined}
        actions={
          <>
            <Button
              variant="default"
              onClick={() => {
                setRenameError("");
                setRenaming(true);
              }}
            >
              Rename
            </Button>
            <Button variant="default" color="red" onClick={() => setDeletingFolder(true)}>
              Delete folder
            </Button>
          </>
        }
      />
      <AddSource folderId={folderId} />
      <Stack gap="sm">
        <Title order={2} id="knowledge-sources-title" size="h3">
          Sources
        </Title>
        {sources.isError ? (
          <StatusAlert tone="critical">{sources.error.message || "Sources could not be loaded."}</StatusAlert>
        ) : null}
        {pending > 0 ? (
          <Text size="sm" c="dimmed" role="status">
            {pendingNote(pending)}
          </Text>
        ) : null}
        {sources.data && list.length === 0 ? (
          <EmptyState
            title="Nothing in this folder yet"
            description="Add a file, a web page or some Markdown above. Upmo reads it once and keeps it searchable."
            icon={<CaretIcon size={24} />}
          />
        ) : null}
        {list.length > 0 ? (
          <DataTable
            aria-labelledby="knowledge-sources-title"
            minWidth={760}
            columns={columns}
            rows={list}
            rowKey={(item) => item.id}
          />
        ) : null}
      </Stack>
      {list.length > 0 ? <SearchFolder folderId={folderId} sources={list} /> : null}
      <FolderDialog
        opened={renaming}
        title="Rename folder"
        action="Save name"
        initial={folder.name}
        busy={rename.isPending}
        error={renameError}
        onSubmit={(name) => {
          setRenameError("");
          rename.mutate(name);
        }}
        onClose={() => setRenaming(false)}
      />
      <ConfirmDialog
        opened={deletingFolder}
        title={`Delete ${folder.name}?`}
        body={`Upmo will stop using ${sourceCount(list.length)} in this folder. This cannot be undone.`}
        action="Delete folder"
        destructive
        busy={removeFolder.isPending}
        onClose={() => setDeletingFolder(false)}
        onConfirm={() => removeFolder.mutate()}
      />
      <ConfirmDialog
        opened={deleting !== undefined}
        title={deleting ? `Delete ${deleting.title}?` : "Delete this source?"}
        body="Upmo will stop using it. You can add it again later."
        action="Delete source"
        destructive
        busy={removeSource.isPending}
        onClose={() => setDeleteSourceId(null)}
        onConfirm={() => {
          if (deleteSourceId) removeSource.mutate(deleteSourceId);
        }}
      />
    </Stack>
  );
}

function SearchFolder({ folderId, sources }: { folderId: string; sources: KnowledgeSource[] }) {
  const api = useApi();
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const hits = useQuery({
    queryKey: knowledgeSearchQueryKey(folderId, query),
    queryFn: () => api.knowledge.search(folderId, query),
    enabled: query !== "",
  });
  const titles = new Map(sources.map((item) => [item.id, item.title]));

  return (
    <Stack gap="sm">
      <Title order={2} id="knowledge-search-title" size="h3">
        Search this folder
      </Title>
      <form
        className="knowledge-search"
        onSubmit={(event) => {
          event.preventDefault();
          const next = draft.trim();
          if (next === "") return;
          if (next === query) void hits.refetch();
          else setQuery(next);
        }}
      >
        <Input
          aria-label="Search query"
          placeholder="What do you want to find?"
          value={draft}
          onChange={(event) => setDraft(event.currentTarget.value)}
        />
        <Button type="submit" disabled={draft.trim() === ""} loading={hits.isFetching}>
          Search
        </Button>
      </form>
      {hits.isError ? <StatusAlert tone="critical">{hits.error.message}</StatusAlert> : null}
      {query !== "" && hits.data && hits.data.length === 0 ? (
        <Text c="dimmed" role="status">
          No matches for &ldquo;{query}&rdquo;.
        </Text>
      ) : null}
      {hits.data && hits.data.length > 0 ? (
        <ul className="knowledge-hits" aria-label="Search results">
          {hits.data.map((hit) => (
            <li key={`${hit.source_id}-${hit.seq}`} className="knowledge-hit">
              <Text fw={600} size="sm">
                {titles.get(hit.source_id) ?? "Deleted source"}
              </Text>
              <Text>{hit.body}</Text>
            </li>
          ))}
        </ul>
      ) : null}
    </Stack>
  );
}
