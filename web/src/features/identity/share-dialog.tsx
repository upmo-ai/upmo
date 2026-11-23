import { Stack, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button, ConfirmDialog, Dialog, Radio, Select, StatusAlert } from "@upmo-ai/components";

import {
  BrainError,
  meQueryKey,
  membersQueryKey,
  shareQueryKey,
  teamsQueryKey,
  useApi,
  useWrite,
  type GrantableShareLevel,
  type Member,
  type ShareMember,
  type ShareUpdate,
  type Task,
  type TaskShare,
  type TaskVisibility,
  type Team,
} from "../../app/api";
import { RowAction } from "../../ui/icons";
import {
  canEditSharing,
  canRemoveShare,
  isGrantableShareLevel,
  shareRole,
  sharedTeamOf,
  teamsOfPerson,
  toShareUpdate,
} from "./rules";

import "./identity.css";

const roleLabel: Record<ShareMember["role"], string> = {
  owner: "Owner",
  editor: "Editor",
  viewer: "Viewer",
};

const visibilityOptions: { value: TaskVisibility; label: string; description: string }[] = [
  { value: "private", label: "Private", description: "Only the people and teams listed below." },
  { value: "team", label: "Team", description: "Your team, and everyone listed below." },
  { value: "everyone", label: "Everyone", description: "Everyone in the workspace." },
];

function isVisibility(value: string): value is TaskVisibility {
  return value === "private" || value === "team" || value === "everyone";
}

/** A refusal is shown in plain words. It never ends the session. */
function problemText(cause: unknown, action: "load" | "save", objectLabel: "task" | "chat"): string {
  if (cause instanceof BrainError && cause.status === 403) {
    return action === "save"
      ? "Only the owner can change sharing."
      : `You do not have access to this ${objectLabel}'s sharing.`;
  }
  if (cause instanceof BrainError && cause.status === 409) {
    return cause.message !== ""
      ? cause.message
      : "A reply is already on its way. Wait for it to finish or stop it before changing who has access.";
  }
  if (cause instanceof BrainError && cause.status === 404) {
    return objectLabel === "chat" ? "This chat is no longer available." : "This task is no longer available.";
  }
  // A refused share update is a 422 whose message is written for the person.
  if (cause instanceof BrainError && cause.status === 422 && cause.message !== "") {
    return cause.message;
  }
  const bare = cause instanceof BrainError && cause.message === cause.error;
  if (cause instanceof Error && cause.message !== "" && !bare) {
    return cause.message;
  }
  return action === "save" ? "Sharing could not be saved. Try again." : "Sharing could not be loaded. Try again.";
}

function subjectKey(member: Pick<ShareMember, "subject_type" | "subject_id">) {
  return `${member.subject_type}:${member.subject_id}`;
}

export type ShareDialogProps =
  | {
      opened: boolean;
      task: Task;
      onClose: () => void;
      objectLabel?: "task";
      queryKey?: readonly unknown[];
      loadShare?: (id: string) => Promise<TaskShare>;
      saveShare?: (id: string, update: ShareUpdate) => Promise<TaskShare>;
    }
  | {
      opened: boolean;
      objectId: string;
      objectLabel: "chat";
      onClose: () => void;
      queryKey: readonly unknown[];
      loadShare: (id: string) => Promise<TaskShare>;
      saveShare: (id: string, update: ShareUpdate) => Promise<TaskShare>;
    };

/**
 * The body is mounted only while the dialog is open, so a picked person, a
 * pending removal or an old error never carries over to the next opening.
 */
export function ShareDialog(props: ShareDialogProps) {
  if (!props.opened) return null;
  const objectId = "objectId" in props ? props.objectId : props.task.id;
  const objectLabel = props.objectLabel ?? "task";
  return (
    <ShareDialogBody
      objectId={objectId}
      objectLabel={objectLabel}
      queryKey={props.queryKey}
      loadShare={props.loadShare}
      saveShare={props.saveShare}
      onClose={props.onClose}
    />
  );
}

