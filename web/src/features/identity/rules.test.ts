import { describe, expect, it } from "vitest";

import {
  actionsForRole,
  canChangeOwnerRole,
  canEditSharing,
  canManageMembers,
  canRemoveMember,
  canRemovePasskey,
  canRemovePassword,
  canRemoveShare,
  canSetMemberRole,
  grantableShareLevels,
  inviteRoles,
  isAllowedScope,
  ownerLockReason,
  scopeObjects,
  showPasskeyOffer,
} from "./rules";

const forbidden = [
  "task:delete",
  "chat:delete",
  "folder:delete",
  "automation:delete",
  "agent:delete",
  "members:manage",
  "teams:manage",
  "roles:manage",
  "settings:manage",
  "connectors:manage",
  "owners:manage",
  "workspace:delete",
];

describe("last sign-in method", () => {
  it("keeps the last passkey when no password is set", () => {
    expect(canRemovePasskey(1, false)).toBe(false);
    expect(canRemovePasskey(2, false)).toBe(true);
    expect(canRemovePasskey(1, true)).toBe(true);
    expect(canRemovePasskey(0, true)).toBe(false);
  });

  it("keeps the password when there are no passkeys", () => {
    expect(canRemovePassword(0, true)).toBe(false);
    expect(canRemovePassword(1, true)).toBe(true);
    expect(canRemovePassword(2, false)).toBe(false);
  });
});

describe("passkey offer", () => {
  it("shows only after a password sign-in that has not been dismissed", () => {
    expect(showPasskeyOffer({ method: "password", dismissed: false })).toBe(true);
    expect(showPasskeyOffer({ method: "password", dismissed: true })).toBe(false);
    expect(showPasskeyOffer({ method: "passkey", dismissed: false })).toBe(false);
    expect(showPasskeyOffer({ method: null, dismissed: false })).toBe(false);
  });

  it("passkey offer: never follows a one-time link sign-in", () => {
    expect(showPasskeyOffer({ method: "link", dismissed: false })).toBe(false);
    expect(showPasskeyOffer({ method: "link", dismissed: false, passkeyCount: 0 })).toBe(false);
  });

  it("passkey offer: is hidden once the person has a passkey, and not hidden when the count is unknown", () => {
    expect(showPasskeyOffer({ method: "password", dismissed: false, passkeyCount: 0 })).toBe(true);
    expect(showPasskeyOffer({ method: "password", dismissed: false, passkeyCount: 1 })).toBe(false);
    expect(showPasskeyOffer({ method: "password", dismissed: false, passkeyCount: undefined })).toBe(true);
  });
});

describe("last owner", () => {
  it("blocks demotion and removal of the last owner", () => {
    expect(canChangeOwnerRole("owner", 1)).toBe(false);
    expect(canChangeOwnerRole("admin", 1)).toBe(false);
    expect(canChangeOwnerRole("owner", 2)).toBe(true);
    expect(canChangeOwnerRole("admin", 2)).toBe(false);
    expect(ownerLockReason("admin", 1)).toBe("The last owner cannot be demoted or removed.");
    expect(ownerLockReason("admin", 2)).toBe("Only an owner can change the owner role.");
    expect(ownerLockReason("owner", 2)).toBeNull();
    expect(
      canRemoveMember({ actor: "admin", target: "owner", ownerCount: 1 }),
    ).toBe(false);
    expect(
      canSetMemberRole({ actor: "admin", target: "owner", next: "member", ownerCount: 1 }),
    ).toBe(false);
    expect(
      canSetMemberRole({ actor: "owner", target: "member", next: "owner", ownerCount: 2 }),
    ).toBe(false);
  });
});

describe("who may manage members", () => {
  it("allows an owner or an admin", () => {
    expect(canManageMembers("owner")).toBe(true);
    expect(canManageMembers("admin")).toBe(true);
    expect(canManageMembers("member")).toBe(false);
    expect(canManageMembers("viewer")).toBe(false);
    expect(inviteRoles).toEqual(["admin", "member", "viewer"]);
  });
});

describe("who may edit sharing", () => {
  it("UPM-205: allows only the owner, not an editor", () => {
    expect(canEditSharing("owner")).toBe(true);
    expect(canEditSharing("editor")).toBe(false);
    expect(canEditSharing("viewer")).toBe(false);
    expect(canEditSharing(null)).toBe(false);
    expect(canRemoveShare("owner")).toBe(false);
    expect(canRemoveShare("editor")).toBe(true);
    expect(grantableShareLevels).toEqual(["editor", "viewer"]);
  });
});

describe("scopes a role may grant", () => {
  it("limits a viewer to view", () => {
    expect(actionsForRole("viewer")).toEqual(["view"]);
    expect(isAllowedScope("viewer", "task:view")).toBe(true);
    expect(isAllowedScope("viewer", "task:edit")).toBe(false);
  });

  it("lets a member, admin, or owner grant create, view, edit, and share", () => {
    for (const role of ["member", "admin", "owner"] as const) {
      expect(actionsForRole(role)).toEqual(["create", "view", "edit", "share"]);
      expect(isAllowedScope(role, "agent:share")).toBe(true);
    }
  });

  it("never offers delete or a workspace permission", () => {
    const granted = new Set(
      scopeObjects.flatMap((object) => actionsForRole("owner").map((action) => `${object}:${action}`)),
    );
    for (const scope of forbidden) {
      expect(granted.has(scope)).toBe(false);
      expect(isAllowedScope("owner", scope)).toBe(false);
    }
  });
});
