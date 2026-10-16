import { Group, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button, Input, Table } from "@upmo-ai/components";

import { useApi } from "../../app/api";
import {
  ConfirmPassword,
  type LiveMember,
  type LiveRole,
  type LiveTeam,
  choicesFor,
  liveJSON,
  liveObject,
  memberWrite,
} from "./members";

import "./identity.css";

const defaultStubTeams = [
  { id: "team-finance", name: "Finance" },
  { id: "team-ops", name: "Ops" },
];

export function TeamsPage() {
  const api = useApi();
  const teams = useQuery({
    queryKey: ["live-teams"],
    queryFn: async () => {
      try {
        return await liveJSON<LiveTeam[]>("/api/teams");
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

  const stubMembers = useQuery({
    queryKey: ["stub-members-teams"],
    queryFn: () => (api.members?.list ? api.members.list() : Promise.resolve([])),
    enabled: !members.data,
  });

  const stubTeams = useQuery({
    queryKey: ["stub-teams"],
    queryFn: () => (api.shares?.teams ? api.shares.teams() : Promise.resolve([])),
    enabled: !teams.data,
  });

  const [teamName, setTeamName] = useState("");
  const [error, setError] = useState("");

  const passkeyCount = passkeys.data?.length ?? 0;
  const memberList: { id: string; name: string; teams: string[] }[] =
    members.data ?? (stubMembers.data ?? []).map((m) => ({ id: m.id, name: m.name, teams: m.teams }));

  const actor = members.data?.find((member) => member.id === session.data?.user_id);
  const held = choicesFor(actor?.role.id, roles.data ?? []);
  const canTeams = held.length === 0 || held.includes("teams:manage");

  const teamList: LiveTeam[] =
    teams.data && teams.data.length > 0
      ? teams.data
      : (stubTeams.data && stubTeams.data.length > 0
          ? stubTeams.data
          : defaultStubTeams);

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
      <Title order={1}>Teams</Title>
      {error ? (
        <Text role="alert" c="red">
          {error}
        </Text>
      ) : null}
      {canTeams ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (!teamName.trim()) return;
            void run(async () => {
              await memberWrite("/api/teams", "POST", { name: teamName.trim() }, passkeyCount);
              setTeamName("");
              await teams.refetch();
            });
          }}
        >
          <Group gap="sm" align="flex-end">
            <Input
              label="Team name"
              value={teamName}
              placeholder="e.g. Finance, Design"
              onChange={(event) => setTeamName(event.currentTarget.value)}
            />
            <Button type="submit" disabled={!teamName.trim()}>
              New team
            </Button>
          </Group>
        </form>
      ) : null}
      <Table.ScrollContainer minWidth={560} className="u-table-wrap">
        <Table stickyHeader highlightOnHover verticalSpacing="sm" className="u-table members-table" aria-label="Teams">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Team</Table.Th>
              <Table.Th>Members</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {teamList.map((team) => {
              const assigned = memberList.filter((m) => m.teams.includes(team.name));
              return (
                <Table.Tr key={team.id}>
                  <Table.Td fw={500}>{team.name}</Table.Td>
                  <Table.Td>
                    {assigned.length > 0 ? (
                      assigned.map((m) => m.name).join(", ")
                    ) : (
                      <Text span c="dimmed">
                        No members
                      </Text>
                    )}
                  </Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Stack>
  );
}
