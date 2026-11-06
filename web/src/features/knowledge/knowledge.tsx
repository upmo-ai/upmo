import { Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Button, EmptyState, PageHeader, StatusAlert } from "@upmo-ai/components";

import {
  KNOWLEDGE_FOLDERS_KEY,
  knowledgeFoldersQueryKey,
  useApi,
  useWrite,
  type KnowledgeFolder,
} from "../../app/api";
import { CaretIcon, ShellIcon } from "../../ui/icons";
import { FolderDialog } from "./folder-dialog";
import { sourceCount } from "./types";

import "./knowledge.css";

const updatedOn = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
});

function folderMeta(folder: KnowledgeFolder) {
  return `${sourceCount(folder.source_count)} · updated ${updatedOn.format(new Date(folder.updated_at))}`;
}

export function KnowledgePage() {
  const api = useApi();
  const navigate = useNavigate();
  const folders = useQuery({
    queryKey: knowledgeFoldersQueryKey(),
    queryFn: () => api.knowledge.folders.list(),
  });
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState("");

  const create = useWrite((name: string) => api.knowledge.folders.create(name), [KNOWLEDGE_FOLDERS_KEY], {
    shown: true,
    onSuccess: async (folder) => {
      setCreating(false);
      await navigate({ to: "/knowledge/$folderId", params: { folderId: folder.id } });
    },
    onError: (cause) => setCreateError(cause.message),
  });

  const openCreate = () => {
    setCreateError("");
    setCreating(true);
  };

  const list = folders.data ?? [];

  return (
    <Stack gap="lg" className="knowledge-page">
      <PageHeader
        title="Knowledge"
        description="Folders group sources. Add files, web pages or notes to a folder so tasks and chat can retrieve them."
        actions={
          <Button leftSection={<ShellIcon concept="action.add" />} onClick={openCreate}>
            New folder
          </Button>
        }
      />
      {folders.isError ? (
        <StatusAlert tone="critical">{folders.error.message || "Folders could not be loaded."}</StatusAlert>
      ) : null}
      {folders.data && list.length === 0 ? (
        <EmptyState
          title="Give Upmo something to read"
          description="Add a folder, then upload files or point to a source. Agents search it when it helps."
          icon={<CaretIcon size={24} />}
          headingLevel={2}
          firstRun
        />
      ) : null}
      {list.length > 0 ? (
        <Stack gap="sm">
          <p className="knowledge-count">Folders · {list.length}</p>
          <ul className="knowledge-grid" aria-label="Folders">
            {list.map((folder) => (
              <li key={folder.id}>
                <article className="knowledge-card">
                  <ThemeIcon variant="light" size={40} radius="md" aria-hidden>
                    <ShellIcon concept="object.folder" />
                  </ThemeIcon>
                  <Title order={2} className="knowledge-card-title" size="h3">
                    <Link
                      to="/knowledge/$folderId"
                      params={{ folderId: folder.id }}
                      className="knowledge-card-link"
                    >
                      {folder.name}
                    </Link>
                  </Title>
                  <Text size="sm" c="dimmed">
                    {folderMeta(folder)}
                  </Text>
                </article>
              </li>
            ))}
          </ul>
        </Stack>
      ) : null}
      <FolderDialog
        opened={creating}
        title="New folder"
        action="Create folder"
        busy={create.isPending}
        error={createError}
        onSubmit={(name) => {
          setCreateError("");
          create.mutate(name);
        }}
        onClose={() => setCreating(false)}
      />
    </Stack>
  );
}
