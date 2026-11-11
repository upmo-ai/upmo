import { describe, expect, it } from "vitest";

import { modelChoices, modelDescription } from "./picks";

describe("modelDescription", () => {
  const account = {
    id: "account-1",
    provider: "openai",
    name: "Shared models",
    adapter: "openai_compat",
    plan_kind: "local" as const,
    status: "Connected" as const,
    models: ["gpt-x"],
    scope: "global" as const,
  };

  it("labels workspace shares, team shares, and older workspace accounts", () => {
    const workspace = modelChoices([{ ...account, share: { scope: "workspace", team_ids: [], team_names: [], can_manage: false } }])[0];
    const team = modelChoices([
      { ...account, share: { scope: "team", team_ids: ["team-1"], team_names: ["Finance", "Ops"], can_manage: false } },
    ])[0];
    const unnamedTeam = modelChoices([
      { ...account, share: { scope: "team", team_ids: ["team-1"], team_names: [], can_manage: false } },
    ])[0];

    expect(modelDescription(workspace)).toBe("Shared models (workspace)");
    expect(modelDescription(team)).toBe("Shared models (Finance, Ops)");
    expect(modelDescription(unnamedTeam)).toBe("Shared models (team)");
    expect(modelDescription(modelChoices([account])[0])).toBe("Shared models (workspace)");
  });
});
