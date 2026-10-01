import {
  Badge,
  Button,
  Card,
  Group,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  ThemeIcon,
  Title,
} from "@mantine/core";
import { IconPlug, IconPlus, IconSearch, IconSettings } from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { CONNECTORS_KEY, connectorsQueryKey, useApi, type ConnectorGrant } from "../../app/api";
import { BrandMark } from "../../ui/brand-mark";
import { ConfirmDialog } from "../identity/confirm";

export interface KnownConnector {
  id: string;
  name: string;
  description: string;
}

export const KNOWN_CONNECTORS: KnownConnector[] = [
  { id: "github", name: "GitHub", description: "Repositories, issues and pull requests" },
  { id: "gitlab", name: "GitLab", description: "Repositories, merge requests and pipelines" },
  { id: "slack", name: "Slack", description: "Workspaces, channels and messaging" },
  { id: "discord", name: "Discord", description: "Server channels, announcements and bots" },
  { id: "google-calendar", name: "Google Calendar", description: "Events and scheduling" },
  { id: "gmail", name: "Gmail", description: "Read and compose emails" },
  { id: "google-drive", name: "Google Drive", description: "Docs, sheets, and workspace storage" },
  { id: "notion", name: "Notion", description: "Pages, databases and workspaces" },
  { id: "linear", name: "Linear", description: "Issues, cycles and projects" },
  { id: "jira", name: "Jira", description: "Projects, tickets and sprints" },
  { id: "asana", name: "Asana", description: "Projects, tasks and team workflows" },
  { id: "supabase", name: "Supabase", description: "PostgreSQL database and storage" },
  { id: "hubspot", name: "HubSpot", description: "CRM contacts, deals and tickets" },
  { id: "sentry", name: "Sentry", description: "Error tracking and performance alerts" },
  { id: "stripe", name: "Stripe", description: "Invoices, subscriptions and payments" },
  { id: "mcp", name: "MCP Server", description: "Model Context Protocol endpoint" },
];

export function ConnectorsPage() {
  const probe = useQuery({
    queryKey: ["connectors-live"],
    retry: false,
    queryFn: async () => {
      try {
        const response = await fetch("/api/connector-grants", { credentials: "include" });
        if (!response.ok) return null;
        const body: unknown = await response.json();
        return Array.isArray(body) ? (body as ConnectorGrant[]) : null;
      } catch {
        return null;
      }
    },
  });

  if (probe.isPending) {
    return (
      <Stack gap="lg" className="identity-page">
        <Title order={1}>Connectors</Title>
      </Stack>
    );
  }

  if (Array.isArray(probe.data)) {
    return <ConnectorsView live initialGrants={probe.data} />;
  }

  return <ConnectorsView live={false} />;
}

