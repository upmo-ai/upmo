import { Button, Checkbox, NativeSelect, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import {
  MEMBERS_KEY,
  SESSION_KEY,
  meQueryKey,
  membersQueryKey,
  useApi,
  useWrite,
  type InviteRole,
  type Member,
  type ViewRole,
  type WorkspaceRole,
} from "../../app/api";
import { fresh } from "../../app/passkeys";
import { ConfirmDialog } from "./confirm";
import {
  canManageMembers,
  canRemoveMember,
  canSetMemberRole,
  inviteRoles,
  isInviteRole,
  ownerLockReason,
} from "./rules";

const roleLabel: Record<WorkspaceRole, string> = {
  owner: "Owner",
  admin: "Admin",
  member: "Member",
  viewer: "Viewer",
};

const viewChoices: { role: ViewRole; label: string }[] = [
  { role: "admin", label: "Admin" },
  { role: "member", label: "Member" },
  { role: "viewer", label: "Viewer" },
];

export function MembersPage() {
  const probe = useQuery({
    queryKey: ["members-live"],
    retry: false,
    queryFn: async () => {
      try {
        const response = await fetch("/api/members", { credentials: "include" });
        if (!response.ok) return null;
        const body: unknown = await response.json();
        return Array.isArray(body) ? (body as LiveMember[]) : null;
      } catch {
        return null;
      }
    },
  });
  if (probe.isPending) {
    return (
      <Stack gap="lg" className="settings-page">
        <Title order={1}>Members</Title>
      </Stack>
    );
  }
  if (Array.isArray(probe.data)) return <LiveMembers />;
  return <StubMembers />;
}

function StubMembers() {
  const api = useApi();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const members = useQuery({
    queryKey: membersQueryKey(),
    queryFn: () => api.members.list(),
  });
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<InviteRole>("member");
  const [error, setError] = useState("");
  const [removeId, setRemoveId] = useState<string | null>(null);

  const viewAs = useWrite((next: ViewRole) => api.me.viewAs(next), [MEMBERS_KEY, SESSION_KEY]);
  const invite = useWrite(
    () => api.members.invite({ email, role }),
    [MEMBERS_KEY],
    {
      shown: true,
      onSuccess: async () => {
        setEmail("");
        setError("");
      },
      onError: (cause: Error) => setError(cause.message),
    },
  );
  const setMemberRole = useWrite(
    (input: { id: string; role: InviteRole }) => api.members.setRole(input.id, input.role),
    [MEMBERS_KEY],
  );
  const remove = useWrite(
    () => {
      if (!removeId) throw new Error("member missing");
      return api.members.remove(removeId);
    },
    [MEMBERS_KEY],
    {
      onSuccess: async () => {
        setRemoveId(null);
      },
    },
  );

  const list = members.data ?? [];
  const actor = list.find((member) => member.id === me.data?.memberId);
  const manage = actor ? canManageMembers(actor.role) : false;
  const owners = list.filter((member) => member.role === "owner").length;
  const removing = list.find((member) => member.id === removeId) ?? null;

  return (
    <Stack gap="lg" className="settings-page">
      <Title order={1}>Members</Title>
      {members.isError ? (
        <Text role="alert" c="red">
          {members.error?.message ?? "Failed to load members."}
        </Text>
      ) : null}
      {me.data ? (
        <div className="view-as" role="group" aria-label="View as">
          <Text fw={600}>View as</Text>
          {viewChoices.map((choice) => (
            <Button
              key={choice.role}
              type="button"
              variant={me.data?.view === choice.role ? "filled" : "default"}
              aria-pressed={me.data?.view === choice.role}
              onClick={() => viewAs.mutate(choice.role)}
            >
              {choice.label}
            </Button>
          ))}
        </div>
      ) : null}
      {manage ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            setError("");
            invite.mutate();
          }}
        >
          <Stack gap="sm">
            <TextInput
              label="Email"
              type="email"
              value={email}
              onChange={(event) => setEmail(event.currentTarget.value)}
            />
            <NativeSelect
              label="Invite role"
              value={role}
              data={inviteRoles.map((item) => ({ value: item, label: roleLabel[item] }))}
              onChange={(event) => {
                const next = event.currentTarget.value;
                if (isInviteRole(next)) setRole(next);
              }}
            />
            {error ? <Text size="sm">{error}</Text> : null}
            <div>
              <Button type="submit" disabled={invite.isPending || !email.trim().includes("@")}>
                Invite
              </Button>
            </div>
          </Stack>
        </form>
      ) : null}
      {members.data && actor && me.data ? (
        <Table className="members-table" aria-label="Members">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Email</Table.Th>
              <Table.Th>Role</Table.Th>
              <Table.Th>Teams</Table.Th>
              {manage ? <Table.Th aria-label="Actions">Actions</Table.Th> : null}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {list.map((member) => (
              <MemberRow
                key={member.id}
                member={member}
                manage={manage}
                actorRole={actor.role}
                ownerCount={owners}
                onRole={(next) => setMemberRole.mutate({ id: member.id, role: next })}
                onRemove={() => setRemoveId(member.id)}
              />
            ))}
          </Table.Tbody>
        </Table>
      ) : null}
      <ConfirmDialog
        opened={removing !== null}
        title={removing ? `Remove ${removing.name}?` : "Remove this member?"}
        body={removing ? `${removing.name} will lose access to this workspace.` : ""}
        action="Remove member"
        busy={remove.isPending}
        onClose={() => setRemoveId(null)}
        onConfirm={() => remove.mutate()}
      />
    </Stack>
  );
}

