import { Button, NativeSelect, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  meQueryKey,
  membersQueryKey,
  useApi,
  type InviteRole,
  type Member,
  type ViewRole,
  type WorkspaceRole,
} from "../../app/api";
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
  const api = useApi();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const members = useQuery({
    queryKey: membersQueryKey(),
    queryFn: () => api.members.list(),
  });
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<InviteRole>("member");
  const [error, setError] = useState("");
  const [removeId, setRemoveId] = useState<string | null>(null);

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: membersQueryKey() });
    await queryClient.invalidateQueries({ queryKey: meQueryKey() });
  };

  const viewAs = useMutation({
    mutationFn: (next: ViewRole) => api.me.viewAs(next),
    onSuccess: refresh,
  });
  const invite = useMutation({
    mutationFn: () => api.members.invite({ email, role }),
    onSuccess: async () => {
      setEmail("");
      setError("");
      await refresh();
    },
    onError: (cause: Error) => setError(cause.message),
  });
  const setMemberRole = useMutation({
    mutationFn: (input: { id: string; role: InviteRole }) =>
      api.members.setRole(input.id, input.role),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: () => {
      if (!removeId) throw new Error("member missing");
      return api.members.remove(removeId);
    },
    onSuccess: async () => {
      setRemoveId(null);
      await refresh();
    },
  });

  const list = members.data ?? [];
  const actor = list.find((member) => member.id === me.data?.memberId);
  const manage = actor ? canManageMembers(actor.role) : false;
  const owners = list.filter((member) => member.role === "owner").length;
  const removing = list.find((member) => member.id === removeId) ?? null;

  return (
    <Stack gap="lg" className="settings-page">
      <Title order={1}>Members</Title>
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
              {manage ? <Table.Th>Access</Table.Th> : null}
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
        <Table.Td>
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