function ConnectorsView({ live, initialGrants }: { live: boolean; initialGrants?: ConnectorGrant[] }) {
  const api = useApi();
  const queryClient = useQueryClient();

  const grantsQuery = useQuery({
    queryKey: live ? ["connectors-live-data"] : connectorsQueryKey(),
    initialData: initialGrants,
    queryFn: async () => {
      if (live) {
        const res = await fetch("/api/connector-grants", { credentials: "include" });
        if (!res.ok) throw new Error("Failed to fetch connectors");
        return (await res.json()) as ConnectorGrant[];
      }
      return api.connectors.list();
    },
  });

  const grants = grantsQuery.data ?? [];
  const [disconnectTarget, setDisconnectTarget] = useState<ConnectorGrant | null>(null);
  const [search, setSearch] = useState("");

  const filteredGrants = grants.filter((grant) => {
    if (!search.trim()) return true;
    const q = search.toLowerCase().trim();
    const tool = toolMeta(grant.connector);
    return (
      tool.name.toLowerCase().includes(q) ||
      tool.description.toLowerCase().includes(q) ||
      grant.connector.toLowerCase().includes(q)
    );
  });

  const removeMutation = useMutation({
    mutationFn: async (connector: string) => {
      if (live) {
        const res = await fetch(`/api/connector-grants/${connector}`, {
          method: "DELETE",
          credentials: "include",
        });
        if (!res.ok) throw new Error("Failed to disconnect");
        return;
      }
      return api.connectors.remove(connector);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: CONNECTORS_KEY });
      if (live) {
        await queryClient.invalidateQueries({ queryKey: ["connectors-live-data"] });
      }
      setDisconnectTarget(null);
    },
  });

  const toolMeta = (id: string) => {
    return KNOWN_CONNECTORS.find((t) => t.id === id) ?? { id, name: id, description: "External connector" };
  };

  return (
    <Stack gap="lg" className="identity-page">
      <Group justify="space-between" align="center" wrap="wrap" gap="md">
        <Stack gap={4}>
          <Title order={1}>Connectors</Title>
          <Text c="dimmed" size="sm">
            GitHub, email, calendar and any MCP server. Agents use only the connectors you add.
          </Text>
        </Stack>
        <Group gap="sm" wrap="wrap">
          {grants.length > 0 && (
            <TextInput
              placeholder="Search connectors..."
              leftSection={<IconSearch size={16} aria-hidden="true" />}
              size="sm"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              style={{ width: 220 }}
            />
          )}
          <Button
            component={Link}
            to="/connectors/new"
            leftSection={<IconPlus size={16} aria-hidden="true" />}
          >
            Add connector
          </Button>
        </Group>
      </Group>

      {grants.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" style={{ borderStyle: "dashed" }}>
          <Stack align="center" gap="sm" py={48}>
            <ThemeIcon variant="light" size={48} radius="lg" color="teal">
              <IconPlug size={24} aria-hidden="true" />
            </ThemeIcon>
            <Title order={3}>Connect your tools</Title>
            <Text c="dimmed" size="sm" maw={360} ta="center">
              GitHub, email, calendar and any MCP server. Agents use only the connectors you add.
            </Text>
            <Button
              mt="xs"
              component={Link}
              to="/connectors/new"
              leftSection={<IconPlus size={16} aria-hidden="true" />}
            >
              Add connector
            </Button>
          </Stack>
        </Card>
      ) : filteredGrants.length === 0 ? (
        <Card withBorder radius="lg" padding="xl">
          <Text c="dimmed" ta="center">
            No connectors found matching &ldquo;{search}&rdquo;.
          </Text>
        </Card>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="md">
          {filteredGrants.map((grant) => {
            const tool = toolMeta(grant.connector);
            return (
              <Card
                key={grant.connector}
                withBorder
                radius="lg"
                padding="md"
                data-testid={`connector-card-${grant.connector}`}
              >
                <Stack justify="space-between" h="100%" gap="md">
                  <Stack gap="sm">
                    <Group justify="space-between" align="flex-start" wrap="nowrap">
                      <Group gap="sm" wrap="nowrap">
                        <BrandMark brand={grant.connector} size={32} />
                        <Stack gap={0}>
                          <Text fw={600} size="md">
                            {tool.name}
                          </Text>
                          <Text size="xs" c="dimmed">
                            {tool.description}
                          </Text>
                        </Stack>
                      </Group>
                      <Badge variant="light" color="teal" radius="xl" style={{ flexShrink: 0 }}>
                        Connected
                      </Badge>
                    </Group>

                    <Stack gap="xs" mt="xs">
                      <Group justify="space-between" align="center">
                        <Text size="xs" c="dimmed">
                          What it can reach
                        </Text>
                        <Badge variant="outline" size="sm">
                          {grant.resource_scope === "all" ? "All" : "Only these"}
                        </Badge>
                      </Group>

                      {grant.resource_scope === "these" && grant.resource_ids.length > 0 && (
                        <Group gap={4}>
                          {grant.resource_ids.map((id) => (
                            <Badge key={id} size="xs" variant="default">
                              {id}
                            </Badge>
                          ))}
                        </Group>
                      )}

                      <Group justify="space-between" align="center">
                        <Text size="xs" c="dimmed">
                          Changes
                        </Text>
                        {grant.changes === "allow" ? (
                          <Badge variant="light" color="teal" size="sm">
                            Allow
                          </Badge>
                        ) : grant.changes === "ask" ? (
                          <Badge variant="light" color="yellow" size="sm">
                            Ask first
                          </Badge>
                        ) : (
                          <Badge variant="light" color="red" size="sm">
                            Block
                          </Badge>
                        )}
                      </Group>
                    </Stack>
                  </Stack>

                  <Group justify="flex-end" gap="xs">
                    <Button
                      component={Link}
                      to={`/connectors/${grant.connector}/edit` as any}
                      variant="default"
                      size="sm"
                      leftSection={<IconSettings size={14} aria-hidden="true" />}
                    >
                      Edit permissions
                    </Button>
                    <Button
                      variant="subtle"
                      color="red"
                      size="sm"
                      onClick={() => setDisconnectTarget(grant)}
                    >
                      Disconnect
                    </Button>
                  </Group>
                </Stack>
              </Card>
            );
          })}
        </SimpleGrid>
      )}

      {/* Disconnect Confirm Dialog */}
      <ConfirmDialog
        opened={Boolean(disconnectTarget)}
        title={disconnectTarget ? `Disconnect ${toolMeta(disconnectTarget.connector).name}` : "Disconnect connector"}
        body={`Agents will lose access to ${disconnectTarget ? toolMeta(disconnectTarget.connector).name : "this tool"} immediately.`}
        action="Disconnect"
        busy={removeMutation.isPending}
        onClose={() => setDisconnectTarget(null)}
        onConfirm={() => {
          if (disconnectTarget) {
            removeMutation.mutate(disconnectTarget.connector);
          }
        }}
      />
    </Stack>
  );
}
