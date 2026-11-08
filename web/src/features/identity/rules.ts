import {
  grantableShareLevels,
  inviteRoles,
  scopeActions,
  scopeObjects,
  type GrantableShareLevel,
  type InviteRole,
  type Member,
  type ScopeAction,
  type ScopeObject,
  type ShareLevel,
  type ShareUpdate,
  type SignInMethod,
  type TaskShare,
  type Team,
  type ViewRole,
  type WorkspaceRole,
} from "./types";

export { grantableShareLevels, inviteRoles, scopeActions, scopeObjects };

export const lastPasskeyReason =
  "You cannot remove the last passkey when no password is set.";

export const lastPasswordReason =
  "You cannot remove the password when there are no passkeys.";

export const lastOwnerReason = "The last owner cannot be demoted or removed.";

export const ownerOnlyReason = "Only an owner can change the owner role.";

/** A signed-in person keeps at least one way to sign in. */
export function canRemovePasskey(passkeyCount: number, passwordSet: boolean): boolean {
  if (passkeyCount <= 1 && !passwordSet) return false;
  return passkeyCount > 0;
}

export function canRemovePassword(passkeyCount: number, passwordSet: boolean): boolean {
  return passwordSet && passkeyCount > 0;
}

/**
 * Offered only after a password sign-in, once, to someone with no passkey.
 * An unknown passkey count does not hide the offer.
 */
export function showPasskeyOffer(input: {
  method: SignInMethod | null;
  dismissed: boolean;
  passkeyCount?: number;
}): boolean {
  if (input.method !== "password" || input.dismissed) return false;
  return !(input.passkeyCount !== undefined && input.passkeyCount > 0);
}

export function canManageMembers(role: WorkspaceRole): boolean {
  return role === "owner" || role === "admin";
}

/** The last owner stays, and only an owner may change an owner. */
export function canChangeOwnerRole(actor: WorkspaceRole, ownerCount: number): boolean {
  return actor === "owner" && ownerCount > 1;
}

export function ownerLockReason(actor: WorkspaceRole, ownerCount: number): string | null {
  if (canChangeOwnerRole(actor, ownerCount)) return null;
  if (ownerCount <= 1) return lastOwnerReason;
  return ownerOnlyReason;
}

export function canSetMemberRole(input: {
  actor: WorkspaceRole;
  target: WorkspaceRole;
  next: WorkspaceRole;
  ownerCount: number;
}): boolean {
  if (!canManageMembers(input.actor)) return false;
  if (input.next === "owner") return false;
  if (input.target === "owner") return canChangeOwnerRole(input.actor, input.ownerCount);
  return isInviteRole(input.next);
}

export function canRemoveMember(input: {
  actor: WorkspaceRole;
  target: WorkspaceRole;
  ownerCount: number;
}): boolean {
  if (!canManageMembers(input.actor)) return false;
  if (input.target === "owner") return canChangeOwnerRole(input.actor, input.ownerCount);
  return true;
}

/** Only the owner of a task may change who it is shared with. */
export function canEditSharing(role: ShareLevel | null): boolean {
  return role === "owner";
}

/** The role a person holds directly on the task, or null when they hold none. */
export function shareRole(share: TaskShare | undefined, memberId: string | undefined): ShareLevel | null {
  if (!share || !memberId) return null;
  const held = share.members.find(
    (member) => member.subject_type === "person" && member.subject_id === memberId,
  );
  return held?.role ?? null;
}

export function toShareUpdate(share: TaskShare): ShareUpdate {
  return {
    visibility: share.visibility,
    ...(share.team_id ? { team_id: share.team_id } : {}),
    members: share.members.map(({ subject_type, subject_id, role }) => ({
      subject_type,
      subject_id,
      role,
    })),
  };
}

/**
 * The teams a person is on, of every team: the roster entry carries team
 * names, and a share names a team by its id.
 */
export function teamsOfPerson(person: Member | undefined, teams: Team[]): Team[] {
  if (!person) return [];
  return teams.filter((team) => person.teams.includes(team.name));
}

/**
 * The team a team visibility names: the saved team while the owner still
 * holds it, else the first of theirs. Undefined when there is no choice to
 * make, so the backend resolves the only team or refuses with its message.
 */
export function sharedTeamOf(share: TaskShare, ownerTeams: Team[]): string | undefined {
  if (ownerTeams.length < 2) return undefined;
  if (share.team_id && ownerTeams.some((team) => team.id === share.team_id)) return share.team_id;
  return ownerTeams[0]?.id;
}

export function canRemoveShare(level: ShareLevel): boolean {
  return level !== "owner";
}

export function isInviteRole(value: string): value is InviteRole {
  return (inviteRoles as readonly string[]).includes(value);
}

export function isViewRole(value: string): value is ViewRole {
  return value === "admin" || value === "member" || value === "viewer";
}

export function isGrantableShareLevel(value: string): value is GrantableShareLevel {
  return (grantableShareLevels as readonly string[]).includes(value);
}

export function actionsForRole(role: WorkspaceRole): readonly ScopeAction[] {
  if (role === "viewer") return ["view"];
  return scopeActions;
}

export function isAllowedScope(role: WorkspaceRole, scope: string): boolean {
  const parts = scope.split(":");
  if (parts.length !== 2) return false;
  const object = parts[0];
  const action = parts[1];
  if (!object || !action) return false;
  if (!(scopeObjects as readonly string[]).includes(object)) return false;
  return (actionsForRole(role) as readonly string[]).includes(action);
}

export function isScopeObject(value: string): value is ScopeObject {
  return (scopeObjects as readonly string[]).includes(value);
}