function MemberRow({
  member,
  manage,
  actorRole,
  ownerCount,
  onRole,
  onRemove,
}: {
  member: Member;
  manage: boolean;
  actorRole: WorkspaceRole;
  ownerCount: number;
  onRole: (role: InviteRole) => void;
  onRemove: () => void;
}) {
  const lock =
    member.role === "owner" ? ownerLockReason(actorRole, ownerCount) : null;
  const roleEditable = canSetMemberRole({
    actor: actorRole,
    target: member.role,
    next: member.role === "owner" ? "admin" : member.role,
    ownerCount,
  });
  const removable = canRemoveMember({
    actor: actorRole,
    target: member.role,
    ownerCount,
  });
  const options =
    member.role === "owner"
      ? [{ value: "owner", label: "Owner" }]
      : inviteRoles.map((item) => ({ value: item, label: roleLabel[item] }));

  return (
    <Table.Tr>
      <Table.Td>{member.name}</Table.Td>
      <Table.Td>{member.email}</Table.Td>
      <Table.Td>
        {manage ? (
          <Stack gap={4}>
            <NativeSelect
              aria-label={`Role for ${member.name}`}
              value={member.role}
              disabled={!roleEditable}
              data={options}
              onChange={(event) => {
                const next = event.currentTarget.value;
                if (isInviteRole(next)) onRole(next);
              }}
            />
            {lock ? <Text size="sm">{lock}</Text> : null}
          </Stack>
        ) : (
          roleLabel[member.role]
        )}
      </Table.Td>
      <Table.Td>{member.teams.length > 0 ? member.teams.join(", ") : "No team"}</Table.Td>
      {manage ? (
        <Table.Td aria-label="Actions">
          <Button
            type="button"
            className="member-remove"
            variant="default"
            disabled={!removable}
            onClick={onRemove}
          >
            Remove {member.name}
          </Button>
        </Table.Td>
      ) : null}
    </Table.Tr>
  );
}

type LiveRole = { id: string; name: string; system: string; permissions?: string[]; members?: number };
type LiveMember = {
  id: string;
  name: string;
  email: string;
  invited: boolean;
  teams: string[];
  role: LiveRole;
};

class ConfirmPassword extends Error {
  constructor() {
    super("Confirm your password to continue.");
    this.name = "ConfirmPassword";
  }
}

