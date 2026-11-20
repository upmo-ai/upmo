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
  StatusAlert,
  Table,
  TestConnectionPanel,
  type TestConnectionState,
} from "@upmo-ai/components";

import {
  PROVIDERS_KEY,
  providerCatalogQueryKey,
  providerModelsQueryKey,
  providersQueryKey,
  providerVendorsQueryKey,
  teamsQueryKey,
  useApi,
  type ProviderAdd,
  type ProviderChange,
  type ProviderPlanKind,
  type SeenModel,
  type TestResult,
} from "../../app/api";
import { formatDate } from "../identity/format";
import { formatRates, listedPrice, sourceLabel } from "./models";
import { baseUrlRequired, hostedBaseUrl, keyLine, keyRequired, providerProblem, showsKeyField } from "./rules";
import { canManage, fetchCanShare, type ListedProvider } from "./sharing";
import { pickerVendors, planOptions, type PickerVendor } from "./vendors";

interface ProviderFormPageProps {
  mode: "create" | "edit";
}

interface ProviderFormProps {
  mode: "create" | "edit";
  vendors: PickerVendor[];
  existing?: ListedProvider;
}

/** Who an account is for: the person alone, named teams, or the whole workspace. */
type ShareAsk = "private" | "team" | "everyone";

const LIST_PATH = "/settings/providers";

function sameTeamIds(left: readonly string[], right: readonly string[]): boolean {
  if (left.length !== right.length) return false;
  const a = [...left].sort();
  const b = [...right].sort();
  return a.every((id, index) => id === b[index]);
}

/** True when the sharing choice is not the account's current visibility, or the team list changed. */
function shareChoiceChanged(existing: ListedProvider, scope: ShareAsk, teams: readonly string[]): boolean {
  if (scope !== existing.visibility) return true;
  return scope === "team" && !sameTeamIds(teams, existing.team_ids);
}

function baseUrlHelp(meta: PickerVendor): string {
  if (meta.adapter === "fake") return "Optional for the built in test provider.";
  if (meta.suggestedPlan === "local") return `Required. Address of your local or private ${meta.name} service.`;
  const fallback = hostedBaseUrl(meta.defaultBaseUrl);
  if (fallback) {
    return `Optional. Empty uses ${fallback}. Change it only for a proxy or a private deployment, and give the address without the chat path.`;
  }
  return "Required. Address of the provider's API as its documentation gives it, for example https://api.example.com, without the chat path.";
}

/** A saved account whose vendor the brain no longer offers still opens, under its own name. */
function vendorOf(vendors: readonly PickerVendor[], existing: ListedProvider | undefined, id: string): PickerVendor {
  const known = vendors.find((vendor) => vendor.id === id);
  if (known) return known;
  return {
    id,
    name: existing?.name ?? id,
    adapter: existing?.adapter ?? "",
    description: "",
    suggestedPlan: existing?.plan_kind ?? "metered",
    requiresKey: existing?.plan_kind !== "local",
    defaultBaseUrl: existing?.base_url,
  };
}

/** The models a list offers: the ones the provider named, then any selected one it did not. */
function modelRows(discovered: readonly SeenModel[], selected: readonly string[]): { seen: SeenModel; listed: boolean }[] {
  const ids = new Set(discovered.map((model) => model.id));
  return [
    ...discovered.map((seen) => ({ seen, listed: true })),
    ...selected.filter((id) => !ids.has(id)).map((id) => ({ seen: { id }, listed: false })),
  ];
}

