import { Card, Group, PasswordInput, Stack, Text, Title } from "@mantine/core";
import { IconArrowLeft, IconCpu, IconPlus, IconSearch } from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useState } from "react";
import {
  Badge,
  BrandMark,
  BrandTileGrid,
  Button,
  Checkbox,
  ConnectWizard,
  Input,
  Radio,
  Select,
  StatusAlert,
  Table,
  TestConnectionPanel,
  type TestConnectionState,
} from "@upmo-ai/components";

import {
  PROVIDERS_KEY,
  providersQueryKey,
  teamsQueryKey,
  useApi,
  type TestResult,
} from "../../app/api";
import { formatPricing, getProviderModels, type ModelInfo } from "./models";
import { ALL_PROVIDERS, KNOWN_PROVIDERS, PICKABLE_PROVIDERS, type KnownProvider } from "./providers";
import { baseUrlRequired, keyRequired, providerProblem, showsKeyField } from "./rules";
import { canManage, fetchCanShare, type ListedProvider, type SharedAdd, type SharedChange } from "./sharing";

interface ProviderFormPageProps {
  mode: "create" | "edit";
}

interface ProviderFormProps {
  mode: "create" | "edit";
  existing?: ListedProvider;
}

/** Who an account is for: the person alone, the whole workspace, or specific teams. */
type ShareAsk = "own" | "workspace" | "teams";

function baseUrlHelp(meta: KnownProvider): string {
  if (meta.id === "lmstudio") return "Required. Address of your local LM Studio service.";
  if (meta.id === "ollama") return "Required. Address of your local or private Ollama service.";
  if (!baseUrlRequired(meta.adapter)) return "Optional for the built in test provider.";
  return "Required. Address of the provider's API, for example https://api.example.com, without a /v1 path.";
}

