import { Card, Group, SimpleGrid, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { IconPlug, IconPlus, IconSearch } from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Button, ConfirmDialog, ConnectorCard, Input } from "@upmo-ai/components";

import { CONNECTORS_KEY, connectorsQueryKey, useApi, type ConnectorGrant } from "../../app/api";
import { ConnectorRulesRow } from "./connector-rules";

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
    return <ConnectorsView live />;
  }

  return <ConnectorsView live={false} />;
}

function ConnectorsView({ live }: { live: boolean }) {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  // The list fetches on mount. Seeding it with the probe snapshot would
  // freeze it there: initialData counts as fresh, and staleTime is infinite.
  const grantsQuery = useQuery({
    queryKey: live ? ["connectors-live-data"] : connectorsQueryKey(),
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
        await queryClient.invalidateQueries({ queryKey: ["connectors-live"] });
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
            <Input
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

      {grantsQuery.isPending ? (
        <Text c="dimmed" size="sm" role="status">
          Loading connectors
        </Text>
      ) : grants.length === 0 ? (
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
            const global = grant.scope === "global";
            return (
              <Stack key={grant.connector} gap="xs">
                <ConnectorCard
                  data-testid={`connector-card-${grant.connector}`}
                  id={grant.connector}
                  name={tool.name}
                  description={tool.description}
                  resourceScope={grant.resource_scope}
                  resourceIds={grant.resource_ids}
                  changes={grant.changes}
                  onEdit={
                    global
                      ? undefined
                      : () =>
                          void navigate({
                            to: "/connectors/$connectorId/edit",
                            params: { connectorId: grant.connector },
                          })
                  }
                  onDisconnect={global ? undefined : () => setDisconnectTarget(grant)}
                />
                {live ? (
                  // Rules are not served by the live API yet, so none are shown.
                  <Text size="xs" c="dimmed">
                    Rules are not available.
                  </Text>
                ) : (
                  <ConnectorRulesRow connector={grant.connector} name={tool.name} readOnly={global} />
                )}
              </Stack>
            );
          })}
        </SimpleGrid>
      )}

      {/* Disconnect Confirm Dialog */}
      <ConfirmDialog
        opened={Boolean(disconnectTarget)}
        title={disconnectTarget ? `Disconnect ${toolMeta(disconnectTarget.connector).name}` : "Disconnect connector"}
        body={`Agents will lose access to ${disconnectTarget ? toolMeta(disconnectTarget.connector).name : "this tool"} immediately.`}
        action="Disconnect connector"
        destructive
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
