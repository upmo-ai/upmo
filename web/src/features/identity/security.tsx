import { Button, Group, Stack, Text, TextInput, Title } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { meQueryKey, passkeysQueryKey, useApi, type Passkey } from "../../app/api";
import { addPasskey, confirmWithPasskey, removePasskey, renamePasskey } from "../../app/passkeys";
import { ConfirmDialog } from "./confirm";
import { formatDate } from "./format";
import { canRemovePasskey, canRemovePassword, lastPasskeyReason, lastPasswordReason } from "./rules";

type PasswordMode = "set" | "change" | "remove";

const passwordCopy: Record<PasswordMode, { title: string; body: string; action: string }> = {
  set: {
    title: "Set a password?",
    body: "This replaces logging in with a passkey only. You can still use a passkey.",
    action: "Set password",
  },
  change: {
    title: "Replace your current password?",
    body: "The current password is replaced and stops working.",
    action: "Change password",
  },
  remove: {
    title: "Remove your password?",
    body: "You will lose the ability to log in with a password.",
    action: "Remove password",
  },
};

export function SecurityPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const passkeys = useQuery({ queryKey: passkeysQueryKey(), queryFn: () => api.passkeys.list() });
  const livePasskeys = useQuery({
    queryKey: ["live-passkeys"],
    queryFn: () => liveJSON<LivePasskey[]>("/api/passkeys"),
  });
  const liveSessions = useQuery({
    queryKey: ["live-sessions"],
    queryFn: () => liveJSON<LiveSession[]>("/api/sessions"),
  });
  const [adding, setAdding] = useState(false);
  const [addName, setAddName] = useState("Passkey");
  const [renameId, setRenameId] = useState<string | null>(null);
  const [renameName, setRenameName] = useState("");
  const [removeId, setRemoveId] = useState<string | null>(null);
  const liveAccount = useQuery({
    queryKey: ["live-session"],
    queryFn: () => liveObject<{ has_password?: boolean }>("/api/session"),
  });
  const [passwordMode, setPasswordMode] = useState<PasswordMode | null>(null);
  const [passwordDraft, setPasswordDraft] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [confirmDraft, setConfirmDraft] = useState("");
  const [notice, setNotice] = useState("");
  const live = livePasskeys.data != null;

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: passkeysQueryKey() });
    await queryClient.invalidateQueries({ queryKey: meQueryKey() });
  };

  const add = useMutation({
    mutationFn: async () => {
      if (live) {
        await addPasskey();
        return;
      }
      await api.passkeys.add(addName);
    },
    onSuccess: async () => {
      setAdding(false);
      setAddName("Passkey");
      setNotice("");
      await refresh();
      await queryClient.invalidateQueries({ queryKey: ["live-passkeys"] });
      await queryClient.invalidateQueries({ queryKey: ["live-sessions"] });
    },
    onError: () => setNotice("The passkey prompt was cancelled. Try again."),
  });
  const rename = useMutation({
    mutationFn: async () => {
      if (!renameId) throw new Error("Passkey missing.");
      if (live) {
        await renamePasskey(renameId, renameName);
        return;
      }
      await api.passkeys.rename(renameId, renameName);
    },
    onSuccess: async () => {
      setRenameId(null);
      await refresh();
      await queryClient.invalidateQueries({ queryKey: ["live-passkeys"] });
    },
  });
  const remove = useMutation({
    mutationFn: async () => {
      if (!removeId) throw new Error("Passkey missing.");
      if (live) {
        await removePasskey(removeId);
        return;
      }
      await api.passkeys.remove(removeId);
    },
    onSuccess: async () => {
      setRemoveId(null);
      await refresh();
      await queryClient.invalidateQueries({ queryKey: ["live-passkeys"] });
      await queryClient.invalidateQueries({ queryKey: ["live-sessions"] });
    },
  });
  const list = livePasskeys.data
    ? livePasskeys.data.map((item) => ({
        id: item.id,
        name: item.name,
        added: item.added,
        lastUsed: item.last_used ?? item.added,
      }))
    : (passkeys.data ?? []);
  const savePassword = useMutation({
    mutationFn: async () => {
      if (liveAccount.data) return saveLivePassword(passwordMode, passwordDraft, list.length);
      if (passwordMode === "set") return api.password.set();
      if (passwordMode === "change") return api.password.change();
      return api.password.remove();
    },
    onSuccess: async () => {
      setPasswordMode(null);
      setPasswordDraft("");
      setConfirming(false);
      setConfirmDraft("");
      setNotice("");
      await refresh();
      await queryClient.invalidateQueries({ queryKey: ["live-session"] });
    },
    onError: (err) => {
      if (err instanceof ConfirmPassword) {
        setConfirming(true);
        return;
      }
      setNotice(err instanceof Error ? err.message : "Try again.");
    },
  });

  const passwordSet = liveAccount.data
    ? Boolean(liveAccount.data.has_password)
    : (me.data?.passwordSet ?? false);
  const allowPasskeyRemoval = canRemovePasskey(list.length, passwordSet);
  const allowPasswordRemoval = canRemovePassword(list.length, passwordSet);
  const removing = list.find((item) => item.id === removeId) ?? null;
  const passwordDialog = confirming
    ? { title: "Confirm it is you", body: "Enter your password to continue.", action: "Confirm" }
    : passwordMode
      ? passwordCopy[passwordMode]
      : null;
  const passwordNeedsValue = !confirming && (passwordMode === "set" || passwordMode === "change");

  const confirmThenSave = async () => {
    const response = await fetch("/api/session/verify/password", {
      method: "POST",
      credentials: "include",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ password: confirmDraft }),
    });
    if (response.status === 401) {
      setNotice("Email or password is wrong.");
      return;
    }
    if (!response.ok) {
      setNotice("Try again.");
      return;
    }
    setConfirming(false);
    setConfirmDraft("");
    savePassword.mutate();
  };

  return (
    <Stack gap="lg">
      <Title order={1}>Security</Title>
      {me.data && passkeys.data ? (
        <>
          <Stack gap="sm">
            <Title order={2}>Passkeys</Title>
            <ul className="record-list" aria-label="Passkeys">
              {list.map((passkey) => (
                <PasskeyRow
                  key={passkey.id}
                  passkey={passkey}
                  renaming={renameId === passkey.id}
                  renameName={renameName}
                  onRenameName={setRenameName}
                  onStartRename={() => {
                    setRenameId(passkey.id);
                    setRenameName(passkey.name);
                  }}
                  onCancelRename={() => setRenameId(null)}
                  onSaveRename={() => rename.mutate()}
                  canRemove={allowPasskeyRemoval}
                  onRemove={() => setRemoveId(passkey.id)}
                />
              ))}
            </ul>
            {adding ? (
              <Stack gap="xs">
                <Text>Confirm this passkey to finish adding it.</Text>
                <TextInput
                  label="Passkey name"
                  value={addName}
                  onChange={(event) => setAddName(event.currentTarget.value)}
                />
                <Group gap="xs">
                  <Button
                    type="button"
                    disabled={add.isPending || addName.trim().length === 0}
                    onClick={() => add.mutate()}
                  >
                    Confirm passkey
                  </Button>
                  <Button type="button" variant="default" onClick={() => setAdding(false)}>
                    Cancel
                  </Button>
                </Group>
              </Stack>
            ) : (
              <div>
                <Button type="button" disabled={live && add.isPending} onClick={() => (live ? add.mutate() : setAdding(true))}>
                  Add passkey
                </Button>
                {notice ? <Text role="status">{notice}</Text> : null}
              </div>
            )}
          </Stack>
          {liveSessions.data ? (
            <Stack gap="sm">
              <Title order={2}>Where you're logged in</Title>
              <ul className="record-list" aria-label="Sessions">
                {liveSessions.data.map((item) => (
                  <li className="record" key={item.id}>
                    <Text fw={600}>{item.current ? "This session" : item.user_agent || "Session"}</Text>
                    <Text size="sm">Logged in {formatDate(item.signed_in_at)}</Text>
                    <Button
                      type="button"
                      variant="default"
                      onClick={() => {
                        void fetch(`/api/sessions/${item.id}`, { method: "DELETE", credentials: "include" }).then(
                          () => {
                            void queryClient.invalidateQueries({ queryKey: ["live-sessions"] });
                          },
                        );
                      }}
                    >
                      Log out
                    </Button>
                  </li>
                ))}
              </ul>
            </Stack>
          ) : null}
          <Stack gap="sm">
            <Title order={2}>Password</Title>
            <Text>{passwordSet ? "A password is set." : "No password is set."}</Text>
            {passwordSet ? (
              <Group gap="xs">
                <Button type="button" variant="default" onClick={() => setPasswordMode("change")}>
                  Change password
                </Button>
                <Button
                  type="button"
                  variant="default"
                  disabled={!allowPasswordRemoval}
                  onClick={() => setPasswordMode("remove")}
                >
                  Remove password
                </Button>
              </Group>
            ) : (
              <div>
                <Button type="button" onClick={() => setPasswordMode("set")}>
                  Set password
                </Button>
              </div>
            )}
            {passwordSet && !allowPasswordRemoval ? <Text size="sm">{lastPasswordReason}</Text> : null}
          </Stack>
        </>
      ) : null}
      <ConfirmDialog
        opened={removing !== null}
        title={removing ? `Remove the ${removing.name} passkey?` : "Remove this passkey?"}
        body="You will lose this way to log in."
        action="Remove passkey"
        busy={remove.isPending}
        onClose={() => setRemoveId(null)}
        onConfirm={() => remove.mutate()}
      />
      <ConfirmDialog
        opened={passwordDialog !== null}
        title={passwordDialog?.title ?? "Password"}
        body={passwordDialog?.body ?? ""}
        action={passwordDialog?.action ?? "Continue"}
        busy={savePassword.isPending}
        confirmDisabled={
          confirming ? [...confirmDraft].length < 15 : passwordNeedsValue && [...passwordDraft].length < 15
        }
        onClose={() => {
          setPasswordMode(null);
          setPasswordDraft("");
          setConfirming(false);
          setConfirmDraft("");
        }}
        onConfirm={() => {
          if (confirming) {
            void confirmThenSave();
            return;
          }
          savePassword.mutate();
        }}
      >
        {confirming ? (
          <TextInput
            label="Password"
            type="password"
            value={confirmDraft}
            onChange={(event) => setConfirmDraft(event.currentTarget.value)}
          />
        ) : passwordNeedsValue ? (
          <TextInput
            label="New password"
            type="password"
            value={passwordDraft}
            onChange={(event) => setPasswordDraft(event.currentTarget.value)}
          />
        ) : null}
      </ConfirmDialog>
    </Stack>
  );
}

