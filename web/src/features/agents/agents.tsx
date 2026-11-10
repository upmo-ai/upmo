import { Card, Group, SimpleGrid, Stack, Text, ThemeIcon, Title } from "@mantine/core";
import { IconPlus, IconRobot, IconSearch } from "@tabler/icons-react";
import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Badge, Button, ConfirmDialog, Dialog, Input, Select, StatusAlert, Switch, Textarea } from "@upmo-ai/components";

import {
  AGENTS_KEY,
  agentsQueryKey,
  providersQueryKey,
  useApi,
  useWrite,
  type AgentProfile,
} from "../../app/api";
import {
  agentProblem,
  changesOf,
  choiceOf,
  choiceValue,
  descriptionProblem,
  draftOf,
  draftProblem,
  emptyDraft,
  modelChoices,
  nameMax,
  nameProblem,
  overlayMax,
  overlayProblem,
  routeProblem,
  trimmedDraft,
  type AgentDraft,
} from "./rules";

function matches(agent: AgentProfile, search: string): boolean {
  const q = search.toLowerCase().trim();
  if (q === "") return true;
  return [agent.name, agent.description, agent.route, agent.model_provider, agent.model_id].some((field) =>
    field.toLowerCase().includes(q),
  );
}

export function AgentsPage() {
  const api = useApi();

  // The list is read from the shared client on every mount, never seeded with a
  // snapshot, as the providers page does.
  const agentsQuery = useQuery({ queryKey: agentsQueryKey(), queryFn: () => api.agents.list() });
  const agents = agentsQuery.data ?? [];

  const [search, setSearch] = useState("");
  // null: no dialog; "new": the create dialog; an agent: its edit dialog.
  const [editing, setEditing] = useState<AgentProfile | "new" | null>(null);
  const [removing, setRemoving] = useState<AgentProfile | null>(null);
  const [removeError, setRemoveError] = useState("");

  const remove = useWrite((id: string) => api.agents.remove(id), [AGENTS_KEY], {
    shown: true,
    onSuccess: () => setRemoving(null),
    onError: (cause) => setRemoveError(agentProblem(cause)),
  });

  const askRemove = (agent: AgentProfile | null) => {
    setRemoveError("");
    setRemoving(agent);
  };

  const shown = agents.filter((agent) => matches(agent, search));

  return (
    <Stack gap="lg" className="identity-page">
      <Group justify="space-between" align="center" wrap="wrap" gap="md">
        <Stack gap={4}>
          <Title order={1}>Agents</Title>
          <Text c="dimmed" size="sm">
            An agent is a saved profile you pick when you chat: instructions, and optionally a route and a model.
          </Text>
        </Stack>
        <Group gap="sm" wrap="wrap">
          {agents.length > 0 && (
            <Input
              placeholder="Search agents..."
              aria-label="Search agents"
              leftSection={<IconSearch size={16} aria-hidden="true" />}
              size="sm"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              style={{ width: 220 }}
            />
          )}
          <Button leftSection={<IconPlus size={16} aria-hidden="true" />} onClick={() => setEditing("new")}>
            New agent
          </Button>
        </Group>
      </Group>

      {agentsQuery.isPending ? (
        <Text c="dimmed" size="sm" role="status">
          Loading agents
        </Text>
      ) : agentsQuery.isError ? (
        <StatusAlert tone="critical" title="Agents could not be loaded">
          {agentProblem(agentsQuery.error)}
        </StatusAlert>
      ) : agents.length === 0 ? (
        <Card withBorder radius="lg" padding="xl" style={{ borderStyle: "dashed" }}>
          <Stack align="center" gap="sm" py={48}>
            <ThemeIcon variant="light" size={48} radius="lg" color="teal">
              <IconRobot size={24} aria-hidden="true" />
            </ThemeIcon>
            <Title order={3}>Add an agent</Title>
            <Text c="dimmed" size="sm" maw={360} ta="center">
              Give a way of working a name, then pick it in the chat composer.
            </Text>
            <Button mt="xs" leftSection={<IconPlus size={16} aria-hidden="true" />} onClick={() => setEditing("new")}>
              New agent
            </Button>
          </Stack>
        </Card>
      ) : shown.length === 0 ? (
        <Card withBorder radius="lg" padding="xl">
          <Text c="dimmed" ta="center">
            No agents found matching &ldquo;{search}&rdquo;.
          </Text>
        </Card>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }} spacing="md">
          {shown.map((agent) => (
            <AgentCard
              key={agent.id}
              agent={agent}
              onEdit={() => setEditing(agent)}
              onDelete={() => askRemove(agent)}
            />
          ))}
        </SimpleGrid>
      )}

      {editing ? (
        <AgentDialog agent={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} />
      ) : null}

      <ConfirmDialog
        opened={Boolean(removing)}
        title={removing ? `Delete ${removing.name}` : "Delete agent"}
        body="Chats that used this agent keep their replies. A reply asked for with it again will fail."
        action="Delete agent"
        destructive
        busy={remove.isPending}
        onClose={() => askRemove(null)}
        onConfirm={() => {
          if (removing) remove.mutate(removing.id);
        }}
      >
        {removeError ? (
          <StatusAlert tone="critical" title="Could not delete">
            {removeError}
          </StatusAlert>
        ) : null}
      </ConfirmDialog>
    </Stack>
  );
}