function ProviderForm({ mode, vendors, existing }: ProviderFormProps) {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const first = vendors[0];
  const [activeStep, setActiveStep] = useState<number>(mode === "edit" ? 1 : 0);
  const [providerSearch, setProviderSearch] = useState<string>("");

  const [selectedProviderId, setSelectedProviderId] = useState<string>(existing?.provider ?? first?.id ?? "");
  const [name, setName] = useState<string>(existing?.name ?? first?.name ?? "");
  const [apiKey, setApiKey] = useState<string>("");
  const [baseUrl, setBaseUrl] = useState<string>(
    existing ? (existing.base_url ?? "") : (first?.defaultBaseUrl ?? ""),
  );
  const [planKind, setPlanKind] = useState<ProviderPlanKind>(
    existing?.plan_kind ?? first?.suggestedPlan ?? "metered",
  );

  const [selectedModels, setSelectedModels] = useState<string[]>(existing ? existing.models : []);
  const [modelSearch, setModelSearch] = useState<string>("");
  const [customModelInput, setCustomModelInput] = useState<string>("");

  // Sharing: who may use the account. Only a person who may share is asked.
  const initialShare: ShareAsk = existing?.visibility === "team" || existing?.visibility === "everyone" ? existing.visibility : "private";
  const [shareScope, setShareScope] = useState<ShareAsk>(initialShare);
  const [shareTeams, setShareTeams] = useState<string[]>(existing?.team_ids ?? []);
  const canShareQuery = useQuery({
    queryKey: [...PROVIDERS_KEY, "sharing"],
    queryFn: fetchCanShare,
  });
  // The choice is offered to a person who may share, when adding an account
  // and when changing a private or a shared one.
  const offerSharing = canShareQuery.data === true;
  const teamsQuery = useQuery({
    queryKey: teamsQueryKey(),
    queryFn: () => api.shares.teams(),
    enabled: offerSharing,
  });
  const teamList = teamsQuery.data ?? [];
  const sharedWithTeams = offerSharing && shareScope === "team";

  // Testing & Validation state
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [isTesting, setIsTesting] = useState<boolean>(false);
  const [submitError, setSubmitError] = useState<string>("");

  const selectedMeta = vendorOf(vendors, existing, selectedProviderId);
  // The brain keeps a key for every account that is not local, so only a
  // saved local account may have none.
  const hasSavedKey = existing !== undefined && existing.plan_kind !== "local";
  const showKey = showsKeyField(selectedMeta.requiresKey, planKind);
  const needsKey = keyRequired(planKind, hasSavedKey);
  const needsBaseUrl = baseUrlRequired(selectedMeta.adapter, selectedMeta.defaultBaseUrl);
  // An empty field sends the provider's hosted address, if it has one, so the
  // brain never has to guess where an account lives.
  const sentBaseUrl = baseUrl.trim() || hostedBaseUrl(selectedMeta.defaultBaseUrl);
  // A key typed while the field was shown is not sent once it is hidden.
  const typedKey = showKey ? apiKey.trim() : "";
  const savedKeyLine = existing ? keyLine(existing, formatDate) : null;

  // What the provider listed: the last test's answer, or for a saved account
  // the list its last saved test stored. Prices come from the provider's own
  // list first, then from models.dev for the account's pricing vendor and model mapping.
  const storedModels = useQuery({
    queryKey: providerModelsQueryKey(existing?.id ?? ""),
    queryFn: () => api.providers.models(existing?.id ?? ""),
    enabled: existing !== undefined,
  });
  const catalogQuery = useQuery({
    queryKey: providerCatalogQueryKey(existing?.pricing_provider || selectedProviderId),
    queryFn: () => api.providers.catalog(existing?.pricing_provider || selectedProviderId),
    enabled: (existing?.pricing_provider || selectedProviderId) !== "" && selectedMeta.adapter !== "fake",
  });
  const discovered = testResult?.ok ? (testResult.models ?? []) : (storedModels.data ?? []);
  const catalog = catalogQuery.data ?? [];

  const resetTest = () => setTestResult(null);

  const handleSelectProvider = (provId: string) => {
    if (mode === "edit") return;
    setSelectedProviderId(provId);
    const meta = vendors.find((p) => p.id === provId);
    if (meta) {
      setName(meta.name);
      setPlanKind(meta.suggestedPlan);
      setBaseUrl(meta.defaultBaseUrl ?? "");
      setSelectedModels([]);
      setApiKey("");
      resetTest();
    }
  };

  const handleToggleModel = (modelId: string) => {
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
        base_url: sentBaseUrl,
        model: selectedModels[0],
        id: savedId,
      });
      setTestResult(result);
      // The brain stores the status and the model list a saved account's test found.
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

      if (mode === "create") {
        const input: ProviderAdd = {
          provider: selectedProviderId,
          name: name.trim() || selectedMeta.name,
          adapter: selectedMeta.adapter,
          plan_kind: planKind,
          base_url: sentBaseUrl,
          api_key: typedKey || undefined,
          models: selectedModels,
        };
        if (offerSharing && shareScope !== "private") {
          input.visibility = shareScope;
          if (shareScope === "team") input.team_ids = shareTeams;
        }
        const made = await api.providers.add(input);
        // A new account starts Not connected. Testing it once saved, with its
        // saved key, stores its status and model list, so its models reach
        // the pickers. The form's own test already passed, so a failure here
        // only leaves the account as the brain saved it.
        await api.providers.test({ provider: made.provider, id: made.id }).catch(() => undefined);
        return made;
      }

      // An empty key field keeps the saved key. An emptied base URL is sent
      // as "", so the brain clears the saved address.
      if (!existing?.id) throw new Error("Missing provider ID");
      const change: ProviderChange = {
        name: name.trim() || existing.name,
        plan_kind: planKind,
        base_url: sentBaseUrl ?? "",
        api_key: typedKey || undefined,
        models: selectedModels,
      };
      if (offerSharing && shareChoiceChanged(existing, shareScope, shareTeams)) {
        change.visibility = shareScope;
        if (shareScope === "team") change.team_ids = shareTeams;
      }
      return api.providers.update(existing.id, change);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: PROVIDERS_KEY });
      await navigate({ to: LIST_PATH });
    },
    onError: (err: Error) => {
      setSubmitError(providerProblem(err));
    },
  });

  const filteredProviders = vendors.filter((p) => {
    if (!providerSearch.trim()) return true;
    const q = providerSearch.toLowerCase();
    return p.name.toLowerCase().includes(q) || p.id.toLowerCase().includes(q) || p.description.toLowerCase().includes(q);
  });

  const rows = modelRows(discovered, selectedModels);
  const modelQuery = modelSearch.trim().toLowerCase();
  const shownRows = rows.filter(
    ({ seen }) =>
      !modelQuery ||
      seen.id.toLowerCase().includes(modelQuery) ||
      (seen.display_name ?? "").toLowerCase().includes(modelQuery),
  );

  // Validation checks for advancing steps
  const canProceedStep1 = mode === "edit" || Boolean(selectedProviderId);
  const canProceedStep2 =
    name.trim().length > 0 &&
    (!needsKey || typedKey.length > 0) &&
    (!needsBaseUrl || baseUrl.trim().length > 0) &&
    (!sharedWithTeams || shareTeams.length > 0);
  const tested = testResult?.ok === true;
  const canSubmit = tested && selectedModels.length > 0 && !saveMutation.isPending;

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
    shareScope === "everyone"
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
          <Button component={Link} to={LIST_PATH} variant="subtle">
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
          onChange={(e) => setName(e.target.value)}
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
                ? `Leave blank to keep the currently stored key.${savedKeyLine ? ` ${savedKeyLine}.` : ""}`
                : needsKey
                  ? "Keys are encrypted at rest and stay on this server."
                  : "Optional on the local plan. Keys are encrypted at rest and stay on this server."
            }
            placeholder={hasSavedKey ? "••••••••••••••••" : "sk-..."}
            value={apiKey}
            onChange={(e) => {
              setApiKey(e.target.value);
              resetTest();
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
            resetTest();
          }}
          placeholder={selectedMeta.defaultBaseUrl ?? "https://api.example.com"}
        />

        <Radio.Group
          label="Plan kind"
          description="Controls rate limits, cost accounting, and scheduling."
          value={planKind}
          onChange={(value) => {
            setPlanKind(value as ProviderPlanKind);
            resetTest();
          }}
        >
          <Stack gap="xs" mt="xs">
            {planOptions.map((option) => (
              <Radio
                key={option.value}
                value={option.value}
                label={option.value === selectedMeta.suggestedPlan ? `${option.label} (suggested)` : option.label}
                description={option.line}
              />
            ))}
          </Stack>
        </Radio.Group>

        {offerSharing ? (
          <Stack gap="xs">
            <Text fw={500} size="sm">
              Who can use this provider
            </Text>
            <Radio.Group value={shareScope} onChange={(value) => setShareScope(value as ShareAsk)}>
              <Stack gap="xs">
                {mode === "create" || existing?.is_owner ? <Radio value="private" label="Only me" /> : null}
                <Radio
                  value="team"
                  label="Teams"
                  description="Only members of the teams you choose can use it. Nobody sees the key."
                />
                <Radio
                  value="everyone"
                  label="Workspace"
                  description="Everyone can use it. Only admins change it. Nobody sees the key."
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
            <Button component={Link} to={LIST_PATH} variant="subtle">
              Cancel
            </Button>
          )}
          <Button disabled={!canProceedStep2} onClick={() => setActiveStep(2)}>
            Continue
          </Button>
        </Group>
      </Stack>
    </Card>
  );

  const testStep = (
    <Card withBorder radius="lg" padding="lg">
      <Stack gap="md">
        <div>
          <Text fw={600} size="sm">
            Connection Verification
          </Text>
          <Text size="xs" c="dimmed">
            Test the endpoint and credentials. A passing test lists the provider&rsquo;s models to choose from.
          </Text>
        </div>

        {sentBaseUrl ? (
          <Text size="xs" c="dimmed">
            Endpoint: <Text span ff="monospace">{sentBaseUrl}</Text>
          </Text>
        ) : null}

        <TestConnectionPanel
          state={testState}
          onTest={runTestConnection}
          latencyMs={testResult?.latency_ms}
          error={testResult?.ok === false ? testErrorMessage : undefined}
        />

        {tested ? (
          <Text size="sm" role="status">
            {discovered.length === 0
              ? "The provider listed no models. Add the ones you use by id."
              : `The provider listed ${discovered.length} ${discovered.length === 1 ? "model" : "models"}.`}
          </Text>
        ) : null}

        <Group justify="space-between" mt="md">
          <Button
            variant="subtle"
            leftSection={<IconArrowLeft size={16} />}
            onClick={() => setActiveStep(1)}
          >
            Back
          </Button>
          <Button disabled={!tested} onClick={() => setActiveStep(3)}>
            Continue
          </Button>
        </Group>

        {!tested ? (
          <Text size="xs" c="dimmed" ta="right">
            You must run a passing connection test before choosing models.
          </Text>
        ) : null}
      </Stack>
    </Card>
  );

  const modelsStep = (
    <Stack gap="lg">
      <Card withBorder radius="lg" padding="lg">
        <Stack gap="md">
          <Group justify="space-between" align="center" wrap="wrap" gap="sm">
            <div>
              <Text fw={600} size="sm">
                Models & Pricing for {selectedMeta.name}
              </Text>
              <Text size="xs" c="dimmed">
                Tick the models to enable. Token pricing is per 1M tokens (MTok), with where it came from.
              </Text>
            </div>
            <Input
              placeholder="Search models..."
              aria-label="Search models"
              leftSection={<IconSearch size={16} aria-hidden="true" />}
              size="sm"
              value={modelSearch}
              onChange={(e) => setModelSearch(e.target.value)}
              style={{ width: 220 }}
            />
          </Group>

          <Table.ScrollContainer minWidth={600}>
            <Table verticalSpacing="xs" striped highlightOnHover>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th style={{ width: 48 }}>Active</Table.Th>
                  <Table.Th>Model</Table.Th>
                  <Table.Th>Token Pricing</Table.Th>
                  <Table.Th>Source</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {shownRows.map(({ seen, listed }) => {
                  const isChecked = selectedModels.includes(seen.id);
                  const price = listedPrice(seen, catalog, existing?.pricing_models?.[seen.id] || seen.id);
                  return (
                    <Table.Tr key={seen.id} onClick={() => handleToggleModel(seen.id)} style={{ cursor: "pointer" }}>
                      <Table.Td onClick={(e) => e.stopPropagation()}>
                        <Checkbox
                          checked={isChecked}
                          onChange={() => handleToggleModel(seen.id)}
                          aria-label={`Select ${seen.id}`}
                        />
                      </Table.Td>
                      <Table.Td>
                        <Stack gap={2}>
                          <Text fw={600} size="sm">
                            {seen.display_name ?? seen.id}
                          </Text>
                          <Text size="xs" c="dimmed" ff="monospace">
                            {seen.id}
                          </Text>
                          {!listed ? (
                            <Badge size="xs" variant="outline">
                              Not listed by the provider
                            </Badge>
                          ) : null}
                        </Stack>
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs" fw={500}>
                          {formatRates(price.input, price.output)}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Badge size="xs" variant="light">
                          {sourceLabel(price.source)}
                        </Badge>
                      </Table.Td>
                    </Table.Tr>
                  );
                })}
                {shownRows.length === 0 ? (
                  <Table.Tr>
                    <Table.Td colSpan={4}>
                      <Text size="sm" c="dimmed" ta="center">
                        {rows.length === 0 ? "No models yet." : `No models match “${modelSearch}”.`}
                      </Text>
                    </Table.Td>
                  </Table.Tr>
                ) : null}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>

          <Group align="flex-end" gap="xs">
            <Input
              label="Add Custom Model Identifier"
              size="md"
              description="Type any model ID the provider serves but did not list."
              placeholder="e.g. my-org/fine-tuned-model"
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
            <Button variant="default" size="md" leftSection={<IconPlus size={16} />} onClick={handleAddCustomModel}>
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
        </Stack>
      </Card>

      <Card withBorder radius="lg" padding="lg">
        <Stack gap="xs">
          <Title order={3} size="h4">
            Review Configuration
          </Title>
          <Text size="xs" c="dimmed">
            {name} · {selectedMeta.name} · Adapter: {selectedMeta.adapter} · Plan: {planKind}
          </Text>
          {offerSharing && shareScope !== "private" ? (
            <Text size="xs" c="dimmed">
              Shared with: {sharedWithText}
            </Text>
          ) : null}
        </Stack>
      </Card>

      {submitError ? (
        <StatusAlert tone="critical" title="Could not save the provider">
          {submitError}
        </StatusAlert>
      ) : null}

      <Group justify="space-between" mt="md">
        <Button variant="subtle" leftSection={<IconArrowLeft size={16} />} onClick={() => setActiveStep(2)}>
          Back
        </Button>
        <Group gap="xs">
          <Button component={Link} to={LIST_PATH} variant="subtle">
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
              ? "Configure an LLM provider to power workspace tasks. Test credentials, then choose its models."
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
          { label: "Test", description: "Test connection", content: testStep },
          { label: "Models & Pricing", description: "Select models", content: modelsStep },
        ]}
      />
    </Stack>
  );
}

