import { Card, Group, SimpleGrid, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { IconPlus, IconSearch, IconServer } from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Badge, Button, ConfirmDialog, Input, ProviderCard, StatusAlert } from "@upmo-ai/components";

import { PROVIDERS_KEY, providersQueryKey, useApi } from "../../app/api";
import { adapterLabel, providerProblem } from "./rules";
import { canManage, sharedNote, type ListedProvider } from "./sharing";

export interface KnownProvider {
  id: string;
  name: string;
  adapter: string;
  description: string;
  defaultPlan: "metered" | "subscription" | "local";
  defaultModels: string[];
  requiresKey: boolean;
  /**
   * The address the provider is reached at, as the brain's vendor table has it
   * (internal/gateway/domain/vendor.go). The brain adds the chat, models and
   * embeddings paths itself, which it picks from this address.
   */
  defaultBaseUrl?: string;
}

export const KNOWN_PROVIDERS: KnownProvider[] = [
  {
    id: "anthropic",
    name: "Anthropic",
    adapter: "anthropic",
    description: "Claude 3.7 Sonnet, Claude 3.5 Haiku",
    defaultPlan: "metered",
    defaultModels: ["claude-3-7-sonnet", "claude-3-5-haiku"],
    requiresKey: true,
    defaultBaseUrl: "https://api.anthropic.com",
  },
  {
    id: "openai",
    name: "OpenAI",
    adapter: "openai",
    description: "GPT-4o, o1, o3-mini",
    defaultPlan: "metered",
    defaultModels: ["gpt-4o", "o3-mini"],
    requiresKey: true,
    defaultBaseUrl: "https://api.openai.com",
  },
  {
    id: "gemini",
    name: "Google Gemini",
    adapter: "gemini",
    description: "Gemini 2.0 Flash, Gemini 1.5 Pro",
    defaultPlan: "metered",
    defaultModels: ["gemini-2.0-flash", "gemini-1.5-pro"],
    requiresKey: true,
    defaultBaseUrl: "https://generativelanguage.googleapis.com/v1beta/openai",
  },
  {
    id: "nebius",
    name: "Nebius",
    adapter: "nebius",
    description: "DeepSeek-R1, Llama 3.3, Qwen 2.5 on fast GPUs",
    defaultPlan: "metered",
    defaultModels: [
      "deepseek-ai/DeepSeek-R1",
      "meta-llama/Meta-Llama-3.1-70B-Instruct",
      "Qwen/Qwen2.5-Coder-32B-Instruct",
    ],
    requiresKey: true,
    defaultBaseUrl: "https://api.studio.nebius.ai",
  },
  {
    id: "ollama",
    name: "Ollama",
    adapter: "ollama",
    description: "Local models, no API key required",
    defaultPlan: "local",
    defaultModels: ["llama3.3:70b", "deepseek-r1:14b"],
    requiresKey: false,
    defaultBaseUrl: "http://localhost:11434",
  },
  {
    id: "deepseek",
    name: "DeepSeek",
    adapter: "deepseek",
    description: "DeepSeek-R1, DeepSeek-V3",
    defaultPlan: "metered",
    defaultModels: ["deepseek-r1", "deepseek-v3"],
    requiresKey: true,
    defaultBaseUrl: "https://api.deepseek.com",
  },
  {
    id: "groq",
    name: "Groq",
    adapter: "groq",
    description: "Fast inference: Llama 3.3, Mixtral",
    defaultPlan: "metered",
    defaultModels: ["llama-3.3-70b-versatile"],
    requiresKey: true,
    defaultBaseUrl: "https://api.groq.com/openai",
  },
  {
    id: "xai",
    name: "xAI",
    adapter: "xai",
    description: "Grok-2, Grok-2-mini",
    defaultPlan: "metered",
    defaultModels: ["grok-2", "grok-2-mini"],
    requiresKey: true,
    defaultBaseUrl: "https://api.x.ai",
  },
  {
    id: "openrouter",
    name: "OpenRouter",
    adapter: "openrouter",
    description: "One API key, hundreds of models",
    defaultPlan: "metered",
    defaultModels: ["anthropic/claude-3.7-sonnet", "openai/gpt-4o"],
    requiresKey: true,
    defaultBaseUrl: "https://openrouter.ai/api",
  },
  {
    id: "mistral",
    name: "Mistral",
    adapter: "mistral",
    description: "Mistral Large, Codestral",
    defaultPlan: "metered",
    defaultModels: ["mistral-large-latest", "codestral-latest"],
    requiresKey: true,
    defaultBaseUrl: "https://api.mistral.ai",
  },
  {
    id: "together",
    name: "Together AI",
    adapter: "together",
    description: "Fast open-source inference: Llama 3.3, DeepSeek",
    defaultPlan: "metered",
    defaultModels: ["meta-llama/Llama-3.3-70B-Instruct-Turbo", "deepseek-ai/DeepSeek-R1"],
    requiresKey: true,
    defaultBaseUrl: "https://api.together.xyz",
  },
  {
    id: "cerebras",
    name: "Cerebras",
    adapter: "cerebras",
    description: "Ultra-fast Wafer-Scale inference",
    defaultPlan: "metered",
    defaultModels: ["llama3.3-70b", "llama3.1-8b"],
    requiresKey: true,
    defaultBaseUrl: "https://api.cerebras.ai",
  },
  {
    id: "fireworks",
    name: "Fireworks AI",
    adapter: "fireworks",
    description: "Production compound AI inference",
    defaultPlan: "metered",
    defaultModels: [
      "accounts/fireworks/models/llama-v3p3-70b-instruct",
      "accounts/fireworks/models/deepseek-r1",
    ],
    requiresKey: true,
    defaultBaseUrl: "https://api.fireworks.ai/inference",
  },
  {
    id: "perplexity",
    name: "Perplexity",
    adapter: "perplexity",
    description: "Search-augmented models: Sonar",
    defaultPlan: "metered",
    defaultModels: ["sonar-pro", "sonar", "sonar-reasoning"],
    requiresKey: true,
    defaultBaseUrl: "https://api.perplexity.ai",
  },
  {
    id: "cohere",
    name: "Cohere",
    adapter: "cohere",
    description: "Command R+, Command R",
    defaultPlan: "metered",
    defaultModels: ["command-r-plus-08-2024", "command-r-08-2024"],
    requiresKey: true,
    defaultBaseUrl: "https://api.cohere.ai/compatibility/v1",
  },
  {
    id: "lmstudio",
    name: "LM Studio",
    adapter: "lmstudio",
    description: "Local model runtime with OpenAI-compatible API",
    defaultPlan: "local",
    defaultModels: ["local-model"],
    requiresKey: false,
    defaultBaseUrl: "http://localhost:1234",
  },
];