function AgentCard({
  agent,
  onEdit,
  onDelete,
}: {
  agent: AgentProfile;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const shared = agent.scope === "global";
  const model = agent.model_id ? `${agent.model_provider} / ${agent.model_id}` : "";
  return (
    <Card withBorder radius="lg" padding="md" data-testid={`agent-card-${agent.id}`}>
      <Stack gap="xs" h="100%">
        <Group justify="space-between" align="flex-start" wrap="nowrap" gap="xs">
          <Title order={3} size="h4">
            {agent.name}
          </Title>
          <Group gap={4} wrap="nowrap">
            {shared ? (
              <Badge variant="light" size="sm">
                Shared
              </Badge>
            ) : null}
            {agent.enabled ? null : (
              <Badge variant="light" size="sm">
                Off
              </Badge>
            )}
          </Group>
        </Group>
        {agent.description ? (
          <Text size="sm" c="dimmed">
            {agent.description}
          </Text>
        ) : null}
        {agent.route ? <Text size="xs">Route: {agent.route}</Text> : null}
        {model ? <Text size="xs">Model: {model}</Text> : null}
        {shared && !agent.editable ? (
          <Text size="xs" c="dimmed">
            Managed by your workspace. Read only.
          </Text>
        ) : null}
        {agent.editable ? (
          <Group gap="xs" mt="auto">
            <Button size="xs" variant="default" onClick={onEdit} aria-label={`Edit ${agent.name}`}>
              Edit
            </Button>
            <Button size="xs" variant="subtle" onClick={onDelete} aria-label={`Delete ${agent.name}`}>
              Delete
            </Button>
          </Group>
        ) : null}
      </Stack>
    </Card>
  );
}

function AgentDialog({ agent, onClose }: { agent?: AgentProfile; onClose: () => void }) {
  const api = useApi();
  const [draft, setDraft] = useState<AgentDraft>(agent ? draftOf(agent) : emptyDraft());
  const [touched, setTouched] = useState(false);
  const [failure, setFailure] = useState("");

  // The models the person's own and the workspace's accounts list. A model the
  // agent already has stays offered even when no account lists it any more.
  const providers = useQuery({ queryKey: providersQueryKey(), queryFn: () => api.providers.list() });
  const choices = modelChoices(providers.data ?? []);
  if (draft.model_id && !choices.some((c) => c.provider === draft.model_provider && c.model === draft.model_id)) {
    choices.push({ provider: draft.model_provider, model: draft.model_id });
  }
  const groups = [...new Set(choices.map((choice) => choice.provider))].map((provider) => ({
    group: provider,
    items: choices
      .filter((choice) => choice.provider === provider)
      .map((choice) => ({ value: choiceValue(choice), label: choice.model })),
  }));

  const save = useWrite(
    () => (agent ? api.agents.update(agent.id, changesOf(agent, draft)) : api.agents.create(trimmedDraft(draft))),
    [AGENTS_KEY],
    {
      shown: true,
      onSuccess: onClose,
      onError: (cause) => setFailure(agentProblem(cause)),
    },
  );

  const set = (patch: Partial<AgentDraft>) => {
    setDraft((current) => ({ ...current, ...patch }));
    setFailure("");
  };

  const problem = draftProblem(draft);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (problem || save.isPending) return;
    setFailure("");
    save.mutate();
  };

  const nameError = nameProblem(draft.name);
  const showNameError = nameError !== null && (touched || draft.name.length > 0);
  const modelValue = draft.model_id ? choiceValue({ provider: draft.model_provider, model: draft.model_id }) : null;

  return (
    <Dialog opened onClose={onClose} title={agent ? `Edit ${agent.name}` : "New agent"} withCloseButton={false}>
      <form onSubmit={submit} noValidate>
        <Stack gap="md">
          <Input
            label="Name"
            description={`1 to ${nameMax} characters. Unique among your agents.`}
            required
            data-autofocus
            value={draft.name}
            error={showNameError ? nameError : undefined}
            onChange={(event) => set({ name: event.currentTarget.value })}
          />
          <Input
            label="Description"
            description="Optional. Shown on the agent's card."
            value={draft.description}
            error={descriptionProblem(draft.description) ?? undefined}
            onChange={(event) => set({ description: event.currentTarget.value })}
          />
          <Textarea
            label="Instructions"
            description={`Added to the system text of every reply. ${draft.prompt_overlay.length} of ${overlayMax} characters`}
            minRows={4}
            value={draft.prompt_overlay}
            error={overlayProblem(draft.prompt_overlay) ?? undefined}
            onChange={(event) => set({ prompt_overlay: event.currentTarget.value })}
          />
          <Input
            label="Route"
            description="Optional. The chain of models used when you pick none in the composer. Empty uses the default."
            placeholder="chat"
            value={draft.route}
            error={routeProblem(draft.route) ?? undefined}
            onChange={(event) => set({ route: event.currentTarget.value })}
          />
          <Select
            label="Model"
            description="Optional. Tried first, on your own account of the provider or else the workspace's."
            placeholder="None, use the route"
            clearable
            searchable
            data={groups}
            value={modelValue}
            onChange={(value) => {
              const choice = value ? choiceOf(value) : null;
              set({ model_provider: choice?.provider ?? "", model_id: choice?.model ?? "" });
            }}
          />
          <Switch
            label="Available to pick"
            checked={draft.enabled}
            onChange={(event) => set({ enabled: event.currentTarget.checked })}
          />
          {failure ? (
            <Text role="alert" c="red" size="sm">
              {failure}
            </Text>
          ) : null}
          <Group gap="xs">
            <Button type="submit" disabled={save.isPending || problem !== null}>
              {agent ? "Save agent" : "Create agent"}
            </Button>
            <Button type="button" variant="default" onClick={onClose}>
              Cancel
            </Button>
          </Group>
        </Stack>
      </form>
    </Dialog>
  );
}
