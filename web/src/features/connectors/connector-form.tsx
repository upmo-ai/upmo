import {
  Badge,
  Button,
  Card,
  Group,
  Loader,
  Radio,
  SimpleGrid,
  Stack,
  Stepper,
  Text,
  TextInput,
  PasswordInput,
  Title,
  UnstyledButton,
} from "@mantine/core";
import {
  IconActivity,
  IconArrowLeft,
  IconCheck,
  IconSearch,
} from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useState } from "react";

import {
  CONNECTORS_KEY,
  connectorsQueryKey,
  useApi,
  type ConnectorGrant,
  type TestResult,
} from "../../app/api";
import { BrandMark } from "../../ui/brand-mark";
import { StatusAlert } from "../../ui/status-alert";
import { KNOWN_CONNECTORS } from "./connectors";

interface ConnectorFormPageProps {
  mode: "create" | "edit";
}

interface ConnectorFormProps {
  mode: "create" | "edit";
  existing?: ConnectorGrant;
  live: boolean;
}

export function ConnectorForm({ mode, existing, live }: ConnectorFormProps) {
  const api = useApi();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const [activeStep, setActiveStep] = useState<number>(mode === "edit" ? 1 : 0);
  const [selectedTool, setSelectedTool] = useState<string>(existing?.connector ?? "github");
  const [toolSearch, setToolSearch] = useState<string>("");

  const filteredTools = KNOWN_CONNECTORS.filter((tool) => {
    if (!toolSearch.trim()) return true;
    const q = toolSearch.toLowerCase().trim();
    return (
      tool.name.toLowerCase().includes(q) ||
      tool.description.toLowerCase().includes(q) ||
      tool.id.toLowerCase().includes(q)
    );
  });

  // Credentials
  const [authToken, setAuthToken] = useState<string>("");
  const [serverEndpoint, setServerEndpoint] = useState<string>(
    selectedTool === "mcp" ? "http://localhost:8000/sse" : "",
  );

  // Scope & Policies
  const [reach, setReach] = useState<"all" | "these">(existing?.resource_scope ?? "all");
  const [resourceIdsInput, setResourceIdsInput] = useState<string>(
    (existing?.resource_ids ?? []).join(", "),
  );
  const [changesPolicy, setChangesPolicy] = useState<"ask" | "allow" | "block">(
    existing?.changes ?? "ask",
  );

  // Validation
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [isTesting, setIsTesting] = useState<boolean>(false);
  const [submitError, setSubmitError] = useState<string>("");

  const toolMeta = (id: string) => {
    return (
      KNOWN_CONNECTORS.find((t) => t.id === id) ?? {
        id,
        name: id,
        description: "External connector",
      }
    );
  };

  const selectedToolInfo = toolMeta(mode === "edit" ? (existing?.connector ?? "") : selectedTool);

  const handleSelectTool = (id: string) => {
    if (mode === "edit") return;
    setSelectedTool(id);
    setAuthToken("");
    setServerEndpoint(id === "mcp" ? "http://localhost:8000/sse" : "");
    setTestResult(null);
  };

  const runTestConnection = async () => {
    setIsTesting(true);
    setSubmitError("");
    try {
      if (live) {
        try {
          const res = await fetch("/api/connectors/test", {
            method: "POST",
            credentials: "include",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              connector: selectedTool,
              token: authToken.trim() || undefined,
              endpoint: serverEndpoint.trim() || undefined,
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

      const result = await api.connectors.test({
        connector: selectedTool,
        token: authToken.trim() || undefined,
        endpoint: serverEndpoint.trim() || undefined,
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
      const ids = resourceIdsInput
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);

      const targetConnector = mode === "edit" ? existing?.connector : selectedTool;
      if (!targetConnector) throw new Error("Missing connector ID");

      const payload: ConnectorGrant = {
        connector: targetConnector,
        resource_scope: reach,
        resource_ids: reach === "these" ? ids : [],
        changes: changesPolicy,
      };

      if (live) {
        const res = await fetch(`/api/connector-grants/${targetConnector}`, {
          method: "PUT",
          credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            resource_scope: payload.resource_scope,
            resource_ids: payload.resource_ids,
            changes: payload.changes,
          }),
        });
        if (!res.ok) throw new Error("Failed to save connector");
        return payload;
      }
      return api.connectors.put(payload);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: CONNECTORS_KEY });
      if (live) {
        await queryClient.invalidateQueries({ queryKey: ["connectors-live-data"] });
      }
      await navigate({ to: "/connectors" });
    },
    onError: (err: Error) => {
      setSubmitError(err.message || "Failed to save connector");
    },
  });

  const canProceedStep1 = mode === "edit" || Boolean(selectedTool);
  const canProceedStep2 = true;
  const canProceedStep3 = reach === "all" || resourceIdsInput.trim().length > 0;
  const canSubmit = testResult?.ok === true && !saveMutation.isPending;

  return (
    <Stack gap="xl" className="identity-page" w="100%">
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Stack gap={4}>
          <Title order={1}>
            {mode === "create" ? "Connect a tool" : `Edit ${selectedToolInfo.name} permissions`}
          </Title>
          <Text c="dimmed" size="sm">
            {mode === "create"
              ? "Link tools, APIs, and MCP servers. Authenticate, define boundaries, and verify the connection."
              : "Control what resources agents can access and how changes are authorized."}
          </Text>
        </Stack>
      </Group>

      {submitError ? (
        <StatusAlert tone="danger" title="Error">
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
          <Stepper.Step label="Tool" description="Select service" disabled={mode === "edit"} />
          <Stepper.Step label="Authentication" description="Credentials & URL" />
          <Stepper.Step label="Permissions" description="Scope & changes" />
          <Stepper.Step label="Validation" description="Test connection" />
        </Stepper>
      </Card>

      {/* STEP 1: Select Tool */}
      {activeStep === 0 && (
        <Card withBorder radius="lg" padding="lg">
          <Stack gap="md">
            <Group justify="space-between" align="center" wrap="wrap" gap="sm">
              <div>
                <Text fw={600} size="sm">
                  Select Tool
                </Text>
                <Text size="xs" c="dimmed">
                  Choose an external tool, integration, or Model Context Protocol endpoint.
                </Text>
              </div>
              <TextInput
                placeholder="Filter tools..."
                leftSection={<IconSearch size={16} aria-hidden="true" />}
                size="sm"
                value={toolSearch}
                onChange={(e) => setToolSearch(e.target.value)}
                style={{ width: 220 }}
              />
            </Group>

            {filteredTools.length === 0 ? (
              <Card withBorder radius="md" padding="md">
                <Text c="dimmed" size="sm" ta="center">
                  No connectors found matching &ldquo;{toolSearch}&rdquo;.
                </Text>
              </Card>
            ) : (
              <SimpleGrid cols={{ base: 2, sm: 4 }} spacing="xs">
                {filteredTools.map((tool) => {
                const isSelected = selectedTool === tool.id;
                return (
                  <UnstyledButton
                    key={tool.id}
                    type="button"
                    onClick={() => handleSelectTool(tool.id)}
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
                    aria-label={tool.name}
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
                    </Group>
                    <BrandMark brand={tool.id} size={36} />
                    <Text size="sm" fw={600} lineClamp={1}>
                      {tool.name}
                    </Text>
                    <Text size="11px" c="dimmed" lineClamp={2}>
                      {tool.description}
                    </Text>
                  </UnstyledButton>
                );
              })}
            </SimpleGrid>
            )}

            <Group justify="space-between" mt="md">
              <Button component={Link} to="/connectors" variant="subtle">
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

      {/* STEP 2: Authentication & Credentials */}
      {activeStep === 1 && (
        <Card withBorder radius="lg" padding="lg">
          <Stack gap="md">
            <Group gap="md">
              <BrandMark brand={selectedToolInfo.id} size={40} />
              <Stack gap={2}>
                <Text fw={600} size="lg">
                  {selectedToolInfo.name} Authentication
                </Text>
                <Text size="xs" c="dimmed">
                  {selectedToolInfo.description}
                </Text>
              </Stack>
            </Group>

            {selectedTool === "mcp" ? (
              <>
                <TextInput
                  label="MCP Server Endpoint URL"
                  required
                  size="md"
                  description="HTTP SSE URL or WebSocket server providing Model Context Protocol tools."
                  placeholder="http://localhost:8000/sse"
                  value={serverEndpoint}
                  onChange={(e) => {
                    setServerEndpoint(e.target.value);
                    setTestResult(null);
                  }}
                />
                <PasswordInput
                  label="Bearer Token (Optional)"
                  size="md"
                  description="Sent in the Authorization header for protected endpoints."
                  placeholder="Bearer token or secret"
                  value={authToken}
                  onChange={(e) => {
                    setAuthToken(e.target.value);
                    setTestResult(null);
                  }}
                />
              </>
            ) : selectedTool === "github" ? (
              <PasswordInput
                label="GitHub Personal Access Token"
                required
                size="md"
                description="Token with repo, read:org, and workflow scopes. Stored encrypted."
                placeholder="ghp_..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "gitlab" ? (
              <>
                <TextInput
                  label="GitLab Host URL (Optional)"
                  size="md"
                  description="Defaults to https://gitlab.com. Enter your self-hosted URL if applicable."
                  placeholder="https://gitlab.example.com"
                  value={serverEndpoint}
                  onChange={(e) => {
                    setServerEndpoint(e.target.value);
                    setTestResult(null);
                  }}
                />
                <PasswordInput
                  label="GitLab Personal Access Token"
                  required
                  size="md"
                  description="Token with api or read_api scopes. Stored encrypted."
                  placeholder="glpat-..."
                  value={authToken}
                  onChange={(e) => {
                    setAuthToken(e.target.value);
                    setTestResult(null);
                  }}
                />
              </>
            ) : selectedTool === "slack" ? (
              <PasswordInput
                label="Slack Bot Token"
                required
                size="md"
                description="OAuth Bot User token starting with xoxb-."
                placeholder="xoxb-..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "discord" ? (
              <PasswordInput
                label="Discord Bot Token"
                required
                size="md"
                description="Bot user token from the Discord Developer Portal."
                placeholder="MTE..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "supabase" ? (
              <>
                <TextInput
                  label="Supabase Project URL"
                  required
                  size="md"
                  description="Your project API URL (e.g. https://xyzcompany.supabase.co)."
                  placeholder="https://your-project.supabase.co"
                  value={serverEndpoint}
                  onChange={(e) => {
                    setServerEndpoint(e.target.value);
                    setTestResult(null);
                  }}
                />
                <PasswordInput
                  label="Supabase Service Role Key or API Key"
                  required
                  size="md"
                  description="Secret service role key for database operations."
                  placeholder="eyJhbGciOi..."
                  value={authToken}
                  onChange={(e) => {
                    setAuthToken(e.target.value);
                    setTestResult(null);
                  }}
                />
              </>
            ) : selectedTool === "stripe" ? (
              <PasswordInput
                label="Stripe Secret API Key"
                required
                size="md"
                description="Restricted or secret key starting with sk_live_ or sk_test_."
                placeholder="sk_live_..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "hubspot" ? (
              <PasswordInput
                label="HubSpot Private App Access Token"
                required
                size="md"
                description="Private app token with CRM scopes starting with pat-."
                placeholder="pat-na1-..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "asana" ? (
              <PasswordInput
                label="Asana Personal Access Token"
                required
                size="md"
                description="Personal access token generated from Asana developer console."
                placeholder="1/120..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "sentry" ? (
              <>
                <TextInput
                  label="Sentry Organization Slug"
                  required
                  size="md"
                  description="The slug identifier of your Sentry organization."
                  placeholder="my-org"
                  value={serverEndpoint}
                  onChange={(e) => {
                    setServerEndpoint(e.target.value);
                    setTestResult(null);
                  }}
                />
                <PasswordInput
                  label="Sentry User Auth Token"
                  required
                  size="md"
                  description="User or custom integration token starting with sntrys_."
                  placeholder="sntrys_..."
                  value={authToken}
                  onChange={(e) => {
                    setAuthToken(e.target.value);
                    setTestResult(null);
                  }}
                />
              </>
            ) : selectedTool === "notion" ? (
              <PasswordInput
                label="Notion Integration Secret"
                required
                size="md"
                description="Internal integration token starting with secret_."
                placeholder="secret_..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "linear" ? (
              <PasswordInput
                label="Linear API Key"
                required
                size="md"
                description="Personal API key starting with lin_api_."
                placeholder="lin_api_..."
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            ) : selectedTool === "jira" ? (
              <>
                <TextInput
                  label="Atlassian Cloud Site URL"
                  required
                  size="md"
                  description="Your Jira cloud workspace URL."
                  placeholder="https://company.atlassian.net"
                  value={serverEndpoint}
                  onChange={(e) => {
                    setServerEndpoint(e.target.value);
                    setTestResult(null);
                  }}
                />
                <PasswordInput
                  label="Atlassian API Token"
                  required
                  size="md"
                  description="API token generated from your Atlassian account security page."
                  placeholder="API token"
                  value={authToken}
                  onChange={(e) => {
                    setAuthToken(e.target.value);
                    setTestResult(null);
                  }}
                />
              </>
            ) : (
              <PasswordInput
                label="OAuth Client Secret or Token"
                size="md"
                description="API key or OAuth credential for service authentication."
                placeholder="API Key / Token"
                value={authToken}
                onChange={(e) => {
                  setAuthToken(e.target.value);
                  setTestResult(null);
                }}
              />
            )}

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
                <Button component={Link} to="/connectors" variant="subtle">
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

      {/* STEP 3: Permissions & Policies */}
      {activeStep === 2 && (
        <Card withBorder radius="lg" padding="lg">
          <Stack gap="md">
            <Title order={3} size="h4">
              Permissions & Scope
            </Title>

            <div>
              <Text fw={600} size="sm" mb={4}>
                Reach
              </Text>
              <Text size="xs" c="dimmed" mb="xs">
                Determine whether agents can access all resources in this tool or only specific items.
              </Text>
              <Radio.Group
                value={reach}
                onChange={(val) => {
                  setReach(val as "all" | "these");
                  setTestResult(null);
                }}
              >
                <Stack gap="xs">
                  <Radio
                    value="all"
                    label="All resources in this tool"
                    description="Agents can read and interact with all items accessible to this token."
                  />
                  <Radio
                    value="these"
                    label="Only these specific resources"
                    description="Restrict access strictly to the IDs or repository names listed below."
                  />
                </Stack>
              </Radio.Group>
            </div>

            {reach === "these" && (
              <TextInput
                label="Resource IDs"
                size="md"
                description="Comma-separated IDs or repository names (e.g. org/repo, #general, team-calendar)."
                placeholder="e.g. upmo/web, #general, team-calendar"
                value={resourceIdsInput}
                onChange={(e) => {
                  setResourceIdsInput(e.target.value);
                  setTestResult(null);
                }}
              />
            )}

            <div>
              <Text fw={600} size="sm" mb={4}>
                Changes policy
              </Text>
              <Text size="xs" c="dimmed" mb="xs">
                How should write or modification operations (e.g. sending messages, creating pull requests) be handled?
              </Text>
              <Radio.Group
                value={changesPolicy}
                onChange={(val) => {
                  setChangesPolicy(val as "ask" | "allow" | "block");
                  setTestResult(null);
                }}
              >
                <Stack gap="xs">
                  <Radio
                    value="ask"
                    label="Ask first"
                    description="Human confirmation is required before mutating any resource."
                  />
                  <Radio
                    value="allow"
                    label="Allow"
                    description="Agents can perform edits directly without requesting manual approval."
                  />
                  <Radio
                    value="block"
                    label="Block"
                    description="Read-only mode. All write, update, or delete operations are rejected."
                  />
                </Stack>
              </Radio.Group>
            </div>

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
                Review Permissions
              </Title>

              <Group justify="space-between" align="center">
                <Group gap="md">
                  <BrandMark brand={selectedToolInfo.id} size={40} />
                  <Stack gap={2}>
                    <Text fw={600} size="md">
                      {selectedToolInfo.name}
                    </Text>
                    <Text size="xs" c="dimmed">
                      {selectedToolInfo.description}
                    </Text>
                  </Stack>
                </Group>
                <Badge variant="light" color={changesPolicy === "block" ? "gray" : "teal"}>
                  Policy: {changesPolicy.toUpperCase()}
                </Badge>
              </Group>

              <Group gap="xs">
                <Badge size="xs" variant="outline">
                  Reach: {reach === "all" ? "All resources" : "Scoped"}
                </Badge>
                {reach === "these" && (
                  <Badge size="xs" variant="default">
                    {resourceIdsInput}
                  </Badge>
                )}
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
                    Verify connectivity and credentials with {selectedToolInfo.name} before saving.
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
                  Click &ldquo;Test Connection&rdquo; to probe the connector and verify authentication.
                </StatusAlert>
              )}

              {isTesting && (
                <StatusAlert tone="info" icon={<Loader size={16} />} title="Testing connection">
                  Pinging connector service and verifying credentials...
                </StatusAlert>
              )}

              {testResult !== null && testResult.ok && (
                <StatusAlert tone="success" title="Connection verified">
                  Handshake verified successfully with {testResult.latency_ms ?? 92} ms latency.
                </StatusAlert>
              )}

              {testResult !== null && !testResult.ok && (
                <StatusAlert tone="danger" title="Connection failed">
                  {testResult.error || "Failed to reach connector service. Check credentials."}
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
              <Button component={Link} to="/connectors" variant="subtle">
                Cancel
              </Button>
              <Button
                type="button"
                disabled={!canSubmit}
                loading={saveMutation.isPending}
                onClick={() => saveMutation.mutate()}
              >
                {mode === "create" ? "Connect tool" : "Save permissions"}
              </Button>
            </Group>
          </Group>

          {!canSubmit && (
            <Text size="xs" c="dimmed" ta="right">
              {testResult?.ok !== true
                ? "You must run a passing connection test before connecting this tool."
                : ""}
            </Text>
          )}
        </Stack>
      )}
    </Stack>
  );
}

export function ConnectorFormPage({ mode }: ConnectorFormPageProps) {
  const api = useApi();
  const { connectorId } = useParams({ strict: false }) as { connectorId?: string };

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

  const live = Array.isArray(probe.data);

  const stubGrants = useQuery({
    queryKey: connectorsQueryKey(),
    queryFn: () => api.connectors.list(),
    enabled: !live,
  });

  const grants = (live ? probe.data : stubGrants.data) ?? [];
  const existing = mode === "edit" ? grants.find((g) => g.connector === connectorId) : undefined;

  if (mode === "edit" && !existing && !stubGrants.isPending && !probe.isPending) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Connector not found</Title>
        <Text c="dimmed">The requested connector configuration could not be located.</Text>
        <div>
          <Button component={Link} to="/connectors" variant="default">
            Back to Connectors
          </Button>
        </div>
      </Stack>
    );
  }

  if (mode === "edit" && !existing) {
    return (
      <Stack gap="md" className="identity-page">
        <Title order={1}>Loading connector...</Title>
      </Stack>
    );
  }

  return (
    <ConnectorForm
      key={existing?.connector ?? "create"}
      mode={mode}
      existing={existing}
      live={live}
    />
  );
}

export function NewConnectorPage() {
  return <ConnectorFormPage mode="create" />;
}

export function EditConnectorPage() {
  return <ConnectorFormPage mode="edit" />;
}
