import { Stack, Text, Title, VisuallyHidden } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button, DataTable, EmptyState, StatusAlert, type DataTableColumn } from "@upmo-ai/components";

import { taskArtifactsQueryKey, useApi } from "../../app/api";
import { CaretIcon, RowAction } from "../../ui/icons";
import { startDownload } from "./download";
import { ArtifactView, createdLabel } from "./viewer";
import { artifactCount, formatSize, kindLabel, type ArtifactRecord } from "./types";

import "./artifacts.css";

/**
 * The Artifacts tab of a task: what the task produced, and a viewer for the
 * one picked. `active` keeps the list unread until the tab is first opened.
 */
export function ArtifactsTab({ taskId, active }: { taskId: string; active: boolean }) {
  const api = useApi();
  const [selectedId, setSelectedId] = useState("");
  const list = useQuery({
    queryKey: taskArtifactsQueryKey(taskId),
    queryFn: () => api.artifacts.list(taskId),
    enabled: active,
    // A running task keeps adding files, so each visit to the tab reads the list again.
    staleTime: 0,
  });

  const items = list.data ?? [];
  const current = items.find((item) => item.id === selectedId) ?? items[0];

  const columns: DataTableColumn<ArtifactRecord>[] = [
    {
      key: "name",
      header: "Name",
      render: (item) => (
        <Button
          variant="subtle"
          size="compact-sm"
          className="artifact-name"
          aria-current={item.id === current?.id ? "true" : undefined}
          onClick={() => setSelectedId(item.id)}
        >
          {item.name}
        </Button>
      ),
    },
    { key: "kind", header: "Kind", render: (item) => kindLabel(item.kind) },
    { key: "size", header: "Size", numeric: true, render: (item) => formatSize(item.size) },
    {
      key: "created",
      header: "Created",
      render: (item) => (
        <time className="artifact-created" dateTime={item.created_at}>
          {createdLabel(item.created_at)}
        </time>
      ),
    },
    {
      key: "actions",
      header: <VisuallyHidden>Actions</VisuallyHidden>,
      render: (item) => (
        <RowAction
          label={`Download ${item.name}`}
          concept="action.download"
          onClick={() => startDownload(api.artifacts.downloadUrl(item.id), item.name)}
        />
      ),
    },
  ];

  return (
    <Stack gap="md" className="artifacts-tab">
      {list.isError ? (
        <StatusAlert tone="critical">{list.error.message || "Artifacts could not be loaded."}</StatusAlert>
      ) : null}
      {list.isLoading ? (
        <Text c="dimmed" role="status">
          Loading artifacts
        </Text>
      ) : null}
      {list.data && items.length === 0 ? (
        <EmptyState
          title="No artifacts yet"
          description="Reports, tables, images and other files this task makes show up here."
          icon={<CaretIcon size={24} />}
          headingLevel={2}
        />
      ) : null}
      {items.length > 0 ? (
        <>
          <Title order={2} id="task-artifacts-title" size="h3">
            Artifacts
          </Title>
          <DataTable
            aria-labelledby="task-artifacts-title"
            minWidth={560}
            columns={columns}
            rows={items}
            rowKey={(item) => item.id}
            summary={artifactCount(items.length)}
          />
          {current ? <ArtifactView key={current.id} artifact={current} /> : null}
        </>
      ) : null}
    </Stack>
  );
}
