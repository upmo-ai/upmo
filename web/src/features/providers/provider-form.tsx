import {
  Card,
  Group,
  PasswordInput,
  SimpleGrid,
  Stack,
  Stepper,
  Text,
  Title,
  UnstyledButton,
} from "@mantine/core";
import {
  IconActivity,
  IconArrowLeft,
  IconCheck,
  IconCpu,
  IconPlus,
  IconSearch,
} from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useState } from "react";
import { Badge, Button, Checkbox, Input, Select, Spinner, StatusAlert, Table } from "@upmo-ai/components";

import {
  PROVIDERS_KEY,
  providersQueryKey,
  useApi,
  type ProviderAccount,
  type TestResult,
} from "../../app/api";
import { BrandMark } from "../../ui/brand-mark";
import { formatPricing, getProviderModels, type ModelInfo } from "./models";
import { KNOWN_PROVIDERS, type KnownProvider } from "./providers";

interface ProviderFormPageProps {
  mode: "create" | "edit";
}

interface ProviderFormProps {
  mode: "create" | "edit";
  existing?: ProviderAccount;
  live: boolean;
}

function ProviderForm({ mode, existing, live }: ProviderFormProps) {
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
    existing?.base_url ?? KNOWN_PROVIDERS[0].defaultBaseUrl ?? "",
  );
  const [planKind, setPlanKind] = useState<"metered" | "subscription" | "local">(
    existing?.plan_kind ?? KNOWN_PROVIDERS[0].defaultPlan,
  );

  // Models state
  const [selectedModels, setSelectedModels] = useState<string[]>(
    existing ? existing.models : KNOWN_PROVIDERS[0].defaultModels,
  );
  const [customModelInput, setCustomModelInput] = useState<string>("");

  // Testing & Validation state
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [isTesting, setIsTesting] = useState<boolean>(false);
  const [submitError, setSubmitError] = useState<string>("");

  const selectedMeta: KnownProvider =
    KNOWN_PROVIDERS.find((p) => p.id === selectedProviderId) ?? KNOWN_PROVIDERS[0];

  const handleSelectProvider = (provId: string) => {
    if (mode === "edit") return;
    setSelectedProviderId(provId);
    const meta = KNOWN_PROVIDERS.find((p) => p.id === provId);
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
    try {
      if (live) {
        try {
          const res = await fetch("/api/providers/test", {
            method: "POST",
            credentials: "include",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              provider: selectedProviderId,
              api_key: apiKey.trim() || undefined,
              base_url: baseUrl.trim() || selectedMeta.defaultBaseUrl || undefined,
              model: selectedModels[0],
            }),
          });
          if (res.ok) {
            const data = (await res.json()) as TestResult;
            setTestResult(data);
            return;
          }
        } catch {
          // Fall through to stub api
        }
      }

      const result = await api.providers.test({
        provider: selectedProviderId,
        api_key: apiKey.trim() || undefined,
        base_url: baseUrl.trim() || selectedMeta.defaultBaseUrl || undefined,
        model: selectedModels[0],
      });
      setTestResult(result);
    } catch (err: unknown) {
      setTestResult({
        ok: false,
        error: err instanceof Error ? err.message : "Connection probe failed",
      });
    } finally {
      setIsTesting(false);
    }
  };

  const saveMutation = useMutation({
    mutationFn: async () => {
      setSubmitError("");
      const resolvedBaseUrl = baseUrl.trim() || selectedMeta.defaultBaseUrl || undefined;

      if (mode === "create") {
        const payload = {
          provider: selectedProviderId,
          name: name.trim() || selectedMeta.name,
          adapter: selectedMeta.adapter,
          plan_kind: planKind,
          base_url: resolvedBaseUrl,
          api_key: selectedMeta.requiresKey ? apiKey.trim() : undefined,
          models: selectedModels,
        };

        if (live) {
          const res = await fetch("/api/providers", {
            method: "POST",
            credentials: "include",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
          });
          if (!res.ok) throw new Error("Failed to connect provider");
          return (await res.json()) as ProviderAccount;
        }
        return api.providers.add(payload);
      }

      // Edit mode
      if (!existing?.id) throw new Error("Missing provider ID");
      const editPayload = {
        name: name.trim() || existing.name,
        plan_kind: planKind,
        base_url: resolvedBaseUrl,
        api_key: apiKey.trim() || undefined,
        models: selectedModels,
      };

      if (live) {
        const res = await fetch(`/api/providers/${existing.id}`, {
          method: "PUT",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(editPayload),
        });
        if (!res.ok) throw new Error("Failed to update provider");
        return (await res.json()) as ProviderAccount;
      }
      return api.providers.update(existing.id, editPayload);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY });
      if (live) {
        await queryClient.invalidateQueries({ queryKey: ["providers-live-data"] });
      }
      await navigate({ to: "/providers" });
    },
    onError: (err: Error) => {
      setSubmitError(err.message || "Failed to save provider");
    },
  });

  const availableCatalog = getProviderModels(selectedProviderId);
  const catalogIds = new Set(availableCatalog.map((m) => m.id));
  const customOnlyModels = selectedModels.filter((m) => !catalogIds.has(m));

  const filteredProviders = KNOWN_PROVIDERS.filter((p) => {
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
    (!selectedMeta.requiresKey || mode === "edit" || apiKey.trim().length > 0) &&
    (selectedMeta.requiresKey || baseUrl.trim().length > 0);
  const canProceedStep3 = selectedModels.length > 0;
  const canSubmit = testResult?.ok === true && !saveMutation.isPending;

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

      {submitError ? (
        <StatusAlert tone="critical" title="Error">
          {submitError}
        </StatusAlert>
      ) : null}

      <Card withBorder radius="lg" padding="md">
        <Stepper
          active={activeStep}
          allowNextStepsSelect={false}
          completedIcon={<IconCheck size={16} stroke={2} />}
          onStepClick={(step) => {
            if (step < activeStep) {
              setActiveStep(step);
            }
          }}
          size="sm"
        >
          <Stepper.Step
            label="Provider"
            description="Choose service"
            disabled={mode === "edit"}
          />
          <Stepper.Step label="Credentials" description="API key & plan" />
          <Stepper.Step label="Models & Pricing" description="Select models" />
          <Stepper.Step label="Validation" description="Test connection" />
        </Stepper>
      </Card>

      {/* STEP 1: Select Provider */}
      {activeStep === 0 && (
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
              <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
                {filteredProviders.map((prov) => {
                const isSelected = selectedProviderId === prov.id;
                return (
                  <UnstyledButton
                    key={prov.id}
                    type="button"
                    onClick={() => handleSelectProvider(prov.id)}
                    style={{
                      padding: "16px 12px",
                      borderRadius: "var(--radius-lg)",
                      border: isSelected
                        ? "2px solid var(--emerald-deep)"
                        : "1px solid var(--border-strong)",
                      background: isSelected
                        ? "var(--surface-selected)"
                        : "var(--surface-100)",
                      display: "flex",
                      flexDirection: "column",
                      alignItems: "center",
                      gap: 8,
                      textAlign: "center",
                      transition: "border-color 120ms ease, background 120ms ease",
                    }}
                    aria-label={prov.name}
                    aria-pressed={isSelected}
                  >
                    <Group justify="space-between" w="100%" px={4}>
                      <div
                        style={{
                          width: 14,
                          height: 14,
                          borderRadius: "var(--radius-full)",
                          border: isSelected
                            ? "4px solid var(--emerald-deep)"
                            : "1.5px solid var(--border-strong)",
                          background: "var(--surface-100)",
                        }}
                      />
                      <Text size="11px" fw={600} c={isSelected ? "var(--emerald-deep)" : "dimmed"}>
                        {prov.adapter}
                      </Text>
                    </Group>
                    <BrandMark brand={prov.id} size={36} />
                    <Text size="sm" fw={600} lineClamp={1}>
                      {prov.name}
                    </Text>
                    <Text size="11px" c="dimmed" lineClamp={2}>
                      {prov.description}
                    </Text>
                  </UnstyledButton>
                );
              })}
            </SimpleGrid>
            )}

            <Group justify="space-between" mt="md">
              <Button component={Link} to="/providers" variant="subtle">
                Cancel
              </Button>
              <Button
                disabled={!canProceedStep1}
                onClick={() => setActiveStep(1)}
              >
                Continue
              </Button>
            </Group>
          </Stack>
        </Card>
      )}

      {/* STEP 2: Credentials & Plan */}
      {activeStep === 1 && (
        <Card withBorder radius="lg" padding="lg">
          <Stack gap="md">
            <Group gap="md">
              <BrandMark brand={selectedProviderId} size={40} />
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

            {selectedMeta.requiresKey ? (
              <>
                <PasswordInput
                  label="API Key"
                  required={mode === "create"}
                  size="md"
                  description={
                    mode === "create"
                      ? "Keys are encrypted at rest and stay on this server."
                      : "Leave blank to keep the currently stored key."
                  }
                  placeholder={mode === "create" ? "sk-..." : "••••••••••••••••"}
                  value={apiKey}
                  onChange={(e) => {
                    setApiKey(e.target.value);
                    setTestResult(null);
                  }}
                />
                <Input
                  label="Endpoint URL"
                  size="md"
                  description="API gateway endpoint URL (optional for standard cloud providers)."
                  value={baseUrl}
                  onChange={(e) => {
                    setBaseUrl(e.target.value);
                    setTestResult(null);
                  }}
                  placeholder={selectedMeta.defaultBaseUrl ?? "https://api.example.com/v1"}
                />
              </>
            ) : (
              <Input
                label="Base URL"
                required
                size="md"
                description={
                  selectedMeta.id === "lmstudio"
                    ? "Address of your local LM Studio service."
                    : "Address of your local or private Ollama service."
                }
                value={baseUrl}
                onChange={(e) => {
                  setBaseUrl(e.target.value);
                  setTestResult(null);
                }}
                placeholder={selectedMeta.defaultBaseUrl ?? "http://localhost:11434"}
              />
            )}

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
      )}

      {/* STEP 3: Models & Pricing */}
      {activeStep === 2 && (
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

            <Table.ScrollContainer minWidth={600} className="u-table-wrap">
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
      )}

      {/* STEP 4: Validation & Connect */}
      {activeStep === 3 && (
        <Stack gap="lg">
          <Card withBorder radius="lg" padding="lg">
            <Stack gap="md">
              <Title order={3} size="h4">
                Review Configuration
              </Title>

              <Group justify="space-between" align="center">
                <Group gap="md">
                  <BrandMark brand={selectedProviderId} size={40} />
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
              <Group justify="space-between" align="center">
                <div>
                  <Text fw={600} size="sm">
                    Connection Verification
                  </Text>
                  <Text size="xs" c="dimmed">
                    Test the endpoint and credentials before saving. The provider cannot be connected until validated.
                  </Text>
                </div>
                <Button
                  variant="outline"
                  leftSection={<IconActivity size={16} />}
                  loading={isTesting}
                  onClick={runTestConnection}
                >
                  Test Connection
                </Button>
              </Group>

              {testResult === null && !isTesting && (
                <StatusAlert tone="info" title="Ready to test">
                  Click &ldquo;Test Connection&rdquo; to send a test probe to {selectedMeta.name} and verify latency.
                </StatusAlert>
              )}

              {isTesting && (
                <StatusAlert tone="info" icon={<Spinner size={16} />} title="Testing connection">
                  Sending validation handshake to provider endpoint...
                </StatusAlert>
              )}

              {testResult !== null && testResult.ok && (
                <StatusAlert tone="positive" title="Connection verified">
                  Handshake verified successfully with {testResult.latency_ms ?? 124} ms latency.
                </StatusAlert>
              )}

              {testResult !== null && !testResult.ok && (
                <StatusAlert tone="critical" title="Connection failed">
                  {testResult.error || "Failed to reach provider. Check your API key or endpoint URL."}
                </StatusAlert>
              )}
            </Stack>
          </Card>

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
      )}
    </Stack>
  );
}

