import { Button, Group, Stack, Text, TextInput, Title } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { meQueryKey, passkeysQueryKey, useApi, type Passkey } from "../../app/api";
import { ConfirmDialog } from "./confirm";
import { formatDate } from "./format";
import { canRemovePasskey, canRemovePassword, lastPasskeyReason, lastPasswordReason } from "./rules";

type PasswordMode = "set" | "change" | "remove";

const passwordCopy: Record<PasswordMode, { title: string; body: string; action: string }> = {
  set: {
    title: "Set a password?",
    body: "This replaces passkey-only sign-in. You can still use a passkey.",
    action: "Set password",
  },
  change: {
    title: "Replace your current password?",
    body: "The current password is replaced and stops working.",
    action: "Change password",
  },
  remove: {
    title: "Remove your password?",
    body: "You will lose password sign-in.",
    action: "Remove password",
  },
};

export function SecurityPage() {
  const api = useApi();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const passkeys = useQuery({ queryKey: passkeysQueryKey(), queryFn: () => api.passkeys.list() });
  const [adding, setAdding] = useState(false);
  const [addName, setAddName] = useState("Passkey");
  const [renameId, setRenameId] = useState<string | null>(null);
  const [renameName, setRenameName] = useState("");
  const [removeId, setRemoveId] = useState<string | null>(null);
  const [passwordMode, setPasswordMode] = useState<PasswordMode | null>(null);
  const [passwordDraft, setPasswordDraft] = useState("");

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: passkeysQueryKey() });
    await queryClient.invalidateQueries({ queryKey: meQueryKey() });
  };

  const add = useMutation({
    mutationFn: () => api.passkeys.add(addName),
    onSuccess: async () => {
      setAdding(false);
      setAddName("Passkey");
      await refresh();
    },
  });
  const rename = useMutation({
    mutationFn: () => {
      if (!renameId) throw new Error("Passkey missing.");
      return api.passkeys.rename(renameId, renameName);
    },
    onSuccess: async () => {
      setRenameId(null);
      await refresh();
    },
  });
  const removePasskey = useMutation({
    mutationFn: () => {
      if (!removeId) throw new Error("Passkey missing.");
      return api.passkeys.remove(removeId);
    },
    onSuccess: async () => {
      setRemoveId(null);
      await refresh();
    },
  });
  const savePassword = useMutation({
    mutationFn: () => {
      if (passwordMode === "set") return api.password.set();
      if (passwordMode === "change") return api.password.change();
      return api.password.remove();
    },
    onSuccess: async () => {
      setPasswordMode(null);
      setPasswordDraft("");
      await refresh();
    },
  });

  const list = passkeys.data ?? [];
  const passwordSet = me.data?.passwordSet ?? false;
  const allowPasskeyRemoval = canRemovePasskey(list.length, passwordSet);
  const allowPasswordRemoval = canRemovePassword(list.length, passwordSet);
  const removing = list.find((item) => item.id === removeId) ?? null;
  const passwordDialog = passwordMode ? passwordCopy[passwordMode] : null;
  const passwordNeedsValue = passwordMode === "set" || passwordMode === "change";

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
                <Button type="button" onClick={() => setAdding(true)}>
                  Add passkey
                </Button>
              </div>
            )}
          </Stack>
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
        body="You will lose this way to sign in."
        action="Remove passkey"
        busy={removePasskey.isPending}
        onClose={() => setRemoveId(null)}
        onConfirm={() => removePasskey.mutate()}
      />
      <ConfirmDialog
        opened={passwordDialog !== null}
        title={passwordDialog?.title ?? "Password"}
        body={passwordDialog?.body ?? ""}
        action={passwordDialog?.action ?? "Continue"}
        busy={savePassword.isPending}
        confirmDisabled={passwordNeedsValue && passwordDraft.trim().length === 0}
        onClose={() => {
          setPasswordMode(null);
          setPasswordDraft("");
        }}
        onConfirm={() => savePassword.mutate()}
      >
        {passwordNeedsValue ? (
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
