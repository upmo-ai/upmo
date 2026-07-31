import { Button, Checkbox, Group, Modal, NativeSelect, Stack, Text, TextInput, Title } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { meQueryKey, membersQueryKey, tokensQueryKey, useApi, type ApiToken, type TokenExpiry } from "../../app/api";
import { ConfirmDialog } from "./confirm";
import { formatDate } from "./format";
import { actionsForRole, scopeActions, scopeObjects } from "./rules";
import type { ScopeAction, ScopeObject } from "./types";

const objectLabel: Record<ScopeObject, string> = {
  task: "Task",
  chat: "Chat",
  folder: "Folder",
  automation: "Automation",
  agent: "Agent",
};

const actionLabel: Record<ScopeAction, string> = {
  create: "create",
  view: "view",
  edit: "edit",
  share: "share",
};

const expiryChoices: { value: TokenExpiry; label: string }[] = [
  { value: 30, label: "30 days" },
  { value: 90, label: "90 days" },
  { value: 365, label: "365 days" },
];

function isExpiry(value: string): value is `${TokenExpiry}` {
  return value === "30" || value === "90" || value === "365";
}

export function TokensPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const members = useQuery({
    queryKey: membersQueryKey(),
    queryFn: () => api.members.list(),
  });
  const tokens = useQuery({ queryKey: tokensQueryKey(), queryFn: () => api.tokens.list() });
  const [opened, setOpened] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [revokeId, setRevokeId] = useState<string | null>(null);

  const actor = members.data?.find((member) => member.id === me.data?.memberId);
  const actions = actor ? actionsForRole(actor.role) : [];
  const list = tokens.data ?? [];
  const revoking = list.find((token) => token.id === revokeId) ?? null;

  const close = () => {
    setOpened(false);
    setSecret(null);
  };

  return (
    <Stack gap="lg" className="settings-page">
      <Title order={1}>API tokens</Title>
      {actor ? (
        <div>
          <Button type="button" onClick={() => setOpened(true)}>
            Create token
          </Button>
        </div>
      ) : null}
      {tokens.data && list.length === 0 ? <Text>No API tokens yet.</Text> : null}
      {list.length > 0 ? (
        <ul className="record-list" aria-label="API tokens">
          {list.map((token) => (
            <TokenRow key={token.id} token={token} onRevoke={() => setRevokeId(token.id)} />
          ))}
        </ul>
      ) : null}
      <CreateTokenDialog
        opened={opened}
        secret={secret}
        actions={actions}
        onClose={close}
        onCreated={async (created) => {
          setSecret(created);
          await queryClient.invalidateQueries({ queryKey: tokensQueryKey() });
        }}
      />
      <ConfirmDialog
        opened={revoking !== null}
        title={revoking ? `Revoke the ${revoking.name}?` : "Revoke this token?"}
        body="You will lose this token."
        action="Revoke token"
        onClose={() => setRevokeId(null)}
        onConfirm={() => {
          if (!revokeId) return;
          void api.tokens.revoke(revokeId).then(async () => {
            setRevokeId(null);
            await queryClient.invalidateQueries({ queryKey: tokensQueryKey() });
          });
        }}
      />
    </Stack>
  );
}

function TokenRow({ token, onRevoke }: { token: ApiToken; onRevoke: () => void }) {
  return (
    <li className="record">
      <Text fw={600}>{token.name}</Text>
      <Text className="token-hint code-sm" data-testid="token-hint">
        {token.hint}
      </Text>
      <Text size="sm">{token.scopes.join(", ")}</Text>
      <Text size="sm">Created {formatDate(token.created)}</Text>
      <Text size="sm">Last used {token.lastUsed ? formatDate(token.lastUsed) : "Not yet"}</Text>
      <Text size="sm">Expires {formatDate(token.expires)}</Text>
      <Button type="button" variant="default" onClick={onRevoke}>
        Revoke {token.name}
      </Button>
    </li>
  );
}

function CreateTokenDialog({
  opened,
  secret,
  actions,
  onClose,
  onCreated,
}: {
  opened: boolean;
  secret: string | null;
  actions: readonly ScopeAction[];
  onClose: () => void;
  onCreated: (secret: string) => Promise<void>;
}) {
  const api = useApi();
  const [name, setName] = useState("");
  const [days, setDays] = useState<TokenExpiry>(30);
  const [scopes, setScopes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const reset = () => {
    setName("");
    setDays(30);
    setScopes([]);
    setError("");
  };

  const close = () => {
    reset();
    onClose();
  };

  const toggle = (scope: string) => {
    setScopes((current) =>
      current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope],
    );
  };

  const create = () => {
    setBusy(true);
    setError("");
    void api.tokens
      .create({ name, scopes, days })
      .then(async (created) => {
        reset();
        await onCreated(created.secret);
      })
      .catch((cause: Error) => setError(cause.message))
      .finally(() => setBusy(false));
  };

  const offered = scopeActions.filter((action) => actions.includes(action));
  if (!opened) return null;

  return (
    <Modal opened onClose={close} title={secret ? "Token created" : "Create token"} withCloseButton={false}>
      {secret ? (
        <Stack gap="md">
          <Text className="token-secret code-sm" data-testid="token-secret">
            {secret}
          </Text>
          <Text>You will not see this token again</Text>
          <Group gap="xs">
            <Button
              type="button"
              onClick={() => {
                void navigator.clipboard?.writeText(secret).catch(() => undefined);
              }}
            >
              Copy
            </Button>
            <Button type="button" variant="default" onClick={close}>
              Close
            </Button>
          </Group>
        </Stack>
      ) : (
        <Stack gap="md">
          <TextInput label="Name" value={name} onChange={(event) => setName(event.currentTarget.value)} />
          <NativeSelect
            label="Expiry"
            value={String(days)}
            data={expiryChoices.map((item) => ({ value: String(item.value), label: item.label }))}
            onChange={(event) => {
              const next = event.currentTarget.value;
              if (isExpiry(next)) setDays(Number(next) as TokenExpiry);
            }}
          />
          <div className="scope-list">
            {scopeObjects.map((object) => (
              <fieldset key={object} className="scope-object">
                <legend>{objectLabel[object]}</legend>
                <div className="scope-actions">
                  {offered.map((action) => {
                    const scope = `${object}:${action}`;
                    const label = `${objectLabel[object]} ${actionLabel[action]}`;
                    return (
                      <Checkbox
                        key={scope}
                        label={label}
                        checked={scopes.includes(scope)}
                        onChange={() => toggle(scope)}
                      />
                    );
                  })}
                </div>
              </fieldset>
            ))}
          </div>
          {error ? <Text size="sm">{error}</Text> : null}
          <Group gap="xs">
            <Button
              type="button"
              disabled={busy || name.trim().length === 0 || scopes.length === 0}
              onClick={create}
            >
              Create token
            </Button>
            <Button type="button" variant="default" onClick={close}>
              Cancel
            </Button>
          </Group>
        </Stack>
      )}
    </Modal>
  );
}