function ProviderForm({ mode, existing }: ProviderFormProps) {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const [activeStep, setActiveStep] = useState<number>(mode === "edit" ? 1 : 0);
  const [providerSearch, setProviderSearch] = useState<string>("");

  const [selectedProviderId, setSelectedProviderId] = useState<string>(
    existing?.provider ?? "anthropic",
  );
  const [name, setName] = useState<string>(existing?.name ?? KNOWN_PROVIDERS[0].name);
  const [apiKey, setApiKey] = useState<string>("");
  const [baseUrl, setBaseUrl] = useState<string>(
    existing ? (existing.base_url ?? "") : (KNOWN_PROVIDERS[0].defaultBaseUrl ?? ""),
  );
  const [planKind, setPlanKind] = useState<"metered" | "subscription" | "local">(
    existing?.plan_kind ?? KNOWN_PROVIDERS[0].defaultPlan,
  );

  // Models state
  const [selectedModels, setSelectedModels] = useState<string[]>(
    existing ? existing.models : KNOWN_PROVIDERS[0].defaultModels,
  );
  const [customModelInput, setCustomModelInput] = useState<string>("");

  // Sharing: who may use the account. Only a person who may share is asked.
  const existingShare = existing?.share;
  const initialShare: ShareAsk =
    existingShare?.scope === "team" ? "teams" : existingShare?.scope === "workspace" ? "workspace" : "own";
  const [shareScope, setShareScope] = useState<ShareAsk>(initialShare);
  const [shareTeams, setShareTeams] = useState<string[]>(existingShare?.team_ids ?? []);
  const canShareQuery = useQuery({
    queryKey: [...PROVIDERS_KEY, "sharing"],
    queryFn: fetchCanShare,
  });
  // The choice is offered when adding an account, and when changing one that
  // is shared already; a personal account stays personal.
  const offerSharing = canShareQuery.data === true && (mode === "create" || initialShare !== "own");
  const teamsQuery = useQuery({
    queryKey: teamsQueryKey(),
    queryFn: () => api.shares.teams(),
    enabled: offerSharing,
  });
  const teamList = teamsQuery.data ?? [];
  const sharedWithTeams = offerSharing && shareScope === "teams";

  // Testing & Validation state
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [isTesting, setIsTesting] = useState<boolean>(false);
  const [submitError, setSubmitError] = useState<string>("");

  const selectedMeta: KnownProvider =
    ALL_PROVIDERS.find((p) => p.id === selectedProviderId) ?? KNOWN_PROVIDERS[0];
  // The brain keeps a key for every account that is not local, so only a
  // saved local account may have none.
  const hasSavedKey = existing !== undefined && existing.plan_kind !== "local";
  const showKey = showsKeyField(selectedMeta.requiresKey, planKind);
  const needsKey = keyRequired(planKind, hasSavedKey);
  const needsBaseUrl = baseUrlRequired(selectedMeta.adapter);
  // A key typed while the field was shown is not sent once it is hidden.
  const typedKey = showKey ? apiKey.trim() : "";

  const handleSelectProvider = (provId: string) => {
    if (mode === "edit") return;
    setSelectedProviderId(provId);
    const meta = ALL_PROVIDERS.find((p) => p.id === provId);
    if (meta) {
      setName(meta.name);
      setPlanKind(meta.defaultPlan);
      setBaseUrl(meta.defaultBaseUrl ?? "");
      setSelectedModels([...meta.defaultModels]);
      setApiKey("");
      setTestResult(null);
    }
  };

  const handleToggleModel = (modelId: string) => {
    setTestResult(null);
    setSelectedModels((prev) =>
      prev.includes(modelId) ? prev.filter((m) => m !== modelId) : [...prev, modelId],
    );
  };

  const handleToggleTeam = (teamId: string) => {
    setShareTeams((prev) => (prev.includes(teamId) ? prev.filter((id) => id !== teamId) : [...prev, teamId]));
  };

  const handleAddCustomModel = () => {
    const trimmed = customModelInput.trim();
    if (!trimmed) return;
    if (!selectedModels.includes(trimmed)) {
      setSelectedModels((prev) => [...prev, trimmed]);
      setTestResult(null);
    }
    setCustomModelInput("");
  };

  const runTestConnection = async () => {
    setIsTesting(true);
    setSubmitError("");
    // Without a newly typed key, an account that exists is tested with its saved key.
    const savedId = existing && !typedKey ? existing.id : undefined;
    try {
      const result = await api.providers.test({
        provider: selectedProviderId,
        adapter: selectedMeta.adapter,
        plan_kind: planKind,
        api_key: typedKey || undefined,
        base_url: baseUrl.trim() || undefined,
        model: selectedModels[0],
        id: savedId,
      });
      setTestResult(result);
      // The brain stores the status a saved account's test found.
      if (savedId) await queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY });
    } catch (err: unknown) {
      setTestResult({ ok: false, error: providerProblem(err) });
    } finally {
      setIsTesting(false);
    }
  };

  // The key is read from the form when the request is made and is not a
  // variable of the mutation, so it is not kept in the mutation cache.
  // meta.shown: the form words a failure itself, so the shell banner does not
  // also print the brain's text.
  const saveMutation = useMutation({
    meta: { shown: true },
    mutationFn: async () => {
      setSubmitError("");
      const resolvedBaseUrl = baseUrl.trim() || undefined;

      if (mode === "create") {
        const input: SharedAdd = {
          provider: selectedProviderId,
          name: name.trim() || selectedMeta.name,
          adapter: selectedMeta.adapter,
          plan_kind: planKind,
          base_url: resolvedBaseUrl,
          api_key: typedKey || undefined,
          models: selectedModels,
        };
        if (offerSharing && shareScope !== "own") {
          input.scope = shareScope;
          if (shareScope === "teams") input.team_ids = shareTeams;
        }
        return api.providers.add(input);
      }

      // An empty key field keeps the saved key.
      if (!existing?.id) throw new Error("Missing provider ID");
      const change: SharedChange = {
        name: name.trim() || existing.name,
        plan_kind: planKind,
        base_url: resolvedBaseUrl,
        api_key: typedKey || undefined,
        models: selectedModels,
      };
      if (offerSharing && shareScope !== "own") {
        change.scope = shareScope;
        if (shareScope === "teams") change.team_ids = shareTeams;
      }
      return api.providers.update(existing.id, change);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY });
      await navigate({ to: "/providers" });
    },
    onError: (err: Error) => {
      setSubmitError(providerProblem(err));
    },
  });

  const availableCatalog = getProviderModels(selectedProviderId);
  const catalogIds = new Set(availableCatalog.map((m) => m.id));
  const customOnlyModels = selectedModels.filter((m) => !catalogIds.has(m));

  const filteredProviders = PICKABLE_PROVIDERS.filter((p) => {
    if (!providerSearch.trim()) return true;
    const q = providerSearch.toLowerCase();
    return (
      p.name.toLowerCase().includes(q) ||
      p.description.toLowerCase().includes(q) ||
      p.defaultModels.some((m) => m.toLowerCase().includes(q))
    );
  });

  // Validation checks for advancing steps
  const canProceedStep1 = mode === "edit" || Boolean(selectedProviderId);
  const canProceedStep2 =
    name.trim().length > 0 &&
    (!needsKey || typedKey.length > 0) &&
    (!needsBaseUrl || baseUrl.trim().length > 0) &&
    (!sharedWithTeams || shareTeams.length > 0);
  const canProceedStep3 = selectedModels.length > 0;
  const canSubmit = testResult?.ok === true && !saveMutation.isPending;

  const testState: TestConnectionState = isTesting
    ? "testing"
    : testResult === null
      ? "idle"
      : testResult.ok
        ? "success"
        : "error";
  const testErrorMessage =
    testResult?.error || "Failed to reach provider. Check your API key or endpoint URL.";

  const sharedWithText =
    shareScope === "workspace"
      ? "the whole workspace"
      : teamList
          .filter((team) => shareTeams.includes(team.id))
          .map((team) => team.name)
          .join(", ");

  const providerStep = (
    <Card withBorder radius="lg" padding="lg">
      <Stack gap="md">
        <Group justify="space-between" align="center" wrap="wrap" gap="sm">
          <div>
            <Text fw={600} size="sm">
              Select Provider
            </Text>
            <Text size="xs" c="dimmed">
              Choose an official cloud provider or local inference runtime.
            </Text>
          </div>
          <Input
            placeholder="Filter providers..."
            leftSection={<IconSearch size={16} aria-hidden="true" />}
            size="sm"
            value={providerSearch}
            onChange={(e) => setProviderSearch(e.target.value)}
            style={{ width: 220 }}
          />
        </Group>

        {filteredProviders.length === 0 ? (
          <Card withBorder radius="md" padding="md">
            <Text c="dimmed" size="sm" ta="center">
              No providers found matching &ldquo;{providerSearch}&rdquo;.
            </Text>
          </Card>
        ) : (
          <BrandTileGrid
            items={filteredProviders}
            selectedId={selectedProviderId}
            onSelect={handleSelectProvider}
            kind="provider"
          />
        )}

        <Group justify="space-between" mt="md">
          <Button component={Link} to="/providers" variant="subtle">
            Cancel
          </Button>
          <Button disabled={!canProceedStep1} onClick={() => setActiveStep(1)}>
            Continue
          </Button>
        </Group>
      </Stack>
    </Card>
  );

  const credentialsStep = (
    <Card withBorder radius="lg" padding="lg">
      <Stack gap="md">
        <Group gap="md">
          <BrandMark brand={selectedProviderId} size={40} kind="provider" />
          <Stack gap={2}>
            <Text fw={600} size="lg">
              {selectedMeta.name} Credentials & Plan
            </Text>
            <Text size="xs" c="dimmed">
              Adapter: {selectedMeta.adapter}
            </Text>
          </Stack>
        </Group>

        <Input
          label="Provider display name"
          required
          size="md"
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            setTestResult(null);
          }}
          placeholder="e.g. Anthropic Production, Nebius AI, Local Ollama"
        />

        {showKey ? (
          <PasswordInput
            label="API Key"
            required={needsKey}
            size="md"
            autoComplete="off"
            description={
              hasSavedKey
                ? "Leave blank to keep the currently stored key."
                : needsKey
                  ? "Keys are encrypted at rest and stay on this server."
                  : "Optional on the local plan. Keys are encrypted at rest and stay on this server."
            }
            placeholder={hasSavedKey ? "••••••••••••••••" : "sk-..."}
            value={apiKey}
            onChange={(e) => {
              setApiKey(e.target.value);
              setTestResult(null);
            }}
          />
        ) : null}

        <Input
          label="Base URL"
          required={needsBaseUrl}
          size="md"
          description={baseUrlHelp(selectedMeta)}
          value={baseUrl}
          onChange={(e) => {
            setBaseUrl(e.target.value);
            setTestResult(null);
          }}
          placeholder={selectedMeta.defaultBaseUrl ?? "https://api.example.com"}
        />

        <Select
          label="Plan kind"
          size="md"
          checkIconPosition="right"
          description="Controls rate limits, cost accounting, and scheduling."
          data={[
            { value: "metered", label: "Metered (Pay-as-you-go)" },
            { value: "subscription", label: "Subscription" },
            { value: "local", label: "Local (Self-hosted)" },
          ]}
          value={planKind}
          onChange={(val) => {
            if (val) {
              setPlanKind(val as "metered" | "subscription" | "local");
              setTestResult(null);
            }
          }}
        />

        {offerSharing ? (
          <Stack gap="xs">
            <Text fw={500} size="sm">
              Who can use this provider
            </Text>
            <Radio.Group value={shareScope} onChange={(value) => setShareScope(value as ShareAsk)}>
              <Stack gap="xs">
                {mode === "create" ? <Radio value="own" label="Only me" /> : null}
                <Radio
                  value="workspace"
                  label="The whole workspace"
                  description="Everyone can use it. Only admins change it. Nobody sees the key."
                />
                <Radio
                  value="teams"
                  label="Specific teams"
                  description="Only members of the teams you choose can use it. Nobody sees the key."
                />
              </Stack>
            </Radio.Group>
            {sharedWithTeams ? (
              teamList.length === 0 ? (
                <Text size="sm" c="dimmed">
                  There are no teams yet. Make one under Teams first.
                </Text>
              ) : (
                <Stack gap="xs">
                  {teamList.map((team) => (
                    <Group key={team.id} gap="xs">
                      <Checkbox
                        checked={shareTeams.includes(team.id)}
                        onChange={() => handleToggleTeam(team.id)}
                        aria-label={`Share with ${team.name}`}
                      />
                      <Text size="sm">{team.name}</Text>
                    </Group>
                  ))}
                </Stack>
              )
            ) : null}
          </Stack>
        ) : null}

        <Group justify="space-between" mt="md">
          {mode === "create" ? (
            <Button
              variant="subtle"
              leftSection={<IconArrowLeft size={16} />}
              onClick={() => setActiveStep(0)}
            >
              Back
            </Button>
          ) : (
            <Button component={Link} to="/providers" variant="subtle">
              Cancel
            </Button>
          )}
          <Button
            disabled={!canProceedStep2}
            onClick={() => setActiveStep(2)}
          >
            Continue
          </Button>
        </Group>
      </Stack>
    </Card>
  );

  const modelsStep = (
    <Card withBorder radius="lg" padding="lg">
      <Stack gap="md">
        <div>
          <Text fw={600} size="sm">
            Models & Pricing for {selectedMeta.name}
          </Text>
          <Text size="xs" c="dimmed">
            Select which models are active for this provider. Token pricing is displayed per 1M tokens (MTok).
          </Text>
        </div>

        <Table.ScrollContainer minWidth={600}>
          <Table verticalSpacing="xs" striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th style={{ width: 48 }}>Active</Table.Th>
                <Table.Th>Model</Table.Th>
                <Table.Th>Context</Table.Th>
                <Table.Th>Token Pricing</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {availableCatalog.map((model: ModelInfo) => {
                const isChecked = selectedModels.includes(model.id);
                return (
                  <Table.Tr
                    key={model.id}
                    onClick={() => handleToggleModel(model.id)}
                    style={{ cursor: "pointer" }}
                  >
                    <Table.Td onClick={(e) => e.stopPropagation()}>
                      <Checkbox
                        checked={isChecked}
                        onChange={() => handleToggleModel(model.id)}
                        aria-label={`Select ${model.name}`}
                      />
                    </Table.Td>
                    <Table.Td>
                      <Stack gap={2}>
                        <Text fw={600} size="sm">
                          {model.name}
                        </Text>
                        <Text size="xs" c="dimmed" ff="monospace">
                          {model.id}
                        </Text>
                      </Stack>
                    </Table.Td>
                    <Table.Td>
                      <Badge size="xs" variant="outline">
                        {model.context_window ?? "128k"}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      <Text size="xs" fw={500}>
                        {formatPricing(model.input_per_mtok, model.output_per_mtok)}
                      </Text>
                    </Table.Td>
                  </Table.Tr>
                );
              })}

              {customOnlyModels.map((customId) => (
                <Table.Tr
                  key={customId}
                  onClick={() => handleToggleModel(customId)}
                  style={{ cursor: "pointer" }}
                >
                  <Table.Td onClick={(e) => e.stopPropagation()}>
                    <Checkbox
                      checked={true}
                      onChange={() => handleToggleModel(customId)}
                      aria-label={`Select ${customId}`}
                    />
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={2}>
                      <Text fw={600} size="sm">
                        Custom Model
                      </Text>
                      <Text size="xs" c="dimmed" ff="monospace">
                        {customId}
                      </Text>
                    </Stack>
                  </Table.Td>
                  <Table.Td>
                    <Badge size="xs" variant="outline">
                      Custom
                    </Badge>
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs" c="dimmed">
                      Custom rate
                    </Text>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        <Group align="flex-end" gap="xs">
          <Input
            label="Add Custom Model Identifier"
            size="md"
            description="Type any additional model ID supported by this provider."
            placeholder="e.g. meta-llama/Llama-3.3-70B-Instruct"
            value={customModelInput}
            onChange={(e) => setCustomModelInput(e.target.value)}
            style={{ flex: 1 }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                handleAddCustomModel();
              }
            }}
          />
          <Button
            variant="default"
            size="md"
            leftSection={<IconPlus size={16} />}
            onClick={handleAddCustomModel}
          >
            Add
          </Button>
        </Group>

        <Group gap={6}>
          <Text size="xs" c="dimmed">
            Selected ({selectedModels.length}):
          </Text>
          {selectedModels.map((m) => (
            <Badge key={m} size="xs" variant="default" leftSection={<IconCpu size={10} />}>
              {m}
            </Badge>
          ))}
        </Group>

        <Group justify="space-between" mt="md">
          <Button
            variant="subtle"
            leftSection={<IconArrowLeft size={16} />}
            onClick={() => setActiveStep(1)}
          >
            Back
          </Button>
          <Button
            disabled={!canProceedStep3}
            onClick={() => setActiveStep(3)}
          >
            Continue
          </Button>
        </Group>
      </Stack>
    </Card>
  );

  const validationStep = (
    <Stack gap="lg">
      <Card withBorder radius="lg" padding="lg">
        <Stack gap="md">
          <Title order={3} size="h4">
            Review Configuration
          </Title>

          <Group justify="space-between" align="center">
            <Group gap="md">
              <BrandMark brand={selectedProviderId} size={40} kind="provider" />
              <Stack gap={2}>
                <Text fw={600} size="md">
                  {name}
                </Text>
                <Text size="xs" c="dimmed">
                  {selectedMeta.name} · Adapter: {selectedMeta.adapter} · Plan: {planKind}
                </Text>
              </Stack>
            </Group>
            <Badge variant="light" color="teal">
              {selectedModels.length} {selectedModels.length === 1 ? "model" : "models"} configured
            </Badge>
          </Group>

          {baseUrl ? (
            <Text size="xs" c="dimmed">
              Endpoint: <Text span ff="monospace">{baseUrl}</Text>
            </Text>
          ) : null}

          {offerSharing && shareScope !== "own" ? (
            <Text size="xs" c="dimmed">
              Shared with: {sharedWithText}
            </Text>
          ) : null}

          <Group gap={6}>
            {selectedModels.map((m) => (
              <Badge key={m} size="xs" variant="default" leftSection={<IconCpu size={10} />}>
                {m}
              </Badge>
            ))}
          </Group>
        </Stack>
      </Card>

      <Card withBorder radius="lg" padding="lg">
        <Stack gap="md">
          <div>
            <Text fw={600} size="sm">
              Connection Verification
            </Text>
            <Text size="xs" c="dimmed">
              Test the endpoint and credentials before saving. The provider cannot be connected until validated.
            </Text>
          </div>

          <TestConnectionPanel
            state={testState}
            onTest={runTestConnection}
            latencyMs={testResult?.latency_ms}
            error={testResult?.ok === false ? testErrorMessage : undefined}
          />
        </Stack>
      </Card>

      {submitError ? (
        <StatusAlert tone="critical" title="Could not save the provider">
          {submitError}
        </StatusAlert>
      ) : null}

      <Group justify="space-between" mt="md">
        <Button
          variant="subtle"
          leftSection={<IconArrowLeft size={16} />}
          onClick={() => setActiveStep(2)}
        >
          Back
        </Button>
        <Group gap="xs">
          <Button component={Link} to="/providers" variant="subtle">
            Cancel
          </Button>
          <Button
            type="button"
            disabled={!canSubmit}
            loading={saveMutation.isPending}
            onClick={() => saveMutation.mutate()}
          >
            {mode === "create" ? "Connect provider" : "Save changes"}
          </Button>
        </Group>
      </Group>

      {!canSubmit && (
        <Text size="xs" c="dimmed" ta="right">
          {testResult?.ok !== true
            ? "You must run a passing connection test before connecting this provider."
            : ""}
        </Text>
      )}
    </Stack>
  );

  return (
    <Stack gap="xl" className="identity-page" w="100%">
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Stack gap={4}>
          <Title order={1}>
            {mode === "create" ? "Connect a provider" : `Edit ${existing?.name ?? "provider"}`}
          </Title>
          <Text c="dimmed" size="sm">
            {mode === "create"
              ? "Configure an LLM provider to power workspace tasks. Test credentials and review model pricing before saving."
              : "Update available models, endpoints, or credentials for this provider."}
          </Text>
        </Stack>
      </Group>

      <ConnectWizard
        active={activeStep}
        onStepChange={setActiveStep}
        steps={[
          { label: "Provider", description: "Choose service", disabled: mode === "edit", content: providerStep },
          { label: "Credentials", description: "API key & plan", content: credentialsStep },
          { label: "Models & Pricing", description: "Select models", content: modelsStep },
          { label: "Validation", description: "Test connection", content: validationStep },
        ]}
      />
    </Stack>
  );
}

