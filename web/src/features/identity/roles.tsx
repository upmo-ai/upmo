import { Group, Stack, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Badge, Button, Checkbox, ConfirmDialog, Input, PageHeader, Table } from "@upmo-ai/components";

import { RowAction } from "../../ui/icons";
import {
  ConfirmPassword,
  type LiveMember,
  type LiveRole,
  canManageRoles,
  choicesFor,
  liveJSON,
  liveObject,
  memberWrite,
  roleActions,
  roleInstall,
  roleObjects,
} from "./members";

import "./identity.css";

const defaultStubRoles: LiveRole[] = [
  {
    id: "role-owner",
    name: "Owner",
    system: "owner",
    permissions: [
      ...roleObjects.flatMap((obj) => roleActions.map((act) => `${obj}:${act}`)),
      ...roleInstall,
      "owners:manage",
    ],
    members: 1,
  },
  {
    id: "role-admin",
    name: "Admin",
    system: "admin",
    permissions: [
      ...roleObjects.flatMap((obj) => roleActions.map((act) => `${obj}:${act}`)),
      ...roleInstall,
    ],
    members: 1,
  },
  {
    id: "role-member",
    name: "Member",
    system: "member",
    permissions: roleObjects.flatMap((obj) => ["create", "view", "edit", "share"].map((act) => `${obj}:${act}`)),
    members: 2,
  },
  {
    id: "role-viewer",
    name: "Viewer",
    system: "viewer",
    permissions: roleObjects.map((obj) => `${obj}:view`),
    members: 0,
  },
];

export function RolesCard({
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
      <PageHeader title="Roles" />
      {canManage ? (
        <div>
          <Button type="button" onClick={() => start(null, "")}>
            New role
          </Button>
        </div>
      ) : null}
      <Table.ScrollContainer minWidth={560}>
        <Table aria-label="Roles">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th ta="right">Permissions</Table.Th>
              <Table.Th ta="right">Members</Table.Th>
              {canManage ? <Table.Th aria-label="Role actions" ta="right">Actions</Table.Th> : null}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {roles.map((role) => (
              <Table.Tr key={role.id}>
                <Table.Td>
                  <Group gap="xs" align="center" wrap="nowrap">
                    <Text fw={500}>{role.name}</Text>
                    {role.system ? (
                      <Badge variant="light" size="sm" radius="xl">
                        System
                      </Badge>
                    ) : null}
                  </Group>
                </Table.Td>
                <Table.Num>{(role.permissions ?? []).length}</Table.Num>
                <Table.Num>{role.members ?? 0}</Table.Num>
                {canManage ? (
                  <Table.Td>
                    <Group gap="xs" justify="flex-end" wrap="nowrap">
                      <Button
                        type="button"
                        variant="default"
                        size="xs"
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
                        <RowAction
                          label="Edit"
                          concept="action.edit"
                          size={30}
                          onClick={() => start(role, role.name)}
                        />
                      )}
                      {role.system ? null : (
                        <RowAction
                          label="Delete"
                          concept="action.delete"
                          size={30}
                          onClick={() => {
                            void run(async () => {
                              await memberWrite(`/api/roles/${role.id}`, "DELETE", null, passkeys);
                              await onChanged();
                            });
                          }}
                        />
                      )}
                    </Group>
                  </Table.Td>
                ) : null}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
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
        <Input label="Role name" value={name} onChange={(event) => setName(event.currentTarget.value)} />
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

export function RolesPage() {
  const roles = useQuery({
    queryKey: ["live-roles"],
    queryFn: async () => {
      try {
        return await liveJSON<LiveRole[]>("/api/roles");
      } catch {
        return null;
      }
    },
  });
  const session = useQuery({
    queryKey: ["live-member-session"],
    queryFn: async () => {
      try {
        return await liveObject<{ user_id: string; has_password: boolean }>("/api/session");
      } catch {
        return null;
      }
    },
  });
  const members = useQuery({
    queryKey: ["live-members"],
    queryFn: async () => {
      try {
        return await liveJSON<LiveMember[]>("/api/members");
      } catch {
        return null;
      }
    },
  });
  const passkeys = useQuery({
    queryKey: ["live-member-passkeys"],
    queryFn: async () => {
      try {
        return await liveJSON<{ id: string }[]>("/api/passkeys");
      } catch {
        return [];
      }
    },
  });

  const [error, setError] = useState("");
  const passkeyCount = passkeys.data?.length ?? 0;
  const roleList = roles.data && roles.data.length > 0 ? roles.data : defaultStubRoles;
  const actor = members.data?.find((member) => member.id === session.data?.user_id);
  const actorRoleId = session.data ? actor?.role.id : "role-owner";
  const held = choicesFor(actorRoleId, roleList);
  const canManage = canManageRoles(actorRoleId, roleList) || !session.data;

  async function run(action: () => Promise<void>) {
    try {
      setError("");
      await action();
    } catch (err) {
      if (err instanceof ConfirmPassword) {
        setError(err.message);
        return;
      }
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <Stack gap="lg" className="settings-content">
      {error ? (
        <Text role="alert" c="red">
          {error}
        </Text>
      ) : null}
      <RolesCard
        roles={roleList}
        held={new Set(held.length > 0 ? held : roleList.flatMap((r) => r.permissions ?? []))}
        canManage={canManage}
        passkeys={passkeyCount}
        onChanged={async () => {
          await roles.refetch();
        }}
        run={run}
      />
    </Stack>
  );
}
