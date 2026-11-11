import { describe, expect, it } from "vitest";

import { providerAddBody, providerChangeBody, toProvider } from "./brain-api";

const base = {
  id: "01990000-0000-7000-8000-0000000000e1",
  provider: "openai",
  name: "Finance GPUs",
  adapter: "openai_compat",
  plan_kind: "metered",
  base_url: "https://api.openai.com",
  status: "Connected",
  models: ["gpt-4o"],
};

const financeTeam = "01990000-0000-7000-8000-0000000000d1";

describe("toProvider with sharing", () => {
  it("reads a team account with its teams and whether the person may manage it", () => {
    const account = toProvider({
      ...base,
      scope: "team",
      team_ids: [financeTeam],
      team_names: ["Finance"],
      can_manage: false,
    });
    // The account is still the workspace's as far as the old reading goes: shared and not the person's own.
    expect(account.scope).toBe("global");
    expect(account.share).toEqual({
      scope: "team",
      team_ids: [financeTeam],
      team_names: ["Finance"],
      can_manage: false,
    });
  });

  it("reads a workspace account, and an account of the person's own, with what the brain says about managing", () => {
    expect(toProvider({ ...base, scope: "workspace", team_ids: [], team_names: [], can_manage: true })).toMatchObject({
      scope: "global",
      share: { scope: "workspace", team_ids: [], team_names: [], can_manage: true },
    });
    expect(toProvider({ ...base, scope: "own", team_ids: [], team_names: [], can_manage: true })).toMatchObject({
      scope: "own",
      share: { scope: "own", can_manage: true },
    });
  });

  it("reads an older brain's global account as the workspace's, with no sharing to show", () => {
    const account = toProvider({ ...base, scope: "global" });
    expect(account.scope).toBe("global");
    expect(account).not.toHaveProperty("share");
  });

  it("refuses a team account that names no teams, and a team list that is not text", () => {
    expect(() => toProvider({ ...base, scope: "team", can_manage: true })).toThrow("unknown scope");
    expect(() =>
      toProvider({ ...base, scope: "team", team_ids: [7], team_names: ["Finance"], can_manage: true }),
    ).toThrow("no team_ids");
    expect(() => toProvider({ ...base, scope: "workspace", team_names: "Finance", can_manage: true })).toThrow(
      "no team_names",
    );
  });
});

describe("provider request bodies with sharing", () => {
  const add = {
    provider: "openai",
    name: "Finance GPUs",
    adapter: "openai",
    plan_kind: "metered" as const,
    base_url: "https://api.openai.com",
    api_key: "sk-1",
    models: ["gpt-4o"],
  };

  it("sends the teams only for a share with specific teams", () => {
    expect(providerAddBody({ ...add, scope: "teams", team_ids: [financeTeam] })).toMatchObject({
      scope: "teams",
      team_ids: [financeTeam],
    });
    const workspace = providerAddBody({ ...add, scope: "workspace", team_ids: [financeTeam] });
    expect(workspace).toMatchObject({ scope: "workspace" });
    expect(workspace).not.toHaveProperty("team_ids");
  });

  it("sends no sharing for a personal account, and none on a change that leaves it alone", () => {
    expect(providerAddBody(add)).not.toHaveProperty("scope");
    expect(providerAddBody(add)).not.toHaveProperty("team_ids");
    expect(providerChangeBody({ name: "Renamed" })).toEqual({ name: "Renamed" });
  });

  it("sends the sharing of a change", () => {
    expect(providerChangeBody({ scope: "teams", team_ids: [financeTeam] })).toEqual({
      scope: "teams",
      team_ids: [financeTeam],
    });
    expect(providerChangeBody({ scope: "workspace" })).toEqual({ scope: "workspace" });
  });
});