function LiveMembers() {
  const members = useQuery({
    queryKey: ["live-members"],
    queryFn: () => liveJSON<LiveMember[]>("/api/members"),
  });
  const roles = useQuery({
    queryKey: ["live-roles"],
    queryFn: () => liveJSON<LiveRole[]>("/api/roles"),
  });
  const session = useQuery({
    queryKey: ["live-member-session"],
    queryFn: () => liveObject<{ user_id: string; has_password: boolean }>("/api/session"),
  });
  const passkeys = useQuery({
    queryKey: ["live-member-passkeys"],
    queryFn: () => liveJSON<{ id: string }[]>("/api/passkeys"),
  });
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [roleId, setRoleId] = useState("");
  const [link, setLink] = useState("");
  const [error, setError] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState<null | (() => Promise<void>)>(null);
  const [removeId, setRemoveId] = useState<string | null>(null);

  const list = members.data ?? [];
  const actor = list.find((member) => member.id === session.data?.user_id);
  const manage = actor?.role.system === "owner" || actor?.role.system === "admin";
  const choices = (roles.data ?? []).filter((role) => role.system !== "owner" || actor?.role.system === "owner");
  const passkeyCount = passkeys.data?.length ?? 0;
  const removing = list.find((member) => member.id === removeId) ?? null;

  async function run(action: () => Promise<void>) {
    try {
      await action();
      setError("");
    } catch (cause) {
      if (cause instanceof ConfirmPassword) {
        setConfirm(() => action);
        return;
      }
      setError(cause instanceof Error ? cause.message : "Try again.");
    }
  }

  async function confirmPassword() {
    const response = await fetch("/api/session/verify/password", {
      method: "POST",
      credentials: "include",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ password }),
    });
    if (!response.ok) {
      setError("Email or password is wrong.");
      return;
    }
    const action = confirm;
    setConfirm(null);
    setPassword("");
    if (action) await run(action);
  }

  return (
    <Stack gap="lg" className="settings-page">
      <Title order={1}>Members</Title>
      {error ? (
        <Text role="alert" c="red">
          {error}
        </Text>
      ) : null}
      {link ? (
        <Stack gap="xs">
          <TextInput label="Invite link" readOnly value={link} />
          <Button
            type="button"
            onClick={() => {
              void navigator.clipboard.writeText(link);
            }}
          >
            Copy
          </Button>
        </Stack>
      ) : null}
      {manage ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void run(async () => {
              const body = await memberWrite(
                "/api/members",
                "POST",
                { email, name, role_id: roleId },
                passkeyCount,
              );
              const token = typeof body === "object" && body && "token" in body ? String(body.token) : "";
              setLink(`${window.location.origin}/login/link#${token}`);
              setEmail("");
              setName("");
              await members.refetch();
            });
          }}
        >
          <Stack gap="sm">
            <TextInput label="Email" type="email" value={email} onChange={(event) => setEmail(event.currentTarget.value)} />
            <TextInput label="Name" value={name} onChange={(event) => setName(event.currentTarget.value)} />
            <NativeSelect
              label="Invite role"
              value={roleId}
              data={[{ value: "", label: "Choose a role" }, ...choices.map((role) => ({ value: role.id, label: role.name }))]}
              onChange={(event) => setRoleId(event.currentTarget.value)}
            />
            <div>
              <Button type="submit" disabled={!email.trim().includes("@") || !name.trim() || !roleId}>
                Invite
              </Button>
            </div>
          </Stack>
        </form>
      ) : null}
      <Table className="members-table" aria-label="Members">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Name</Table.Th>
            <Table.Th>Email</Table.Th>
            <Table.Th>Role</Table.Th>
            <Table.Th>Teams</Table.Th>
            {manage ? <Table.Th aria-label="Actions">Actions</Table.Th> : null}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {list.map((member) => (
            <Table.Tr key={member.id}>
              <Table.Td>{member.name}</Table.Td>
              <Table.Td>{member.email}</Table.Td>
              <Table.Td>
                {manage && member.id !== actor?.id ? (
                  <NativeSelect
                    aria-label={`Role for ${member.name}`}
                    value={member.role.id}
                    data={(roles.data ?? []).map((role) => ({ value: role.id, label: role.name }))}
                    onChange={(event) => {
                      const nextRole = event.currentTarget.value;
                      void run(async () => {
                        await memberWrite(`/api/members/${member.id}/role`, "PUT", { role_id: nextRole }, passkeyCount);
                        await members.refetch();
                      });
                    }}
                  />
                ) : (
                  member.role.name
                )}
              </Table.Td>
              <Table.Td>{member.teams.length > 0 ? member.teams.join(", ") : "No team"}</Table.Td>
              {manage ? (
                <Table.Td aria-label="Actions">
                  {member.id === actor?.id ? null : (
                    <Button
                      type="button"
                      variant="default"
                      onClick={() => {
                        const path = member.invited
                          ? `/api/members/${member.id}/invite-link`
                          : `/api/members/${member.id}/reset-link`;
                        void run(async () => {
                          const body = await memberWrite(path, "POST", null, passkeyCount);
                          const token = typeof body === "object" && body && "token" in body ? String(body.token) : "";
                          setLink(`${window.location.origin}/login/link#${token}`);
                        });
                      }}
                    >
                      {member.invited ? "New invite link" : "Reset link"}
                    </Button>
                  )}
                  {!member.invited && member.id !== actor?.id ? (
                    <Text size="sm">This signs them in and logs out their other browsers.</Text>
                  ) : null}
                  {member.id === actor?.id ? null : (
                    <Button type="button" variant="default" onClick={() => setRemoveId(member.id)}>
                      Remove {member.name}
                    </Button>
                  )}
                </Table.Td>
              ) : null}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <RolesCard
        roles={roles.data ?? []}
        held={new Set(choicesFor(actor?.role.id, roles.data ?? []))}
        canManage={canManageRoles(actor?.role.id, roles.data ?? [])}
        passkeys={passkeyCount}
        onChanged={async () => {
          await roles.refetch();
          await members.refetch();
        }}
        run={run}
      />
      <ConfirmDialog
        opened={removing !== null}
        title={removing ? `Remove ${removing.name}?` : "Remove this member?"}
        body={removing ? `${removing.name} will lose access to this workspace.` : ""}
        action="Remove member"
        onClose={() => setRemoveId(null)}
        onConfirm={() => {
          const id = removeId;
          setRemoveId(null);
          if (!id) return;
          void run(async () => {
            await memberWrite(`/api/members/${id}`, "DELETE", null, passkeyCount);
            await members.refetch();
          });
        }}
      />
      <ConfirmDialog
        opened={confirm !== null}
        title="Confirm your password"
        body="Enter your password to continue."
        action="Confirm"
        confirmDisabled={!password}
        onClose={() => setConfirm(null)}
        onConfirm={() => {
          void confirmPassword();
        }}
      >
        <TextInput
          label="Password"
          type="password"
          value={password}
          onChange={(event) => setPassword(event.currentTarget.value)}
        />
      </ConfirmDialog>
    </Stack>
  );
}

async function liveJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: "include" });
  if (!response.ok) throw new Error("Failed to load members.");
  return (await response.json()) as T;
}