/**
 * A provider with no network behind it, for trying the screens and the e2e
 * checks. It is offered in a dev build only, but an account that uses it is
 * always recognised.
 */
export const TEST_PROVIDER: KnownProvider = {
  id: "fake",
  name: "Test provider",
  adapter: "fake",
  description: "Built in, no network. For trying Upmo out.",
  defaultPlan: "local",
  defaultModels: [],
  requiresKey: true,
};

/** Every provider an account can name. */
export const ALL_PROVIDERS: KnownProvider[] = [...KNOWN_PROVIDERS, TEST_PROVIDER];

/** The providers the picker offers. */
export const PICKABLE_PROVIDERS: KnownProvider[] = import.meta.env.DEV ? ALL_PROVIDERS : KNOWN_PROVIDERS;

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
            to="/providers/new"
            leftSection={<IconPlus size={16} aria-hidden="true" />}
          >
            Add provider
          </Button>
        </Group>
      </Group>

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
              to="/providers/new"
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
            // Edit and disconnect only when the brain says this person may.
            const manage = canManage(prov);
            const note = sharedNote(prov);
            return (
              <Stack key={prov.id} gap="xs">
                <ProviderCard
                  data-testid={`provider-card-${prov.id}`}
                  id={prov.id}
                  brand={prov.provider}
                  name={prov.name}
                  adapter={adapterLabel(prov.adapter)}
                  status={prov.status}
                  planKind={prov.plan_kind}
                  baseUrl={prov.base_url}
                  models={prov.models}
                  onEdit={
                    manage
                      ? () =>
                          void navigate({
                            to: "/providers/$providerId/edit",
                            params: { providerId: prov.id },
                          })
                      : undefined
                  }
                  onDisconnect={manage ? () => askDisconnect(prov) : undefined}
                />
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
    </Stack>
  );
}