function ShareDialogBody({
  objectId,
  objectLabel,
  queryKey,
  loadShare,
  saveShare,
  onClose,
}: {
  objectId: string;
  objectLabel: "task" | "chat";
  queryKey?: readonly unknown[];
  loadShare?: (id: string) => Promise<TaskShare>;
  saveShare?: (id: string, update: ShareUpdate) => Promise<TaskShare>;
  onClose: () => void;
}) {
  const api = useApi();
  const key = queryKey ?? shareQueryKey(objectId);
  const load = loadShare ?? ((id: string) => api.shares.get(id));
  const saveFn = saveShare ?? ((id: string, update: ShareUpdate) => api.shares.put(id, update));
  const me = useQuery({ queryKey: meQueryKey(), queryFn: () => api.me.current() });
  const share = useQuery({
    queryKey: key,
    queryFn: () => load(objectId),
  });
  const editable = canEditSharing(shareRole(share.data, me.data?.memberId));
  // The roster and the teams are only needed to add access, which is owner only.
  const members = useQuery({
    queryKey: membersQueryKey(),
    queryFn: () => api.members.list(),
    enabled: editable,
  });
  const teams = useQuery({
    queryKey: teamsQueryKey(),
    queryFn: () => api.shares.teams(),
    enabled: editable,
  });
  const [pickedPerson, setPickedPerson] = useState<string | null>(null);
  const [pickedTeam, setPickedTeam] = useState<string | null>(null);
  const [level, setLevel] = useState<GrantableShareLevel>("viewer");
  const [removeKey, setRemoveKey] = useState<string | null>(null);
  const [problem, setProblem] = useState("");

  const save = useWrite((update: ShareUpdate) => saveFn(objectId, update), [key], {
    shown: true,
    onSuccess: () => setProblem(""),
    onError: (cause) => setProblem(problemText(cause, "save", objectLabel)),
  });

  const current = share.data;
  const held = current?.members ?? [];
  const removing = held.find((member) => subjectKey(member) === removeKey) ?? null;
  const roster = members.data ?? [];
  const personChoices = roster.filter(
    (person) => !held.some((member) => member.subject_type === "person" && member.subject_id === person.id),
  );
  const teamChoices = (teams.data ?? []).filter(
    (team) => !held.some((member) => member.subject_type === "team" && member.subject_id === team.id),
  );
  const personId =
    pickedPerson && personChoices.some((person) => person.id === pickedPerson) ? pickedPerson : "";
  const teamId = pickedTeam && teamChoices.some((team) => team.id === pickedTeam) ? pickedTeam : "";
  // A team visibility names one of the owner's teams. With more than one the
  // owner picks it; the saved team opens picked.
  const ownerTeams = teamsOfPerson(
    roster.find((person) => person.id === me.data?.memberId),
    teams.data ?? [],
  );
  const pickTeam = editable && ownerTeams.length > 1;
  const teamChoice = current ? sharedTeamOf(current, ownerTeams) : undefined;

  const change = (next: ShareUpdate, after?: () => void) => {
    setProblem("");
    save.mutate(next, { onSuccess: after });
  };

  const setVisibility = (visibility: TaskVisibility) => {
    if (!current) return;
    const update = toShareUpdate(current);
    change(
      visibility === "team"
        ? { ...update, visibility, ...(teamChoice !== undefined ? { team_id: teamChoice } : {}) }
        : { visibility, members: update.members },
    );
  };

  const setTeam = (chosen: string) => {
    if (!current) return;
    change({ ...toShareUpdate(current), visibility: "team", team_id: chosen });
  };

  const setRole = (member: ShareMember, role: GrantableShareLevel) => {
    if (!current) return;
    const update = toShareUpdate(current);
    change({
      ...update,
      members: update.members.map((grant) =>
        subjectKey(grant) === subjectKey(member) ? { ...grant, role } : grant,
      ),
    });
  };

  const grant = (subject_type: "person" | "team", subject_id: string, after: () => void) => {
    if (!current || subject_id === "") return;
    const update = toShareUpdate(current);
    change({ ...update, members: [...update.members, { subject_type, subject_id, role: level }] }, after);
  };

  const revoke = (member: ShareMember) => {
    if (!current) return;
    const update = toShareUpdate(current);
    save.mutate(
      { ...update, members: update.members.filter((item) => subjectKey(item) !== subjectKey(member)) },
      { onSettled: () => setRemoveKey(null) },
    );
    setProblem("");
  };

  const loadProblem = share.isError
    ? problemText(share.error, "load", objectLabel)
    : me.isError
      ? problemText(me.error, "load", objectLabel)
      : "";
  const choicesProblem =
    editable && (members.isError || teams.isError)
      ? "The people and teams you can share with could not be loaded."
      : "";
  const choicesLoading = editable && (members.isPending || teams.isPending) && choicesProblem === "";
  const close = () => {
    if (!save.isPending) onClose();
  };

  return (
    <>
      <Dialog opened onClose={close} title="Share" withCloseButton={false}>
        <Stack gap="md">
          {loadProblem ? <StatusAlert tone="critical">{loadProblem}</StatusAlert> : null}
          {choicesProblem ? <StatusAlert tone="critical">{choicesProblem}</StatusAlert> : null}
          {problem ? <StatusAlert tone="critical">{problem}</StatusAlert> : null}
          {current && me.data ? (
            <>
              {current.private_read ? (
                <StatusAlert tone="attention">
                  Earlier replies in this chat used personal memory or private knowledge. Consider branching this chat instead of sharing the full thread.
                </StatusAlert>
              ) : null}
              {editable ? null : (
                <Text size="sm" c="dimmed">
                  Only the owner can change who has access. You can see it here.
                </Text>
              )}
              <Visibility
                objectLabel={objectLabel}
                visibility={current.visibility}
                editable={editable}
                busy={save.isPending}
                // Team visibility names one of the owner's teams, which is only
                // known once the roster and the teams have loaded.
                teamWaiting={choicesLoading || choicesProblem !== ""}
                onChange={setVisibility}
              />
              {pickTeam && current.visibility === "team" ? (
                <Select
                  label="Shared team"
                  value={teamChoice ?? null}
                  allowDeselect={false}
                  disabled={save.isPending}
                  data={ownerTeams.map((team) => ({ value: team.id, label: team.name }))}
                  onChange={(next) => {
                    if (next) setTeam(next);
                  }}
                />
              ) : null}
              <Stack gap="xs">
                <Text fw={600}>Who has access</Text>
                <ul className="record-list" aria-label="Who has access">
                  {held.map((member) => (
                    <li key={subjectKey(member)} className="record">
                      <div className="record-main">
                        <Text fw={600}>{member.name}</Text>
                        <Text size="sm" c="dimmed">
                          {member.subject_type === "team" ? "Team" : "Person"}
                        </Text>
                      </div>
                      {editable && canRemoveShare(member.role) ? (
                        <div className="record-actions">
                          <Select
                            aria-label={`Access for ${member.name}`}
                            value={member.role}
                            allowDeselect={false}
                            disabled={save.isPending}
                            data={[
                              { value: "editor", label: "Editor" },
                              { value: "viewer", label: "Viewer" },
                            ]}
                            onChange={(next) => {
                              if (next && isGrantableShareLevel(next)) setRole(member, next);
                            }}
                          />
                          <RowAction
                            label={`Remove access for ${member.name}`}
                            concept="action.remove"
                            disabled={save.isPending}
                            onClick={() => setRemoveKey(subjectKey(member))}
                          />
                        </div>
                      ) : (
                        <Text>{roleLabel[member.role]}</Text>
                      )}
                    </li>
                  ))}
                </ul>
              </Stack>
              {choicesLoading ? <Text size="sm" c="dimmed">Loading people and teams.</Text> : null}
              {editable && (personChoices.length > 0 || teamChoices.length > 0) ? (
                <AddAccess
                  people={personChoices}
                  teams={teamChoices}
                  personId={personId}
                  teamId={teamId}
                  level={level}
                  busy={save.isPending}
                  onPerson={setPickedPerson}
                  onTeam={setPickedTeam}
                  onLevel={setLevel}
                  onAddPerson={() => grant("person", personId, () => setPickedPerson(null))}
                  onAddTeam={() => grant("team", teamId, () => setPickedTeam(null))}
                />
              ) : null}
            </>
          ) : loadProblem ? null : (
            <Text>Loading sharing.</Text>
          )}
          <div>
            <Button type="button" variant="default" disabled={save.isPending} onClick={close}>
              Close
            </Button>
          </div>
        </Stack>
      </Dialog>
      <ConfirmDialog
        opened={removing !== null}
        title={removing ? `Remove ${removing.name}'s access?` : "Remove this access?"}
        body={
          removing
            ? removing.subject_type === "team"
              ? `People in ${removing.name} will lose the access they get through the team.`
              : `${removing.name} will lose access to this ${objectLabel}.`
            : ""
        }
        action="Remove access"
        destructive
        busy={save.isPending}
        onClose={() => setRemoveKey(null)}
        onConfirm={() => {
          if (removing) revoke(removing);
        }}
      />
    </>
  );
}

