import { describe, expect, it } from "vitest";

import type { AgentProfile, ProviderAccount } from "../../app/api";
import type { GatewayRoute } from "../models/types";
import { autoPicks, chatRoutes, modelChoices, modelDescription, pickableAgents, validPicks } from "./picks";

const account = (id: string, provider: string, over: Partial<ProviderAccount> = {}): ProviderAccount => ({
  id,
  provider,
  name: `${provider} account`,
  adapter: "openai_compat",
  plan_kind: "metered",
  status: "Connected",
  models: ["m1"],
  usable: true,
  usable_models: ["m1"],
  scope: "own",
  ...over,
});

const route = (name: string, over: Partial<GatewayRoute> = {}): GatewayRoute => ({
  name,
  scope: "global",
  capability: "chat",
  decision: "",
  usable: true,
  hops: [],
  ...over,
});

const agent = (id: string, over: Partial<AgentProfile> = {}): AgentProfile => ({
  id,
  name: id,
  description: "",
  prompt_overlay: "",
  route: "",
  model_provider: "",
  model_id: "",
  enabled: true,
  usable: true,
  scope: "own",
  editable: true,
  ...over,
});

describe("UPM-265 picker filters", () => {
  it("offers only the usable models of the accounts", () => {
    const accounts = [
      account("a1", "openai", { models: ["m1", "m2"], usable_models: ["m1", "m2"] }),
      account("a2", "anthropic", { status: "Not connected", usable: false, usable_models: [], models: ["claude"] }),
    ];
    expect(modelChoices(accounts).map((choice) => choice.model)).toEqual(["m1", "m2"]);
    // The pin still carries the account the model came from.
    expect(modelChoices(accounts)[0]).toMatchObject({ account_id: "a1", provider: "openai" });
  });

  it("offers chat and stream routes that are usable, and marks the rest inactive", () => {
    const routes = [
      route("chat"),
      route("chat-stream", { capability: "stream" }),
      route("embed", { capability: "embed" }),
      route("decide", { capability: "decide", decision: "yesno" }),
      route("dead", { usable: false }),
    ];
    expect(chatRoutes(routes).map((item) => item.name)).toEqual(["chat", "chat-stream"]);
  });

  it("offers enabled agents whose model or route resolves, and no others", () => {
    const agents = [
      agent("working"),
      agent("broken", { usable: false }),
      agent("off", { enabled: false }),
      agent("off-and-broken", { enabled: false, usable: false }),
    ];
    expect(pickableAgents(agents).map((item) => item.id)).toEqual(["working"]);
  });

  it("returns a remembered pick to Auto when it is no longer offered", () => {
    const agents = [agent("working"), agent("broken", { usable: false })];
    const routes = [route("chat"), route("dead", { usable: false })];
    const models = modelChoices([account("a1", "openai"), account("a2", "anthropic", { usable: false, usable_models: [] })]);
    // The page hands the filtered lists over, exactly as the pickers show them.
    const picks = validPicks(
      { agent: "broken", route: "dead", model: "a2/m1" },
      pickableAgents(agents),
      chatRoutes(routes),
      models,
    );
    expect(picks).toEqual(autoPicks);
  });
});

describe("modelDescription", () => {
  const sharedAccount = {
    id: "account-1",
    provider: "openai",
    name: "Shared models",
    adapter: "openai_compat",
    plan_kind: "local" as const,
    status: "Connected" as const,
    models: ["gpt-x"],
    usable: true,
    usable_models: ["gpt-x"],
    scope: "global" as const,
  };

  it("labels workspace shares, team shares, and older workspace accounts", () => {
    const workspace = modelChoices([{ ...sharedAccount, share: { scope: "workspace", team_ids: [], team_names: [], can_manage: false } }])[0];
    const team = modelChoices([
      { ...sharedAccount, share: { scope: "team", team_ids: ["team-1"], team_names: ["Finance", "Ops"], can_manage: false } },
    ])[0];
    const unnamedTeam = modelChoices([
      { ...sharedAccount, share: { scope: "team", team_ids: ["team-1"], team_names: [], can_manage: false } },
    ])[0];

    expect(modelDescription(workspace)).toBe("Shared models (workspace)");
    expect(modelDescription(team)).toBe("Shared models (Finance, Ops)");
    expect(modelDescription(unnamedTeam)).toBe("Shared models (team)");
    expect(modelDescription(modelChoices([sharedAccount])[0])).toBe("Shared models (workspace)");
  });
});
