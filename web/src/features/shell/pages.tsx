import { Anchor, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { useApi } from "./api";

function EmptyState({ title, body }: { title: string; body: string }) {
  return (
    <Stack align="center" gap="xs" py={48} px="md">
      <Title order={3}>{title}</Title>
      <Text c="dimmed" maw={320} ta="center">
        {body}
      </Text>
    </Stack>
  );
}

function Section({
  title,
  emptyTitle,
  emptyBody,
  children,
}: {
  title: string;
  emptyTitle: string;
  emptyBody: string;
  children?: ReactNode;
}) {
  return (
    <Stack gap="md">
      <Title order={1}>{title}</Title>
      {children}
      <EmptyState title={emptyTitle} body={emptyBody} />
    </Stack>
  );
}

export function HomePage() {
  return (
    <Section
      title="Home"
      emptyTitle="Your workspace"
      emptyBody="Open a section from the sidebar to see what is happening here."
    />
  );
}

export function ChatPage() {
  return (
    <Section
      title="Chat"
      emptyTitle="Start a chat"
      emptyBody="Ask a question, paste a file, or describe what you want done."
    />
  );
}

export function TasksPage() {
  const api = useApi();
  const tasks = useQuery({
    queryKey: ["tasks"],
    queryFn: () => api.listTasks(),
  });
  const items = tasks.data ?? [];

  return (
    <Stack gap="md">
      <Title order={1}>Tasks</Title>
      {tasks.data && items.length === 0 ? (
        <EmptyState
          title="Your first task"
          body="Give Upmo a goal. It plans the work, does it and checks the result while you do something else."
        />
      ) : (
        <ul className="shell-tasks">
          {items.map((task) => (
            <li key={task.id}>{task.title}</li>
          ))}
        </ul>
      )}
    </Stack>
  );
}

export function AutomationsPage() {
  return (
    <Section
      title="Automations"
      emptyTitle="Run tasks on a schedule"
      emptyBody="A daily digest, a weekly review, a check every hour."
    />
  );
}

export function AgentsPage() {
  return (
    <Section
      title="Agents"
      emptyTitle="Add an agent"
      emptyBody="Agents run tasks with the tools you give them."
    />
  );
}

export function KnowledgePage() {
  return (
    <Section
      title="Knowledge"
      emptyTitle="Give Upmo something to read"
      emptyBody="Upload files or point to a folder. Agents search it when it helps."
    />
  );
}

export function UsagePage() {
  return (
    <Section
      title="Usage"
      emptyTitle="Usage shows up here"
      emptyBody="Token counts and cost appear after a task runs."
    />
  );
}

export function SettingsPage() {
  return (
    <Section
      title="Settings"
      emptyTitle="Workspace settings"
      emptyBody="Connectors and other sections for this workspace live here."
    >
      <Anchor component={Link} to="/settings/connectors" fw={600}>
        Connectors
      </Anchor>
    </Section>
  );
}

export function ConnectorsPage() {
  return (
    <Section
      title="Connectors"
      emptyTitle="Connect your tools"
      emptyBody="GitHub, email, calendar and any MCP server. Agents use only the connectors you add."
    />
  );
}
