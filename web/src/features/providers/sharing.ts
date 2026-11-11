import type { ProviderAccount, ProviderAdd, ProviderChange } from "../../app/api";

/**
 * Who a provider account is for. A personal account is "own". An admin shares
 * an account with the "workspace" or with specific teams ("team").
 */
export type ProviderScope = "own" | "workspace" | "team";

/**
 * What the brain says about the sharing of an account. It is absent from an
 * older brain, which only knows "own" and "global" (the workspace's, read only).
 */
export interface ProviderShare {
  scope: ProviderScope;
  /** The teams a team account is shared with, and their names in the same order. */
  team_ids: string[];
  team_names: string[];
  /** Whether this person may change or disconnect the account. */
  can_manage: boolean;
}

/** An account as the list gives it: the account, and its sharing when the brain says it. */
export type ListedProvider = ProviderAccount & { share?: ProviderShare };

/** The sharing an admin chooses, as POST and PUT /api/providers take it. */
export type ShareChoice = { scope: "workspace" | "teams"; team_ids?: string[] };

/** A new account, with the sharing an admin chose. */
export type SharedAdd = ProviderAdd & Partial<ShareChoice>;

/** A change to an account, with the sharing an admin chose. */
export type SharedChange = ProviderChange & Partial<ShareChoice>;

/**
 * Whether this person may change or disconnect the account: what the brain
 * says, else their own accounts and not the workspace's.
 */
export function canManage(account: ListedProvider): boolean {
  return account.share ? account.share.can_manage : account.scope !== "global";
}

/** The label and sentence under a card that is shared. Nothing for a personal account. */
export function sharedNote(account: ListedProvider): { badge: string; text: string } | null {
  const share = account.share;
  if (!share) {
    return account.scope === "global" ? { badge: "Shared", text: "Managed by your workspace. Read only." } : null;
  }
  const readOnly = share.can_manage ? "" : " Read only.";
  switch (share.scope) {
    case "workspace":
      return { badge: "Workspace", text: `Shared with the whole workspace.${readOnly}` };
    case "team": {
      const names = share.team_names.length > 0 ? share.team_names.join(", ") : "your team";
      return { badge: "Team", text: `Shared with ${names}.${readOnly}` };
    }
    default:
      return null;
  }
}

/**
 * Whether this person may share accounts (hold teams:manage), as the brain
 * decides. An answer that is not a plain yes, a refusal, or no brain at all
 * reads as no, so the choice is never offered by mistake.
 */
export async function fetchCanShare(): Promise<boolean> {
  try {
    const response = await fetch("/api/providers/sharing", { credentials: "include" });
    if (!response.ok) return false;
    const body: unknown = await response.json();
    return typeof body === "object" && body !== null && (body as { can_share?: unknown }).can_share === true;
  } catch {
    return false;
  }
}
