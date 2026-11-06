import {
  grantableShareLevels,
  inviteRoles,
  scopeActions,
  scopeObjects,
  type GrantableShareLevel,
  type InviteRole,
  type ScopeAction,
  type ScopeObject,
  type ShareLevel,
  type ShareUpdate,
  type TaskShare,
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

export function showPasskeyOffer(input: {
  method: "passkey" | "password" | null;
  dismissed: boolean;
}): boolean {
  return input.method === "password" && !input.dismissed;
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
    members: share.members.map(({ subject_type, subject_id, role }) => ({
      subject_type,
      subject_id,
      role,
    })),
  };
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
