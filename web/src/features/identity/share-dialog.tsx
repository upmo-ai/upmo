import { Stack, Text } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button, ConfirmDialog, Dialog, Radio, Select } from "@upmo-ai/components";

import {
  meQueryKey,
  membersQueryKey,
  shareQueryKey,
  teamsQueryKey,
  useApi,
  type GrantableShareLevel,
  type Member,
  type Task,
  type TaskShare,
  type TaskVisibility,
  type Team,
} from "../../app/api";
import { canEditSharing, canRemoveShare, isGrantableShareLevel } from "./rules";

import "./identity.css";

const levelLabel: Record<"owner" | GrantableShareLevel, string> = {
  owner: "Owner",
  editor: "Editor",
  viewer: "Viewer",
};

function isVisibility(value: string): value is TaskVisibility {
  return value === "private" || value === "team" || value === "everyone";
}

export function ShareDialog({
  opened,
  task,
  onClose,
}: {
  opened: boolean;
  task: Task;
  onClose: () => void;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const members = useQuery({
    queryKey: membersQueryKey(),
    queryFn: () => api.members.list(),
    enabled: opened,
  });
  const teams = useQuery({
    queryKey: teamsQueryKey(),
    queryFn: () => api.shares.teams(),
    enabled: opened,
  });
  const share = useQuery({
    queryKey: shareQueryKey(task.id),
    queryFn: () => api.shares.get(task.id),
    enabled: opened,
  });
  const [picked, setPicked] = useState<string | null>(null);
  const [level, setLevel] = useState<GrantableShareLevel>("viewer");
  const [removeId, setRemoveId] = useState<string | null>(null);

  const refresh = () => queryClient.invalidateQueries({ queryKey: shareQueryKey(task.id) });

  const ready = me.data && members.data && teams.data && share.data;
  const people = share.data?.people ?? [];
  const mine = people.find((person) => person.memberId === me.data?.memberId) ?? null;
  const editable = canEditSharing(mine?.level ?? null);
  const roster = members.data ?? [];
  const candidates = roster.filter((member) => !people.some((person) => person.memberId === member.id));
  const personId =
    picked && candidates.some((member) => member.id === picked) ? picked : (candidates[0]?.id ?? "");
  const removing = roster.find((member) => member.id === removeId) ?? null;
  if (!opened) return null;

  return (
    <>
      <Dialog opened onClose={onClose} title="Share" withCloseButton={false}>
        {ready && share.data ? (
          <Stack gap="md">
            <Visibility
              share={share.data}
              teams={teams.data ?? []}
              editable={editable}
              onChange={(visibility, teamId) => {
                void api.shares.setVisibility(task.id, visibility, teamId).then(() => {
                  void refresh();
                });
              }}
            />
            <Stack gap="xs">
              <Text fw={600}>People with access</Text>
              <ul className="record-list" aria-label="People with access">
                {people.map((person) => {
                  const member = roster.find((item) => item.id === person.memberId);
                  const name = member?.name ?? "Unknown";
                  return (
                    <li key={person.memberId} className="record">
                      <Text fw={600}>{name}</Text>
                      {editable && canRemoveShare(person.level) ? (
                        <>
                          <Select
                            aria-label={`Access for ${name}`}
                            value={person.level}
                            allowDeselect={false}
                            data={[
                              { value: "editor", label: "Editor" },
                              { value: "viewer", label: "Viewer" },
                            ]}
                            onChange={(next) => {
                              if (!next || !isGrantableShareLevel(next)) return;
                              void api.shares
                                .setLevel(task.id, person.memberId, next)
                                .then(() => {
                                  void refresh();
                                });
                            }}
                          />
                          <Button type="button" variant="default" onClick={() => setRemoveId(person.memberId)}>
                            Remove access for {name}
                          </Button>
                        </>
                      ) : (
                        <Text>{levelLabel[person.level]}</Text>
                      )}
                    </li>
                  );
                })}
              </ul>
            </Stack>
            {editable && candidates.length > 0 ? (
              <Stack gap="xs">
                <Select
                  label="Person"
                  value={personId}
                  allowDeselect={false}
                  data={candidates.map((member) => ({
                    value: member.id,
                    label: personLabel(member),
                  }))}
                  onChange={(next) => {
                    if (next) setPicked(next);
                  }}
                />
                <Select
                  label="Level"
                  value={level}
                  allowDeselect={false}
                  data={[
                    { value: "editor", label: "Editor" },
                    { value: "viewer", label: "Viewer" },
                  ]}
                  onChange={(next) => {
                    if (next && isGrantableShareLevel(next)) setLevel(next);
                  }}
                />
                <div>
                  <Button
                    type="button"
                    disabled={personId.length === 0}
                    onClick={() => {
                      void api.shares.addPerson(task.id, personId, level).then(() => {
                        setPicked(null);
                        void refresh();
                      });
                    }}
                  >
                    Add person
                  </Button>
                </div>
              </Stack>
            ) : null}
            <div>
              <Button type="button" variant="default" onClick={onClose}>
                Close
              </Button>
            </div>
          </Stack>
        ) : (
          <Text>Loading sharing.</Text>
        )}
      </Dialog>
      <ConfirmDialog
        opened={removing !== null}
        title={removing ? `Remove ${removing.name}'s access?` : "Remove this access?"}
        body={removing ? `${removing.name} will lose access to this task.` : ""}
        action="Remove access"
        destructive
        onClose={() => setRemoveId(null)}
        onConfirm={() => {
          if (!removeId) return;
          void api.shares.removePerson(task.id, removeId).then(() => {
            setRemoveId(null);
            void refresh();
          });
        }}
      />
    </>
  );
}

function personLabel(member: Member) {
  return `${member.name} (${member.email})`;
}

function Visibility({
  share,
  teams,
  editable,
  onChange,
}: {
  share: TaskShare;
  teams: Team[];
  editable: boolean;
  onChange: (visibility: TaskVisibility, teamId: string | null) => void;
}) {
  const teamName = teams.find((team) => team.id === share.teamId)?.name;

  if (!editable) {
    const label =
      share.visibility === "private"
        ? "Only me"
        : share.visibility === "everyone"
          ? "Everyone in the workspace"
          : (teamName ?? "A team");
    return (
      <Stack gap={4}>
        <Text fw={600}>Who can see this task</Text>
        <Text>{label}</Text>
      </Stack>
    );
  }

  return (
    <Stack gap="xs">
      <Radio.Group
        label="Who can see this task"
        value={share.visibility}
        onChange={(value) => {
          if (!isVisibility(value)) return;
          if (value === "team") {
            const teamId = share.teamId ?? teams[0]?.id ?? null;
            if (!teamId) return;
            onChange("team", teamId);
            return;
          }
          onChange(value, null);
        }}
      >
        <Stack gap="xs" mt="xs">
          <Radio value="private" label="Only me" />
          <Radio value="team" label="A team" />
          <Radio value="everyone" label="Everyone in the workspace" />
        </Stack>
      </Radio.Group>
      {share.visibility === "team" ? (
        <Select
          label="Team"
          value={share.teamId ?? teams[0]?.id ?? null}
          allowDeselect={false}
          data={teams.map((team) => ({ value: team.id, label: team.name }))}
          onChange={(next) => {
            if (next) onChange("team", next);
          }}
        />
      ) : null}
    </Stack>
  );
}