function personLabel(member: Member) {
  return `${member.name} (${member.email})`;
}

function Visibility({
  objectLabel,
  visibility,
  editable,
  busy,
  teamWaiting,
  onChange,
}: {
  objectLabel: "task" | "chat";
  visibility: TaskVisibility;
  editable: boolean;
  busy: boolean;
  teamWaiting: boolean;
  onChange: (visibility: TaskVisibility) => void;
}) {
  const shown = visibilityOptions.find((option) => option.value === visibility);
  const heading = `Who can open this ${objectLabel}`;

  if (!editable) {
    return (
      <Stack gap={4}>
        <Text fw={600}>{heading}</Text>
        <Text>{shown?.label}</Text>
        <Text size="sm" c="dimmed">
          {shown?.description}
        </Text>
      </Stack>
    );
  }

  return (
    <Radio.Group
      label={heading}
      value={visibility}
      onChange={(value) => {
        if (isVisibility(value)) onChange(value);
      }}
    >
      <Stack gap="xs" mt="xs">
        {visibilityOptions.map((option) => (
          <Radio
            key={option.value}
            value={option.value}
            label={option.label}
            description={option.description}
            disabled={busy || (option.value === "team" && teamWaiting)}
          />
        ))}
      </Stack>
    </Radio.Group>
  );
}