function BackToProviders() {
  return (
    <div>
      <Button component={Link} to={LIST_PATH} variant="default">
        Back to Providers
      </Button>
    </div>
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
  const vendorList = useQuery({
    queryKey: providerVendorsQueryKey(),
    queryFn: () => api.providers.vendors(),
  });

  const existing: ListedProvider | undefined =
    mode === "edit" ? accounts.data?.find((p) => p.id === providerId) : undefined;

  if (mode === "edit" && !existing && accounts.isError) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Provider could not be loaded</Title>
        <Text c="dimmed">{providerProblem(accounts.error)}</Text>
        <BackToProviders />
      </Stack>
    );
  }

  if (mode === "edit" && !existing && !accounts.isPending) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Provider not found</Title>
        <Text c="dimmed">The requested provider configuration could not be located.</Text>
        <BackToProviders />
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
        <BackToProviders />
      </Stack>
    );
  }

  if (vendorList.isError) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Providers could not be loaded</Title>
        <Text c="dimmed">{providerProblem(vendorList.error)}</Text>
        <BackToProviders />
      </Stack>
    );
  }

  if (vendorList.isPending) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>{mode === "create" ? "Connect a provider" : `Edit ${existing?.name ?? "provider"}`}</Title>
        <Text c="dimmed" size="sm" role="status">
          Loading providers
        </Text>
      </Stack>
    );
  }

  const vendors = pickerVendors(vendorList.data, import.meta.env.DEV);
  return <ProviderForm key={existing?.id ?? "create"} mode={mode} vendors={vendors} existing={existing} />;
}

export function NewProviderPage() {
  return <ProviderFormPage mode="create" />;
}

export function EditProviderPage() {
  return <ProviderFormPage mode="edit" />;
}