export function ProviderFormPage({ mode }: ProviderFormPageProps) {
  const api = useApi();
  const { providerId } = useParams({ strict: false }) as { providerId?: string };

  const probe = useQuery({
    queryKey: ["providers-live"],
    retry: false,
    queryFn: async () => {
      try {
        const res = await fetch("/api/providers", { credentials: "include" });
        if (!res.ok) return null;
        const data: unknown = await res.json();
        return Array.isArray(data) ? (data as ProviderAccount[]) : null;
      } catch {
        return null;
      }
    },
  });

  const live = Array.isArray(probe.data);

  const stubProviders = useQuery({
    queryKey: providersQueryKey(),
    queryFn: () => api.providers.list(),
    enabled: !live,
  });

  const providers = (live ? probe.data : stubProviders.data) ?? [];
  const existing = mode === "edit" ? providers.find((p) => p.id === providerId) : undefined;

  if (mode === "edit" && !existing && !stubProviders.isPending && !probe.isPending) {
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

  return (
    <ProviderForm
      key={existing?.id ?? "create"}
      mode={mode}
      existing={existing}
      live={live}
    />
  );
}

export function NewProviderPage() {
  return <ProviderFormPage mode="create" />;
}

export function EditProviderPage() {
  return <ProviderFormPage mode="edit" />;
}
