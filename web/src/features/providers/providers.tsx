import { Card, Group, SimpleGrid, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { IconPlus, IconSearch, IconServer } from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { Button, ConfirmDialog, Input, ProviderCard } from "@upmo-ai/components";

import { PROVIDERS_KEY, providersQueryKey, useApi, type ProviderAccount } from "../../app/api";

export interface KnownProvider {
  id: string;
  name: string;
  adapter: string;
  description: string;
  defaultPlan: "metered" | "subscription" | "local";
  defaultModels: string[];
  requiresKey: boolean;
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
  },
  {
    id: "openai",
    name: "OpenAI",
    adapter: "openai",
    description: "GPT-4o, o1, o3-mini",
    defaultPlan: "metered",
    defaultModels: ["gpt-4o", "o3-mini"],
    requiresKey: true,
  },
  {
    id: "gemini",
    name: "Google Gemini",
    adapter: "gemini",
    description: "Gemini 2.0 Flash, Gemini 1.5 Pro",
    defaultPlan: "metered",
    defaultModels: ["gemini-2.0-flash", "gemini-1.5-pro"],
    requiresKey: true,
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
    defaultBaseUrl: "https://api.studio.nebius.ai/v1",
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
  },
  {
    id: "groq",
    name: "Groq",
    adapter: "groq",
    description: "Fast inference: Llama 3.3, Mixtral",
    defaultPlan: "metered",
    defaultModels: ["llama-3.3-70b-versatile"],
    requiresKey: true,
  },
  {
    id: "xai",
    name: "xAI",
    adapter: "xai",
    description: "Grok-2, Grok-2-mini",
    defaultPlan: "metered",
    defaultModels: ["grok-2", "grok-2-mini"],
    requiresKey: true,
  },
  {
    id: "openrouter",
    name: "OpenRouter",
    adapter: "openrouter",
    description: "One API key, hundreds of models",
    defaultPlan: "metered",
    defaultModels: ["anthropic/claude-3.7-sonnet", "openai/gpt-4o"],
    requiresKey: true,
  },
  {
    id: "mistral",
    name: "Mistral",
    adapter: "mistral",
    description: "Mistral Large, Codestral",
    defaultPlan: "metered",
    defaultModels: ["mistral-large-latest", "codestral-latest"],
    requiresKey: true,
  },
  {
    id: "together",
    name: "Together AI",
    adapter: "together",
    description: "Fast open-source inference: Llama 3.3, DeepSeek",
    defaultPlan: "metered",
    defaultModels: ["meta-llama/Llama-3.3-70B-Instruct-Turbo", "deepseek-ai/DeepSeek-R1"],
    requiresKey: true,
  },
  {
    id: "cerebras",
    name: "Cerebras",
    adapter: "cerebras",
    description: "Ultra-fast Wafer-Scale inference",
    defaultPlan: "metered",
    defaultModels: ["llama3.3-70b", "llama3.1-8b"],
    requiresKey: true,
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
  },
  {
    id: "perplexity",
    name: "Perplexity",
    adapter: "perplexity",
    description: "Search-augmented models: Sonar",
    defaultPlan: "metered",
    defaultModels: ["sonar-pro", "sonar", "sonar-reasoning"],
    requiresKey: true,
  },
  {
    id: "cohere",
    name: "Cohere",
    adapter: "cohere",
    description: "Command R+, Command R",
    defaultPlan: "metered",
    defaultModels: ["command-r-plus-08-2024", "command-r-08-2024"],
    requiresKey: true,
  },
  {
    id: "lmstudio",
    name: "LM Studio",
    adapter: "lmstudio",
    description: "Local model runtime with OpenAI-compatible API",
    defaultPlan: "local",
    defaultModels: ["local-model"],
    requiresKey: false,
    defaultBaseUrl: "http://localhost:1234/v1",
  },
];

export function ProvidersPage() {
  const probe = useQuery({
    queryKey: ["providers-live"],
    retry: false,
    queryFn: async () => {
      try {
        const response = await fetch("/api/providers", { credentials: "include" });
        if (!response.ok) return null;
        const body: unknown = await response.json();
        return Array.isArray(body) ? (body as ProviderAccount[]) : null;
      } catch {
        return null;
      }
    },
  });

  if (probe.isPending) {
    return (
      <Stack gap="lg" className="identity-page">
        <Title order={1}>Providers</Title>
      </Stack>
    );
  }

  if (Array.isArray(probe.data)) {
    return <ProvidersView live initialProviders={probe.data} />;
  }

  return <ProvidersView live={false} />;
}

function ProvidersView({
  live,
  initialProviders,
}: {
  live: boolean;
  initialProviders?: ProviderAccount[];
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const providersQuery = useQuery({
    queryKey: live ? ["providers-live-data"] : providersQueryKey(),
    initialData: initialProviders,
    queryFn: async () => {
      if (live) {
        const res = await fetch("/api/providers", { credentials: "include" });
        if (!res.ok) throw new Error("Failed to fetch providers");
        return (await res.json()) as ProviderAccount[];
      }
      return api.providers.list();
    },
  });

  const providers = providersQuery.data ?? [];
  const [disconnectTarget, setDisconnectTarget] = useState<ProviderAccount | null>(null);

  const removeMutation = useMutation({
    mutationFn: async (id: string) => {
      if (live) {
        const res = await fetch(`/api/providers/${id}`, {
          method: "DELETE",
          credentials: "include",
        });
        if (!res.ok) throw new Error("Failed to disconnect provider");
        return;
      }
      return api.providers.remove(id);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY });
      if (live) {
        await queryClient.invalidateQueries({ queryKey: ["providers-live-data"] });
      }
      setDisconnectTarget(null);
    },
  });

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

      {providers.length === 0 ? (
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
          {filteredProviders.map((prov) => (
            <ProviderCard
              key={prov.id}
              data-testid={`provider-card-${prov.id}`}
              id={prov.id}
              brand={prov.provider}
              name={prov.name}
              adapter={prov.adapter}
              status={prov.status}
              planKind={prov.plan_kind}
              baseUrl={prov.base_url}
              models={prov.models}
              onEdit={() =>
                void navigate({
                  to: "/providers/$providerId/edit",
                  params: { providerId: prov.id },
                })
              }
              onDisconnect={() => setDisconnectTarget(prov)}
            />
          ))}
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
        onClose={() => setDisconnectTarget(null)}
        onConfirm={() => {
          if (disconnectTarget) {
            removeMutation.mutate(disconnectTarget.id);
          }
        }}
      />
    </Stack>
  );
}
