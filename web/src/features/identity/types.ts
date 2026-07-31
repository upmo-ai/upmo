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
  signIn: "passkey" | "password" | null;
  passwordSet: boolean;
  offerDismissed: boolean;
}

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

export interface PersonAccess {
  memberId: string;
  level: ShareLevel;
}

export interface TaskShare {
  taskId: string;
  visibility: TaskVisibility;
  teamId: string | null;
  people: PersonAccess[];
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