function AddAccess({
  people,
  teams,
  personId,
  teamId,
  level,
  busy,
  onPerson,
  onTeam,
  onLevel,
  onAddPerson,
  onAddTeam,
}: {
  people: Member[];
  teams: Team[];
  personId: string;
  teamId: string;
  level: GrantableShareLevel;
  busy: boolean;
  onPerson: (id: string) => void;
  onTeam: (id: string) => void;
  onLevel: (level: GrantableShareLevel) => void;
  onAddPerson: () => void;
  onAddTeam: () => void;
}) {
  return (
    <Stack gap="xs">
      <Text fw={600}>Add access</Text>
      {people.length > 0 ? (
        <Select
          label="Person"
          placeholder="Choose a person"
          value={personId === "" ? null : personId}
          allowDeselect={false}
          disabled={busy}
          data={people.map((member) => ({ value: member.id, label: personLabel(member) }))}
          onChange={(next) => {
            if (next) onPerson(next);
          }}
        />
      ) : null}
      {teams.length > 0 ? (
        <Select
          label="Team"
          placeholder="Choose a team"
          value={teamId === "" ? null : teamId}
          allowDeselect={false}
          disabled={busy}
          data={teams.map((team) => ({ value: team.id, label: team.name }))}
          onChange={(next) => {
            if (next) onTeam(next);
          }}
        />
      ) : null}
      <Select
        label="Level"
        value={level}
        allowDeselect={false}
        disabled={busy}
        data={[
          { value: "editor", label: "Editor" },
          { value: "viewer", label: "Viewer" },
        ]}
        onChange={(next) => {
          if (next && isGrantableShareLevel(next)) onLevel(next);
        }}
      />
      <div className="record-actions">
        {people.length > 0 ? (
          <Button type="button" disabled={busy || personId === ""} onClick={onAddPerson}>
            Add person
          </Button>
        ) : null}
        {teams.length > 0 ? (
          <Button type="button" disabled={busy || teamId === ""} onClick={onAddTeam}>
            Add team
          </Button>
        ) : null}
      </div>
    </Stack>
  );
}