export function ProviderFormPage({ mode }: ProviderFormPageProps) {
  const api = useApi();
  const { providerId } = useParams({ strict: false }) as { providerId?: string };

  // Only an edit needs the account. It is read from the shared client, the
  // same source as the list.
  const accounts = useQuery({
    queryKey: providersQueryKey(),
    queryFn: () => api.providers.list(),
    enabled: mode === "edit",
  });

  const existing: ListedProvider | undefined =
    mode === "edit" ? accounts.data?.find((p) => p.id === providerId) : undefined;

  if (mode === "edit" && !existing && accounts.isError) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Provider could not be loaded</Title>
        <Text c="dimmed">{providerProblem(accounts.error)}</Text>
        <div>
          <Button component={Link} to="/providers" variant="default">
            Back to Providers
          </Button>
        </div>
      </Stack>
    );
  }

  if (mode === "edit" && !existing && !accounts.isPending) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Provider not found</Title>
        <Text c="dimmed">The requested provider configuration could not be located.</Text>
        <div>
          <Button component={Link} to="/providers" variant="default">
            Back to Providers
          </Button>
        </div>
      </Stack>
    );
  }

  if (mode === "edit" && !existing) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Loading provider...</Title>
      </Stack>
    );
  }

  if (existing && !canManage(existing)) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>{existing.name}</Title>
        <Text c="dimmed">This provider is shared by the workspace. It is read only here.</Text>
        <div>
          <Button component={Link} to="/providers" variant="default">
            Back to Providers
          </Button>
        </div>
      </Stack>
    );
  }

  return <ProviderForm key={existing?.id ?? "create"} mode={mode} existing={existing} />;
}

export function NewProviderPage() {
  return <ProviderFormPage mode="create" />;
}

export function EditProviderPage() {
  return <ProviderFormPage mode="edit" />;
}
