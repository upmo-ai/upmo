export const workspaceRoles = ["owner", "admin", "member", "viewer"] as const;

export type WorkspaceRole = (typeof workspaceRoles)[number];

export const viewRoles = ["admin", "member", "viewer"] as const;

export type ViewRole = (typeof viewRoles)[number];

export const inviteRoles = ["admin", "member", "viewer"] as const;

export type InviteRole = (typeof inviteRoles)[number];

export interface Member {
  id: string;
  name: string;
  email: string;
  role: WorkspaceRole;
  teams: string[];
}

export interface CurrentUser {
  memberId: string;
  view: ViewRole;
  signIn: SignInMethod | null;
  passwordSet: boolean;
  /** How many passkeys the person has. Absent when the session does not say. */
  passkeyCount?: number;
  offerDismissed: boolean;
}

export type SignInMethod = "passkey" | "password" | "link";

export interface Passkey {
  id: string;
  name: string;
  added: string;
  lastUsed: string;
}

export const shareLevels = ["owner", "editor", "viewer"] as const;

export type ShareLevel = (typeof shareLevels)[number];

export const grantableShareLevels = ["editor", "viewer"] as const;

export type GrantableShareLevel = (typeof grantableShareLevels)[number];

export const taskVisibilities = ["private", "team", "everyone"] as const;

export type TaskVisibility = (typeof taskVisibilities)[number];

export type ShareSubjectType = "person" | "team";

/**
 * GET /api/tasks/{id}/share. The people and teams that hold a role on the
 * task, with the names the backend resolved for them.
 */
export interface ShareMember {
  subject_type: ShareSubjectType;
  subject_id: string;
  name: string;
  role: ShareLevel;
}

export interface TaskShare {
  visibility: TaskVisibility;
  /** The team a team visibility names. Absent for private and everyone. */
  team_id?: string;
  members: ShareMember[];
}

export interface ShareGrant {
  subject_type: ShareSubjectType;
  subject_id: string;
  role: ShareLevel;
}

/**
 * PUT /api/tasks/{id}/share. Replaces the visibility and the member list, and
 * answers with the stored TaskShare. Only the owner may write: anyone else
 * gets 403, and a 403 is not a sign-out. The owner entry must come back
 * unchanged.
 */
export interface ShareUpdate {
  visibility: TaskVisibility;
  /** Sent only for team visibility, when the owner must name one of their teams. */
  team_id?: string;
  members: ShareGrant[];
}

export interface Team {
  id: string;
  name: string;
}

export const scopeObjects = ["task", "chat", "folder", "automation", "agent"] as const;

export type ScopeObject = (typeof scopeObjects)[number];

export const scopeActions = ["create", "view", "edit", "share"] as const;

export type ScopeAction = (typeof scopeActions)[number];

export const tokenExpiries = [30, 90, 365] as const;

export type TokenExpiry = (typeof tokenExpiries)[number];

export interface ApiToken {
  id: string;
  name: string;
  hint: string;
  scopes: string[];
  created: string;
  lastUsed: string | null;
  expires: string;
}

export interface CreatedToken {
  token: ApiToken;
  secret: string;
}
