import { Card, Group, List, SimpleGrid, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { IconPlus, IconSearch, IconServer } from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Badge, Button, ConfirmDialog, Input, ProviderCard, StatusAlert, Switch } from "@upmo-ai/components";

import { PROVIDERS_KEY, providersQueryKey, useApi, type ProviderUses } from "../../app/api";
import { formatDate } from "../identity/format";
import { adapterLabel, cardStatus, keyLine, providerProblem, retiredModels, workspaceOff } from "./rules";
import { canManage, sharedNote, type ListedProvider } from "./sharing";

/** Each use as one line, or nothing when nothing uses the account. */
function usedBy(uses: ProviderUses): string[] {
  return [
    ...uses.routes.map((name) => `Route ${name}`),
    ...uses.agents.map((name) => `Agent ${name}`),
    ...uses.automations.map((name) => `Automation ${name}`),
  ];
}

export function ProvidersPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  // The list is read from the shared client on every mount. It is never seeded
  // with a snapshot: seeded data counts as fresh and would freeze the list.
  const providersQuery = useQuery({
    queryKey: providersQueryKey(),
    queryFn: () => api.providers.list(),
  });

  const providers: ListedProvider[] = providersQuery.data ?? [];
  const [disconnectTarget, setDisconnectTarget] = useState<ListedProvider | null>(null);
  const [disconnectError, setDisconnectError] = useState("");

  // meta.shown: the dialog words a failure itself, so the shell banner does not
  // also print the brain's text.
  const removeMutation = useMutation({
    meta: { shown: true },
    mutationFn: (id: string) => api.providers.remove(id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY });
      setDisconnectTarget(null);
    },
    onError: (error: unknown) => setDisconnectError(providerProblem(error)),
  });

  const askDisconnect = (account: ListedProvider | null) => {
    setDisconnectError("");
    setDisconnectTarget(account);
  };

  // Turning an account off asks first and names what uses it. Turning it on does not.
  const [offTarget, setOffTarget] = useState<ListedProvider | null>(null);
  const [switchError, setSwitchError] = useState("");
  const usesQuery = useQuery({
    queryKey: [...PROVIDERS_KEY, offTarget?.id ?? "", "uses"],
    queryFn: () => api.providers.uses(offTarget?.id ?? ""),
    enabled: offTarget !== null,
  });
  const switchMutation = useMutation({
    meta: { shown: true },
    mutationFn: (input: { id: string; enabled: boolean }) => api.providers.update(input.id, { enabled: input.enabled }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY });
      setOffTarget(null);
    },
    onError: (error: unknown) => setSwitchError(providerProblem(error)),
  });
  const askOff = (account: ListedProvider | null) => {
    setSwitchError("");
    setOffTarget(account);
  };
  const uses = usesQuery.data ? usedBy(usesQuery.data) : [];

  const [search, setSearch] = useState("");

  const filteredProviders = providers.filter((prov) => {
    if (!search.trim()) return true;
    const q = search.toLowerCase().trim();
    return (
      prov.name.toLowerCase().includes(q) ||
      prov.adapter.toLowerCase().includes(q) ||
      prov.provider.toLowerCase().includes(q) ||
      prov.models.some((m) => m.toLowerCase().includes(q))
    );
  });

  return (
    <Stack gap="lg" className="identity-page">
      <Group justify="space-between" align="center" wrap="wrap" gap="md">
        <Stack gap={4}>
          <Title order={1}>Providers</Title>
          <Text c="dimmed" size="sm">
            Connect model providers to power agents and tasks. Keys stay on this server.
          </Text>
        </Stack>
        <Group gap="sm" wrap="wrap">
          {providers.length > 0 && (
            <Input
              placeholder="Search providers..."
              leftSection={<IconSearch size={16} aria-hidden="true" />}
              size="sm"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              style={{ width: 220 }}
            />
          )}
          <Button
            component={Link}
            to="/settings/providers/new"
            leftSection={<IconPlus size={16} aria-hidden="true" />}
          >
            Add provider
          </Button>
        </Group>
      </Group>

      {switchError && !offTarget ? (
        <StatusAlert tone="critical" title="Could not turn it on">
          {switchError}
        </StatusAlert>
      ) : null}

      {providersQuery.isPending ? (
        <Text c="dimmed" size="sm" role="status">
          Loading providers
        </Text>
      ) : providersQuery.isError ? (
        <StatusAlert tone="critical" title="Providers could not be loaded">
          {providerProblem(providersQuery.error)}
        </StatusAlert>
      ) : providers.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" style={{ borderStyle: "dashed" }}>
          <Stack align="center" gap="sm" py={48}>
            <ThemeIcon variant="light" size={48} radius="lg" color="teal">
              <IconServer size={24} aria-hidden="true" />
            </ThemeIcon>
            <Title order={3}>Connect a provider</Title>
            <Text c="dimmed" size="sm" maw={360} ta="center">
              Upmo needs at least one model to think with. Local Ollama works without a key.
            </Text>
            <Button
              mt="xs"
              component={Link}
              to="/settings/providers/new"
              leftSection={<IconPlus size={16} aria-hidden="true" />}
            >
              Add provider
            </Button>
          </Stack>
        </Card>
      ) : filteredProviders.length === 0 ? (
        <Card withBorder radius="lg" padding="xl">
          <Text c="dimmed" ta="center">
            No providers found matching &ldquo;{search}&rdquo;.
          </Text>
        </Card>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="md">
          {filteredProviders.map((prov) => {
            // Edit, disconnect and the switch only when the brain says this person may.
            const manage = canManage(prov);
            const note = sharedNote(prov);
            const on = prov.enabled !== false;
            const retired = retiredModels(prov);
            const key = keyLine(prov, formatDate);
            return (
              <Stack key={prov.id} gap="xs">
                <ProviderCard
                  data-testid={`provider-card-${prov.id}`}
                  id={prov.id}
                  brand={prov.provider}
                  name={prov.name}
                  adapter={adapterLabel(prov.adapter)}
                  status={cardStatus(prov.status)}
                  planKind={prov.plan_kind}
                  baseUrl={prov.base_url}
                  models={prov.models}
                  onEdit={
                    manage
                      ? () =>
                          void navigate({
                            to: "/settings/providers/$providerId/edit",
                            params: { providerId: prov.id },
                          })
                      : undefined
                  }
                  onDisconnect={manage ? () => askDisconnect(prov) : undefined}
                />
                {key ? (
                  <Text size="xs" c="dimmed">
                    {key}
                  </Text>
                ) : null}
                <Group gap="sm" justify="space-between" wrap="wrap">
                  {manage ? (
                    <Switch
                      label={on ? "On" : "Off"}
                      aria-label={`${prov.name} on`}
                      checked={on}
                      disabled={switchMutation.isPending}
                      onChange={(event) => {
                        if (event.currentTarget.checked) switchMutation.mutate({ id: prov.id, enabled: true });
                        else askOff(prov);
                      }}
                    />
                  ) : null}
                  <Button
                    component={Link}
                    to={`/settings/providers/${encodeURIComponent(prov.id)}/prices`}
                    variant="subtle"
                    size="xs"
                    aria-label={`Prices for ${prov.name}`}
                  >
                    Prices
                  </Button>
                </Group>
                {note ? (
                  <Group gap="xs">
                    <Badge variant="light" size="sm">
                      {note.badge}
                    </Badge>
                    <Text size="xs" c="dimmed">
                      {note.text}
                    </Text>
                  </Group>
                ) : null}
                {workspaceOff(prov.status) ? (
                  <Group gap="xs">
                    <Badge variant="light" size="sm" color="orange">
                      Turned off by your workspace
                    </Badge>
                  </Group>
                ) : null}
                {retired.length > 0 ? (
                  <Group gap="xs">
                    <Badge variant="light" size="sm" color="orange">
                      Retired
                    </Badge>
                    <Text size="xs" c="dimmed">
                      {retired.join(", ")} no longer listed by the provider.
                    </Text>
                  </Group>
                ) : null}
                {!on ? (
                  <Group gap="xs">
                    <Badge variant="light" size="sm">
                      Turned off
                    </Badge>
                  </Group>
                ) : !prov.usable ? (
                  <Group gap="xs">
                    <Badge variant="light" size="sm">
                      Inactive
                    </Badge>
                  </Group>
                ) : null}
              </Stack>
            );
          })}
        </SimpleGrid>
      )}

      {/* Disconnect Confirm Dialog */}
      <ConfirmDialog
        opened={Boolean(disconnectTarget)}
        title={disconnectTarget ? `Disconnect ${disconnectTarget.name}` : "Disconnect provider"}
        body={`Agents and tasks using models from ${disconnectTarget ? disconnectTarget.name : "this provider"} will pause until another provider is configured.`}
        action="Disconnect provider"
        destructive
        busy={removeMutation.isPending}
        onClose={() => askDisconnect(null)}
        onConfirm={() => {
          if (disconnectTarget) {
            removeMutation.mutate(disconnectTarget.id);
          }
        }}
      >
        {disconnectError ? (
          <StatusAlert tone="critical" title="Could not disconnect">
            {disconnectError}
          </StatusAlert>
        ) : null}
      </ConfirmDialog>

      <ConfirmDialog
        opened={Boolean(offTarget)}
        title={offTarget ? `Turn off ${offTarget.name}` : "Turn off provider"}
        body="Its models leave every picker until it is turned on again."
        action="Turn off"
        destructive
        busy={switchMutation.isPending}
        confirmDisabled={usesQuery.isPending || usesQuery.isError}
        onClose={() => askOff(null)}
        onConfirm={() => {
          if (offTarget) switchMutation.mutate({ id: offTarget.id, enabled: false });
        }}
      >
        {usesQuery.isPending ? (
          <Text size="sm" c="dimmed" role="status">
            Checking what uses it
          </Text>
        ) : usesQuery.isError ? (
          <StatusAlert tone="critical" title="Could not check what uses it">
            {providerProblem(usesQuery.error)}
          </StatusAlert>
        ) : uses.length === 0 ? (
          <Text size="sm">Nothing uses it now.</Text>
        ) : (
          <Stack gap={4}>
            <Text size="sm">These stop working:</Text>
            <List size="sm" aria-label="What uses it">
              {uses.map((line) => (
                <List.Item key={line}>{line}</List.Item>
              ))}
            </List>
          </Stack>
        )}
        {switchError ? (
          <StatusAlert tone="critical" title="Could not turn it off">
            {switchError}
          </StatusAlert>
        ) : null}
      </ConfirmDialog>
    </Stack>
  );
}