type LivePasskey = { id: string; name: string; added: string; last_used: string | null };
type LiveSession = { id: string; user_agent: string; signed_in_at: string; current: boolean };

async function liveObject<T extends object>(path: string): Promise<T | null> {
  try {
    const response = await fetch(path, { credentials: "include" });
    if (!response.ok) return null;
    const body: unknown = await response.json();
    if (!body || typeof body !== "object" || Array.isArray(body)) return null;
    return body as T;
  } catch {
    return null;
  }
}

class ConfirmPassword extends Error {
  constructor() {
    super("sign in again");
    this.name = "ConfirmPassword";
  }
}

async function saveLivePassword(mode: PasswordMode | null, password: string, passkeys: number): Promise<void> {
  const send = () =>
    mode === "remove"
      ? fetch("/api/password", { method: "DELETE", credentials: "include" })
      : fetch("/api/password", {
          method: "PUT",
          credentials: "include",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ password }),
        });
  let response = await send();
  if (response.status === 403) {
    if (passkeys === 0) throw new ConfirmPassword();
    await confirmWithPasskey();
    response = await send();
  }
  if (response.ok) return;
  let message = "Try again.";
  try {
    const body = (await response.json()) as { error?: string };
    if (body.error) message = body.error;
  } catch {
    // The status is enough when the body is not JSON.
  }
  throw new Error(message);
}

