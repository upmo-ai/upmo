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
  usable: true,
  usable_models: ["gpt-x"],
  models: ["gpt-4o"],
  team_ids: [] as string[],
  team_names: [] as string[],
  can_manage: true,
  can_use: true,
  is_owner: true,
};

const financeTeam = "01990000-0000-7000-8000-0000000000d1";

describe("toProvider with sharing", () => {
  it("reads a team account with its teams and whether the person may manage or use it", () => {
    const account = toProvider({
      ...base,
      visibility: "team",
      team_ids: [financeTeam],
      team_names: ["Finance"],
      can_manage: false,
      can_use: true,
    });
    expect(account.visibility).toBe("team");
    expect(account.team_ids).toEqual([financeTeam]);
    expect(account.team_names).toEqual(["Finance"]);
    expect(account.can_manage).toBe(false);
    expect(account.can_use).toBe(true);
  });

  it("reads a workspace account and a private account", () => {
    expect(toProvider({ ...base, visibility: "everyone", can_manage: true, can_use: true })).toMatchObject({
      visibility: "everyone",
      team_ids: [],
      team_names: [],
      can_manage: true,
      can_use: true,
    });
    expect(toProvider({ ...base, visibility: "private", can_manage: true, can_use: false })).toMatchObject({
      visibility: "private",
      can_manage: true,
      can_use: false,
    });
  });

  it("refuses a visibility the web does not know, and a team list that is not text", () => {
    expect(() => toProvider({ ...base, visibility: "workspace" })).toThrow("unknown visibility");
    expect(() => toProvider({ ...base, visibility: "team", team_ids: undefined })).toThrow("no team_ids");
    expect(() =>
      toProvider({ ...base, visibility: "team", team_ids: [7], team_names: ["Finance"], can_manage: true }),
    ).toThrow("no team_ids");
    expect(() => toProvider({ ...base, visibility: "everyone", team_names: "Finance" })).toThrow("no team_names");
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

  it("sends the teams only for a team account", () => {
    expect(providerAddBody({ ...add, visibility: "team", team_ids: [financeTeam] })).toMatchObject({
      visibility: "team",
      team_ids: [financeTeam],
    });
    const workspace = providerAddBody({ ...add, visibility: "everyone", team_ids: [financeTeam] });
    expect(workspace).toMatchObject({ visibility: "everyone" });
    expect(workspace).not.toHaveProperty("team_ids");
  });

  it("sends no sharing for a personal account, and none on a change that leaves it alone", () => {
    expect(providerAddBody(add)).not.toHaveProperty("visibility");
    expect(providerAddBody(add)).not.toHaveProperty("team_ids");
    expect(providerAddBody({ ...add, visibility: "private", team_ids: [financeTeam] })).not.toHaveProperty("visibility");
    expect(providerChangeBody({ name: "Renamed" })).toEqual({ name: "Renamed" });
  });

  it("sends the sharing of a change", () => {
    expect(providerChangeBody({ visibility: "team", team_ids: [financeTeam] })).toEqual({
      visibility: "team",
      team_ids: [financeTeam],
    });
    expect(providerChangeBody({ visibility: "everyone" })).toEqual({ visibility: "everyone" });
    expect(providerChangeBody({ visibility: "private" })).toEqual({ visibility: "private" });
    expect(providerChangeBody({ visibility: "private", team_ids: [financeTeam] })).toEqual({
      visibility: "private",
    });
  });
});