async function liveObject<T extends object>(path: string): Promise<T> {
  const response = await fetch(path, { credentials: "include" });
  if (!response.ok) throw new Error("Failed to load members.");
  return (await response.json()) as T;
}

async function memberWrite(path: string, method: string, body: unknown, passkeys: number): Promise<unknown> {
  const send = async () => {
    const response = await fetch(path, {
      method,
      credentials: "include",
      headers: body ? { "content-type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    const payload = await readPayload(response);
    if (response.status === 403 && payload.error === "sign in again") {
      if (passkeys === 0) throw new ConfirmPassword();
      throw { status: 403, error: "sign in again" };
    }
    if (!response.ok) throw new Error(payload.error || "Try again.");
    return payload.value;
  };
  if (passkeys === 0) return send();
  return fresh(send);
}

const roleObjects = ["task", "chat", "folder", "automation", "agent"] as const;
const roleActions = ["create", "view", "edit", "share", "delete"] as const;
const roleInstall = ["members:manage", "teams:manage", "roles:manage", "settings:manage", "connectors:manage"] as const;

function canManageRoles(roleId: string | undefined, roles: LiveRole[]): boolean {
  return roles.find((role) => role.id === roleId)?.permissions?.includes("roles:manage") ?? false;
}

function choicesFor(roleId: string | undefined, roles: LiveRole[]): string[] {
  return roles.find((role) => role.id === roleId)?.permissions ?? [];
}

function RolesCard({
  roles,
  held,
  canManage,
  passkeys,
  onChanged,
  run,
}: {
  roles: LiveRole[];
  held: Set<string>;
  canManage: boolean;
  passkeys: number;
  onChanged: () => Promise<void>;
  run: (action: () => Promise<void>) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [editing, setEditing] = useState<string | null>(null);

  function start(role: LiveRole | null, copyName: string) {
    setEditing(role && role.system === "" ? role.id : null);
    setName(copyName);
    setSelected(role?.permissions ?? []);
    setOpen(true);
  }

  const catalog = [
    ...roleObjects.flatMap((object) => roleActions.map((action) => `${object}:${action}`)),
    ...roleInstall,
  ];

  return (
    <Stack gap="sm">
      <Title order={2}>Roles</Title>
      {canManage ? (
        <Button type="button" onClick={() => start(null, "")}>
          New role
        </Button>
      ) : null}
      <Table aria-label="Roles">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Name</Table.Th>
            <Table.Th>Permissions</Table.Th>
            <Table.Th>Members</Table.Th>
            {canManage ? <Table.Th aria-label="Role actions">Actions</Table.Th> : null}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {roles.map((role) => (
            <Table.Tr key={role.id}>
              <Table.Td>
                {role.name}
                {role.system ? <Text span> System</Text> : null}
              </Table.Td>
              <Table.Td>{(role.permissions ?? []).length}</Table.Td>
              <Table.Td>{role.members ?? 0}</Table.Td>
              {canManage ? (
                <Table.Td>
                  <Button
                    type="button"
                    variant="default"
                    onClick={() => {
                      void run(async () => {
                        await memberWrite(
                          `/api/roles/${role.id}/duplicate`,
                          "POST",
                          { name: `${role.name} copy` },
                          passkeys,
                        );
                        await onChanged();
                      });
                    }}
                  >
                    Duplicate {role.name}
                  </Button>
                  {role.system ? null : (
                    <Button type="button" variant="default" onClick={() => start(role, role.name)}>
                      Edit {role.name}
                    </Button>
                  )}
                  {role.system ? null : (
                    <Button
                      type="button"
                      variant="default"
                      onClick={() => {
                        void run(async () => {
                          await memberWrite(`/api/roles/${role.id}`, "DELETE", null, passkeys);
                          await onChanged();
                        });
                      }}
                    >
                      Delete {role.name}
                    </Button>
                  )}
                </Table.Td>
              ) : null}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <ConfirmDialog
        opened={open}
        title={editing ? "Edit role" : "New role"}
        body="Choose permissions this role may use."
        action="Save role"
        confirmDisabled={!name.trim() || selected.length === 0}
        onClose={() => setOpen(false)}
        onConfirm={() => {
          const id = editing;
          const payload = { name, permissions: selected };
          setOpen(false);
          void run(async () => {
            if (id) await memberWrite(`/api/roles/${id}`, "PUT", payload, passkeys);
            else await memberWrite("/api/roles", "POST", payload, passkeys);
            await onChanged();
          });
        }}
      >
        <TextInput label="Role name" value={name} onChange={(event) => setName(event.currentTarget.value)} />
        <Stack gap="xs">
          {catalog.map((permission) => {
            const locked = !held.has(permission);
            return (
              <Checkbox
                key={permission}
                label={permission}
                checked={selected.includes(permission) || (locked && selected.includes(permission))}
                disabled={locked}
                onChange={(event) => {
                  setSelected((current) =>
                    event.currentTarget.checked
                      ? [...current, permission]
                      : current.filter((item) => item !== permission),
                  );
                }}
              />
            );
          })}
        </Stack>
      </ConfirmDialog>
    </Stack>
  );
}

async function readPayload(response: Response): Promise<{ error: string; value: unknown }> {
  if (response.status === 204) return { error: "", value: null };
  try {
    const value = (await response.json()) as { error?: string };
    return { error: value.error ?? "", value };
  } catch {
    return { error: "", value: null };
  }
}