async function liveJSON<T>(path: string): Promise<T | null> {
  try {
    const response = await fetch(path, { credentials: "include" });
    if (!response.ok) return null;
    const body: unknown = await response.json();
    return Array.isArray(body) ? (body as T) : null;
  } catch {
    return null;
  }
}

function PasskeyRow({
  passkey,
  renaming,
  renameName,
  onRenameName,
  onStartRename,
  onCancelRename,
  onSaveRename,
  canRemove,
  onRemove,
}: {
  passkey: Passkey;
  renaming: boolean;
  renameName: string;
  onRenameName: (value: string) => void;
  onStartRename: () => void;
  onCancelRename: () => void;
  onSaveRename: () => void;
  canRemove: boolean;
  onRemove: () => void;
}) {
  return (
    <li className="record">
      {renaming ? (
        <Group gap="xs" wrap="wrap">
          <TextInput
            aria-label={`New name for ${passkey.name}`}
            value={renameName}
            onChange={(event) => onRenameName(event.currentTarget.value)}
          />
          <Button type="button" disabled={renameName.trim().length === 0} onClick={onSaveRename}>
            Save name
          </Button>
          <Button type="button" variant="default" onClick={onCancelRename}>
            Cancel
          </Button>
        </Group>
      ) : (
        <>
          <Text fw={600}>{passkey.name}</Text>
          <Text size="sm">Added {formatDate(passkey.added)}</Text>
          <Text size="sm">Last used {formatDate(passkey.lastUsed)}</Text>
          <Button type="button" variant="default" onClick={onStartRename}>
            Rename {passkey.name}
          </Button>
          <Button type="button" variant="default" disabled={!canRemove} onClick={onRemove}>
            Remove {passkey.name}
          </Button>
          {canRemove ? null : <Text size="sm">{lastPasskeyReason}</Text>}
        </>
      )}
    </li>
  );
}
