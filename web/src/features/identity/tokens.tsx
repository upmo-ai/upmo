import { Group, Stack, Text } from "@mantine/core";
import { DateInput } from "@mantine/dates";
import { IconCalendar } from "@tabler/icons-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button, Checkbox, ConfirmDialog, Dialog, Input, PageHeader } from "@upmo-ai/components";

import {
  TOKENS_KEY,
  meQueryKey,
  membersQueryKey,
  tokensQueryKey,
  useApi,
  useWrite,
  type ApiToken,
} from "../../app/api";
import { fresh } from "../../app/passkeys";
import { formatDate } from "./format";
import { actionsForRole, scopeActions, scopeObjects } from "./rules";
import type { ScopeAction, ScopeObject } from "./types";

import "./identity.css";

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

function pad(value: number): string {
  return String(value).padStart(2, "0");
}

function formatDay(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function shiftDays(days: number): string {
  const date = new Date();
  date.setDate(date.getDate() + days);
  return formatDay(date);
}

const monthNames = ["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"];

function parseExpiry(input: string): string | null {
  const text = input.trim();
  const lower = text.toLowerCase();
  if (lower === "today") return shiftDays(0);
  if (lower === "tomorrow") return shiftDays(1);
  if (/^\d{4}-\d{2}-\d{2}$/.test(text)) return text;
  const named = /^(\d{1,2})\s+([a-z]+)(?:\s+(\d{4}))?$/i.exec(text);
  if (!named) return null;
  const month = monthNames.findIndex((name) => named[2].toLowerCase().startsWith(name));
  if (month < 0) return null;
  const year = named[3] ? Number(named[3]) : new Date().getFullYear();
  const day = Number(named[1]);
  const date = new Date(year, month, day);
  if (date.getFullYear() !== year || date.getMonth() !== month || date.getDate() !== day) return null;
  return formatDay(date);
}

const localTimeZone = new Intl.DateTimeFormat().resolvedOptions().timeZone;

function ExpiryField({ value, onChange }: { value: string; onChange: (day: string) => void }) {
  return (
    <DateInput
      label="Expiry"
      description={localTimeZone}
      placeholder="15 Jan 2027"
      value={value || null}
      valueFormat="D MMM YYYY"
      minDate={shiftDays(0)}
      maxDate={shiftDays(365)}
      dateParser={parseExpiry}
      popoverProps={{ shadow: "sm", radius: "lg" }}
      rightSection={<IconCalendar size={20} stroke={1.75} />}
      rightSectionPointerEvents="none"
      onChange={(next) => onChange(next ?? "")}
    />
  );
}

function daysUntil(day: string): number {
  const [year, month, date] = day.split("-").map(Number);
  if (!year || !month || !date) return -1;
  const chosen = new Date(year, month - 1, date);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  chosen.setHours(0, 0, 0, 0);
  return Math.round((chosen.getTime() - today.getTime()) / 86_400_000);
}

export function TokensPage() {
  const probe = useQuery({
    queryKey: ["tokens-live"],
    retry: false,
    queryFn: async () => {
      try {
        const response = await fetch("/api/tokens", { credentials: "include" });
        if (!response.ok) return null;
        const body: unknown = await response.json();
        return Array.isArray(body) ? (body as LiveToken[]) : null;
      } catch {
        return null;
      }
    },
  });
  if (probe.isPending) {
    return (
      <Stack gap="lg" className="settings-content">
        <PageHeader title="API tokens" />
      </Stack>
    );
  }
  if (Array.isArray(probe.data)) return <LiveTokens />;
  return <StubTokens />;
}

function StubTokens() {
  const api = useApi();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const members = useQuery({
    queryKey: membersQueryKey(),
    queryFn: () => api.members.list(),
  });
  const tokens = useQuery({ queryKey: tokensQueryKey(), queryFn: () => api.tokens.list() });
  const [opened, setOpened] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [revokeId, setRevokeId] = useState<string | null>(null);

  const revoke = useWrite((id: string) => api.tokens.revoke(id), [TOKENS_KEY], {
    onSuccess: async () => {
      setRevokeId(null);
    },
  });

  const actor = members.data?.find((member) => member.id === me.data?.memberId);
  const actions = actor ? actionsForRole(actor.role) : [];
  const list = tokens.data ?? [];
  const revoking = list.find((token) => token.id === revokeId) ?? null;

  const close = () => {
    setOpened(false);
    setSecret(null);
  };

  return (
    <Stack gap="lg" className="settings-content">
      <PageHeader title="API tokens" />
      {tokens.isError ? (
        <Text role="alert" c="red">
          {tokens.error?.message ?? "Failed to load tokens."}
        </Text>
      ) : null}
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
        }}
      />
      <ConfirmDialog
        opened={revoking !== null}
        title={revoking ? `Revoke the ${revoking.name}?` : "Revoke this token?"}
        body="You will lose this token."
        action="Revoke token"
        destructive
        busy={revoke.isPending}
        onClose={() => setRevokeId(null)}
        onConfirm={() => {
          if (!revokeId) return;
          revoke.mutate(revokeId);
        }}
      />
    </Stack>
  );
}

