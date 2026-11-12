import type { ProviderAccount } from "../../app/api";

/** An account as the list gives it. */
export type ListedProvider = ProviderAccount;

/** Anything with visibility and team names, for the shared label. */
export type ShareListed = Pick<ListedProvider, "visibility" | "team_names" | "can_manage">;

/**
 * Whether this person may change or disconnect the account, as the brain says
 * with can_manage.
 */
export function canManage(account: ListedProvider): boolean {
  return account.can_manage;
}

/** The label and sentence under a card that is shared. Nothing for a personal account. */
export function sharedNote(account: ShareListed): { badge: string; text: string } | null {
  const readOnly = account.can_manage ? "" : " Read only.";
  switch (account.visibility) {
    case "everyone":
      return { badge: "Workspace", text: `Shared with the whole workspace.${readOnly}` };
    case "team": {
      const names = account.team_names.length > 0 ? account.team_names.join(", ") : "your team";
      return { badge: "Teams", text: `Shared with ${names}.${readOnly}` };
    }
    default:
      return null;
  }
}

/**
 * Whether this person may share accounts (hold settings:manage), as the brain
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
