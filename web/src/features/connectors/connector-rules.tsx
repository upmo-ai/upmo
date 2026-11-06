import { Group, Stack, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { Badge, Button, Dialog, SegmentedControl, StatusAlert } from "@upmo-ai/components";

import {
  BrainError,
  CONNECTORS_KEY,
  connectorRulesQueryKey,
  useApi,
  useWrite,
  type ConnectorRules,
  type ConnectorRulesInput,
  type RulePolicy,
} from "../../app/api";
import { RowAction } from "../../ui/icons";

const policyLabel: Record<RulePolicy, string> = {
  allow: "Allow",
  ask: "Ask first",
  block: "Block",
};

const policyColor: Record<RulePolicy, string> = {
  allow: "teal",
  ask: "yellow",
  block: "red",
};

const policyChoices = (Object.keys(policyLabel) as RulePolicy[]).map((value) => ({
  value,
  label: policyLabel[value],
}));

/** The action classes a person can set. Delete is fixed and shown apart. */
const editableClasses = [
  {
    key: "read",
    label: "Read",
    hint: "Looking things up, such as opening files, listing issues or reading messages.",
  },
  {
    key: "change",
    label: "Change",
    hint: "Creating or editing, such as sending messages, opening pull requests or updating records.",
  },
] as const;

function isPolicy(value: string): value is RulePolicy {
  return value === "allow" || value === "ask" || value === "block";
}

export const rulesNotSaved = "Rules could not be saved. Nothing changed. Try again.";
export const rulesRefused = "You are not allowed to change these rules. Nothing was saved.";

/**
 * A refused save is explained on the page. It never ends the session. A
 * rejected rule (400 or 422) keeps the reason the server gave.
 */
export function saveProblem(error: Error): string {
  if (error instanceof BrainError) {
    if (error.status === 401 || error.status === 403) return rulesRefused;
    if ((error.status === 400 || error.status === 422) && error.message !== "" && !/^HTTP \d+$/.test(error.message)) {
      return error.message;
    }
    return rulesNotSaved;
  }
  return error.message === "" ? rulesNotSaved : error.message;
}

/**
 * The current rule for each action class of one connector, with an icon button
 * that opens the editor. Rules come from the API only; nothing is assumed
 * while they load or when they cannot be read.
 */
export function ConnectorRulesRow({
  connector,
  name,
  readOnly = false,
}: {
  connector: string;
  name: string;
  /** A global connector: its rules are shown with a Global marker and no editor. */
  readOnly?: boolean;
}) {
  const api = useApi();
  const [editing, setEditing] = useState(false);
  const rules = useQuery({
    queryKey: connectorRulesQueryKey(connector),
    queryFn: () => api.connectors.rules.get(connector),
  });

  if (rules.isPending) {
    return (
      <Text size="xs" c="dimmed" role="status">
        Loading rules
      </Text>
    );
  }
  if (rules.isError) {
    return (
      <Text size="xs" c="dimmed">
        Rules are not available.
      </Text>
    );
  }

  const current = rules.data;
  return (
    <>
      <Group justify="space-between" align="center" wrap="nowrap" gap="xs">
        <Group gap={6} wrap="wrap" role="group" aria-label={`${name} rules`}>
          <RuleBadge label="Read" policy={current.read} />
          <RuleBadge label="Change" policy={current.change} />
          <RuleBadge label="Delete" policy={current.delete} />
          {readOnly ? (
            <Badge variant="outline" size="sm" color="gray">
              Global
            </Badge>
          ) : null}
        </Group>
        {readOnly ? null : (
          <RowAction
            label={`Edit rules for ${name}`}
            concept="action.edit"
            onClick={() => setEditing(true)}
          />
        )}
      </Group>
      {readOnly ? (
        <Text size="xs" c="dimmed">
          Global connector. The workspace manages its rules, so they are read only.
        </Text>
      ) : null}
      {editing && !readOnly ? (
        <Dialog opened onClose={() => setEditing(false)} title={`${name} rules`}>
          <RulesForm
            connector={connector}
            name={name}
            saved={current}
            onClose={() => setEditing(false)}
          />
        </Dialog>
      ) : null}
    </>
  );
}

function RuleBadge({ label, policy }: { label: string; policy: RulePolicy }) {
  return (
    <Badge variant="light" size="sm" color={policyColor[policy]}>
      {`${label}: ${policyLabel[policy]}`}
    </Badge>
  );
}

function RulesForm({
  connector,
  name,
  saved,
  onClose,
}: {
  connector: string;
  name: string;
  saved: ConnectorRules;
  onClose: () => void;
}) {
  const api = useApi();
  const hintId = useId();
  const [draft, setDraft] = useState<ConnectorRulesInput>({ read: saved.read, change: saved.change });
  const [failure, setFailure] = useState("");

  const save = useWrite(
    (input: ConnectorRulesInput) => api.connectors.rules.put(connector, input),
    // The whole connectors tree, so the card's change policy and the rules
    // are read again together.
    [CONNECTORS_KEY],
    {
      shown: true,
      onSuccess: onClose,
      onError: (cause) => setFailure(saveProblem(cause)),
    },
  );

  const dirty = draft.read !== saved.read || draft.change !== saved.change;

  return (
    <form
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        if (!dirty || save.isPending) return;
        setFailure("");
        save.mutate(draft);
      }}
    >
      <Stack gap="md">
        <Text size="sm" c="dimmed">
          Choose what happens when an agent uses {name}. These rules apply to each action class.
        </Text>

        {editableClasses.map((item) => (
          <Stack key={item.key} gap={6}>
            <Text fw={600} size="sm">
              {item.label}
            </Text>
            <Text size="xs" c="dimmed" id={`${hintId}-${item.key}`}>
              {item.hint}
            </Text>
            <SegmentedControl
              aria-label={`${item.label} rule`}
              aria-describedby={`${hintId}-${item.key}`}
              fullWidth
              data={policyChoices}
              value={draft[item.key]}
              disabled={save.isPending}
              onChange={(value) => {
                if (!isPolicy(value)) return;
                setFailure("");
                setDraft({ ...draft, [item.key]: value });
              }}
            />
          </Stack>
        ))}

        <Stack gap={6}>
          <Group gap="xs" align="center">
            <Text fw={600} size="sm">
              Delete
            </Text>
            <Badge variant="light" size="sm" color={policyColor[saved.delete]}>
              {policyLabel[saved.delete]}
            </Badge>
          </Group>
          <Text size="xs" c="dimmed">
            Deleting always asks first. This rule is fixed and cannot be changed.
          </Text>
        </Stack>

        {failure ? <StatusAlert tone="critical">{failure}</StatusAlert> : null}

        <Group gap="xs">
          <Button type="submit" disabled={!dirty} loading={save.isPending}>
            Save rules
          </Button>
          <Button type="button" variant="default" onClick={onClose}>
            Cancel
          </Button>
        </Group>
      </Stack>
    </form>
  );
}