function TokenRow({ token, onRevoke }: { token: ApiToken; onRevoke: () => void }) {
  return (
    <li className="record">
      <Text fw={600}>{token.name}</Text>
      <Text className="token-hint" data-testid="token-hint">
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
  const [expiry, setExpiry] = useState(shiftDays(90));
  const [scopes, setScopes] = useState<string[]>([]);
  const [error, setError] = useState("");

  const reset = () => {
    setName("");
    setExpiry(shiftDays(90));
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

  const create = useWrite(
    () => api.tokens.create({ name, scopes, days: daysUntil(expiry) }),
    [TOKENS_KEY],
    {
      shown: true,
      onSuccess: async (created) => {
        reset();
        await onCreated(created.secret);
      },
      onError: (cause: Error) => setError(cause.message),
    },
  );

  const offered = scopeActions.filter((action) => actions.includes(action));
  if (!opened) return null;

  return (
    <Dialog opened onClose={close} title={secret ? "Token created" : "Create token"} withCloseButton={false}>
      {secret ? (
        <Stack gap="md">
          <Text className="token-secret" data-testid="token-secret">
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
          <Input label="Name" value={name} onChange={(event) => setName(event.currentTarget.value)} />
          <ExpiryField value={expiry} onChange={setExpiry} />
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
              disabled={create.isPending || name.trim().length === 0 || scopes.length === 0 || daysUntil(expiry) < 0}
              onClick={() => create.mutate()}
            >
              Create token
            </Button>
            <Button type="button" variant="default" onClick={close}>
              Cancel
            </Button>
          </Group>
        </Stack>
      )}
    </Dialog>
  );
}

type LiveToken = {
  id: string;
  name: string;
  scope: string[] | null;
  created_at: string;
  expires_at: string;
  last_used_at: string | null;
};

type LiveRole = { id: string; permissions?: string[] };
type LiveMember = { id: string; role: { id: string } };

function LiveTokens() {
  const client = useQueryClient();
  const tokens = useQuery({
    queryKey: ["tokens-live-list"],
    queryFn: () => tokenJSON<LiveToken[]>("/api/tokens"),
  });
  const session = useQuery({
    queryKey: ["tokens-session"],
    queryFn: () => tokenJSON<{ user_id: string }>("/api/session"),
  });
  const members = useQuery({
    queryKey: ["tokens-members"],
    queryFn: () => tokenJSON<LiveMember[]>("/api/members"),
  });
  const roles = useQuery({
    queryKey: ["tokens-roles"],
    queryFn: () => tokenJSON<LiveRole[]>("/api/roles"),
  });
  const [opened, setOpened] = useState(false);
  const [secret, setSecret] = useState<string | null>(null);
  const [revokeId, setRevokeId] = useState<string | null>(null);
  const list = tokens.data ?? [];
  const revoking = list.find((token) => token.id === revokeId) ?? null;
  const actor = members.data?.find((member) => member.id === session.data?.user_id);
  const held = roles.data?.find((role) => role.id === actor?.role.id)?.permissions ?? [];
  const offered = held.filter((name) => !name.endsWith(":delete") && !name.endsWith(":manage"));

  const revoke = useMutation({
    mutationFn: async (id: string) => {
      const response = await fetch(`/api/tokens/${id}`, { method: "DELETE", credentials: "include" });
      if (!response.ok) throw new Error("Try again.");
    },
    onSuccess: async () => {
      setRevokeId(null);
      await client.invalidateQueries({ queryKey: ["tokens-live-list"] });
    },
  });

  return (
    <Stack gap="lg" className="settings-content">
      <PageHeader title="API tokens" />
      {tokens.isError ? (
        <Text role="alert" c="red">
          {tokens.error instanceof Error ? tokens.error.message : "Failed to load tokens."}
        </Text>
      ) : null}
      <div>
        <Button type="button" onClick={() => setOpened(true)}>
          Create token
        </Button>
      </div>
      {tokens.data && list.length === 0 ? <Text>No API tokens yet.</Text> : null}
      {list.length > 0 ? (
        <ul className="record-list" aria-label="API tokens">
          {list.map((token) => (
            <li key={token.id} className="record">
              <Text fw={600}>{token.name}</Text>
              <Text size="sm">{(token.scope ?? []).join(", ")}</Text>
              <Text size="sm">Created {formatDate(token.created_at)}</Text>
              <Text size="sm">Last used {token.last_used_at ? formatDate(token.last_used_at) : "Not yet"}</Text>
              <Text size="sm">Expires {formatDate(token.expires_at)}</Text>
              <Button type="button" variant="default" onClick={() => setRevokeId(token.id)}>
                Revoke {token.name}
              </Button>
            </li>
          ))}
        </ul>
      ) : null}
      <LiveCreate
        opened={opened}
        secret={secret}
        offered={offered}
        onClose={() => {
          setOpened(false);
          setSecret(null);
        }}
        onCreated={(value) => setSecret(value)}
      />
      <ConfirmDialog
        opened={revoking !== null}
        title={revoking ? `Revoke the ${revoking.name}?` : "Revoke this token?"}
        body="You will lose this token."
        action="Revoke token"
        destructive
        busy={revoke.isPending}
        onClose={() => setRevokeId(null)}
        onConfirm={() => {
          if (!revokeId) return;
          revoke.mutate(revokeId);
        }}
      />
    </Stack>
  );
}

function LiveCreate({
  opened,
  secret,
  offered,
  onClose,
  onCreated,
}: {
  opened: boolean;
  secret: string | null;
  offered: string[];
  onClose: () => void;
  onCreated: (secret: string) => void;
}) {
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [expiry, setExpiry] = useState(shiftDays(90));
  const [scopes, setScopes] = useState<string[]>([]);
  const [error, setError] = useState("");
  const create = useMutation({
    mutationFn: () =>
      tokenWrite("/api/tokens", "POST", { name, scope: scopes, expires: expiry }) as Promise<{ secret?: string }>,
    onSuccess: async (created) => {
      setName("");
      setExpiry(shiftDays(90));
      setScopes([]);
      setError("");
      onCreated(created.secret ?? "");
      await client.invalidateQueries({ queryKey: ["tokens-live-list"] });
    },
    onError: (cause: Error) => {
      if (cause.message !== "canceled") setError(cause.message);
    },
  });
  if (!opened) return null;
  return (
    <Dialog opened onClose={onClose} title={secret ? "Token created" : "Create token"} withCloseButton={false}>
      {secret ? (
        <Stack gap="md">
          <Text className="token-secret" data-testid="token-secret">
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
            <Button type="button" variant="default" onClick={onClose}>
              Close
            </Button>
          </Group>
        </Stack>
      ) : (
        <Stack gap="md">
          <Input label="Name" value={name} onChange={(event) => setName(event.currentTarget.value)} />
          <ExpiryField value={expiry} onChange={setExpiry} />
          <div className="scope-list">
            {scopeObjects.map((object) => {
              const actions = scopeActions.filter((action) => offered.includes(`${object}:${action}`));
              if (actions.length === 0) return null;
              return (
                <fieldset key={object} className="scope-object">
                  <legend>{objectLabel[object]}</legend>
                  <div className="scope-actions">
                    {actions.map((action) => {
                      const scope = `${object}:${action}`;
                      return (
                        <Checkbox
                          key={scope}
                          label={`${objectLabel[object]} ${actionLabel[action]}`}
                          checked={scopes.includes(scope)}
                          onChange={() =>
                            setScopes((current) =>
                              current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope],
                            )
                          }
                        />
                      );
                    })}
                  </div>
                </fieldset>
              );
            })}
          </div>
          {error ? <Text size="sm">{error}</Text> : null}
          <Button
            type="button"
            disabled={create.isPending || name.trim().length === 0 || scopes.length === 0 || expiry === ""}
            onClick={() => create.mutate()}
          >
            Create token
          </Button>
        </Stack>
      )}
    </Dialog>
  );
}

async function tokenJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: "include" });
  if (!response.ok) throw new Error("Failed to load tokens.");
  return (await response.json()) as T;
}

async function tokenWrite(path: string, method: string, body: unknown): Promise<unknown> {
  const send = async () => {
    const response = await fetch(path, {
      method,
      credentials: "include",
      headers: body ? { "content-type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    const text = await response.text();
    let payload: { error?: string; secret?: string } = {};
    if (text) {
      try {
        payload = JSON.parse(text) as { error?: string; secret?: string };
      } catch {
        payload = {};
      }
    }
    if (response.status === 403 && payload.error === "sign in again") {
      throw { status: 403, error: "sign in again" };
    }
    if (!response.ok && response.status !== 204) throw new Error(payload.error || "Try again.");
    return payload;
  };
  const result = await fresh(send);
  if (result === undefined) throw new Error("canceled");
  return result;
}
