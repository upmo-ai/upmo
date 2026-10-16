import { Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { useState } from "react";
import { Button } from "@upmo-ai/components";

import { EmptyState } from "../../ui/section";
import { ShellIcon } from "../../ui/icons";
import { sampleFolders, type Folder } from "./sample";

import "./knowledge.css";

const updatedOn = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
});

function sourceCount(count: number) {
  return count === 1 ? "1 source" : `${count} sources`;
}

function folderMeta(folder: Folder) {
  return `${sourceCount(folder.sources.length)} · updated ${updatedOn.format(new Date(`${folder.updated}T00:00:00Z`))}`;
}

export function KnowledgePage() {
  const [folders, setFolders] = useState<Folder[]>(sampleFolders);

  return (
    <Stack gap="lg" className="knowledge-page">
      <div className="knowledge-header">
        <div className="knowledge-header-copy">
          <Title order={1}>Knowledge</Title>
          <Text c="dimmed" size="sm" mt="xs">
            Folders group sources. Add files to a folder so tasks and chat can retrieve them.
          </Text>
        </div>
        <div className="knowledge-actions">
          <Button variant="default">Add source</Button>
          <Button
            leftSection={<ShellIcon concept="action.add" />}
            onClick={() => {
              setFolders((current) => [
                {
                  id: `folder-${current.length + 1}`,
                  name: "New folder",
                  summary: "Nothing in this folder yet.",
                  updated: new Date().toISOString().slice(0, 10),
                  sources: [],
                },
                ...current,
              ]);
            }}
          >
            New folder
          </Button>
        </div>
      </div>
      {folders.length === 0 ? (
        <EmptyState
          title="Give Upmo something to read"
          body="Add a folder, then upload files or point to a source. Agents search it when it helps."
        />
      ) : (
        <Stack gap="sm">
          <p className="knowledge-count">Folders · {folders.length}</p>
          <ul className="knowledge-grid">
            {folders.map((folder) => (
              <li key={folder.id}>
                <article className="knowledge-card" aria-labelledby={folder.id}>
                  <ThemeIcon variant="light" size={40} radius="md" aria-hidden>
                    <ShellIcon concept="object.folder" />
                  </ThemeIcon>
                  <Title order={2} id={folder.id} className="knowledge-card-title" size="h3">
                    {folder.name}
                  </Title>
                  <Text size="sm" c="dimmed" className="knowledge-card-summary">
                    {folder.summary}
                  </Text>
                  <Text size="sm" c="dimmed">
                    {folderMeta(folder)}
                  </Text>
                </article>
              </li>
            ))}
          </ul>
        </Stack>
      )}
    </Stack>
  );
}
