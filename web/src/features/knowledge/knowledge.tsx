import { Button, Group, Stack, Text, Title } from "@mantine/core";
import { useState } from "react";

import { EmptyState } from "../../ui/section";
import { sampleFolders, type Folder } from "./sample";

import "./knowledge.css";

export function KnowledgePage() {
  const [folders] = useState<Folder[]>(sampleFolders);

  return (
    <Stack gap="md" className="knowledge-page">
      <Group justify="space-between" align="flex-start" wrap="wrap">
        <Title order={1}>Knowledge</Title>
        <Button variant="light">Add source</Button>
      </Group>
      <Text c="dimmed" size="sm">
        Folders are the access scopes. Sample sources until ingest exists.
      </Text>
      {folders.length === 0 ? (
        <EmptyState
          title="Give Upmo something to read"
          body="Upload files or point to a folder. Agents search it when it helps."
          action={{ label: "Add source", to: "/knowledge" }}
        />
      ) : (
        folders.map((folder) => (
          <section key={folder.id} className="knowledge-folder" aria-labelledby={folder.id}>
            <Title order={2} id={folder.id}>
              {folder.name}
            </Title>
            <Text size="sm" c="dimmed">
              {folder.sources.length === 1 ? "1 source" : `${folder.sources.length} sources`}
            </Text>
            <ul className="knowledge-sources">
              {folder.sources.map((source) => (
                <li key={source.id}>
                  <Text>{source.name}</Text>
                </li>
              ))}
            </ul>
          </section>
        ))
      )}
    </Stack>
  );
}
