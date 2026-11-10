import { describe, expect, it } from "vitest";

import { BrainError, type AgentProfile, type ProviderAccount } from "../../app/api";
import {
  agentProblem,
  changesOf,
  choiceOf,
  choiceValue,
  draftOf,
  draftProblem,
  emptyDraft,
  modelChoices,
  nameProblem,
  routeProblem,
  trimmedDraft,
} from "./rules";

const agent: AgentProfile = {
  id: "a1",
  name: "Research",
  description: "d",
  prompt_overlay: "Be brief.",
  route: "deep",
  model_provider: "OpenAI",
  model_id: "gpt-x",
  enabled: true,
  scope: "own",
  editable: true,
};

describe("agent rules", () => {
  it("asks for a name of 1 to 60 characters", () => {
    expect(nameProblem("")).toBe("Give the agent a name.");
    expect(nameProblem("   ")).toBe("Give the agent a name.");
    expect(nameProblem("é".repeat(60))).toBeNull();
    expect(nameProblem("n".repeat(61))).toBe("The name must be 1 to 60 characters.");
  });

  it("takes a route name or nothing", () => {
    expect(routeProblem("")).toBeNull();
    expect(routeProblem(" fast ")).toBeNull();
    expect(routeProblem("has space")).toBe("The route is not a valid route name.");
    expect(routeProblem("-lead")).toBe("The route is not a valid route name.");
  });

  it("reports the first problem of a draft and none for a good one", () => {
    expect(draftProblem(emptyDraft())).toBe("Give the agent a name.");
    expect(draftProblem({ ...emptyDraft(), name: "a" })).toBeNull();
    expect(draftProblem({ ...emptyDraft(), name: "a", prompt_overlay: "o".repeat(4001) })).toBe(
      "The instructions must be at most 4000 characters.",
    );
    expect(draftProblem({ ...emptyDraft(), name: "a", description: "d".repeat(301) })).toBe(
      "The description must be at most 300 characters.",
    );
  });

  it("trims every text before it is sent", () => {
    expect(trimmedDraft({ ...emptyDraft(), name: " a ", description: " d ", prompt_overlay: " p ", route: " r " })).toEqual({
      ...emptyDraft(),
      name: "a",
      description: "d",
      prompt_overlay: "p",
      route: "r",
    });
  });

  it("sends only what changed, and an empty string to clear", () => {
    expect(changesOf(agent, draftOf(agent))).toEqual({});
    expect(changesOf(agent, { ...draftOf(agent), name: " Renamed ", route: "", enabled: false })).toEqual({
      name: "Renamed",
      route: "",
      enabled: false,
    });
    expect(changesOf(agent, { ...draftOf(agent), model_provider: "", model_id: "" })).toEqual({
      model_provider: "",
      model_id: "",
    });
  });

  it("lists each provider and model once from the accounts", () => {
    const account = (id: string, provider: string, models: string[]): ProviderAccount => ({
      id,
      provider,
      name: id,
      adapter: "openai_compat",
      plan_kind: "metered",
      status: "Connected",
      models,
      scope: "own",
    });
    const choices = modelChoices([
      account("a", "OpenAI", ["gpt-x", "meta/llama"]),
      account("b", "OpenAI", ["gpt-x"]),
      account("c", "Nebius", ["gpt-x"]),
    ]);
    expect(choices).toEqual([
      { provider: "OpenAI", model: "gpt-x" },
      { provider: "OpenAI", model: "meta/llama" },
      { provider: "Nebius", model: "gpt-x" },
    ]);
  });

  it("round-trips a choice whatever the model id holds", () => {
    const choice = { provider: "a::b", model: 'we"ird/model:1' };
    expect(choiceOf(choiceValue(choice))).toEqual(choice);
    expect(choiceOf("not a choice")).toBeNull();
    expect(choiceOf('["only one"]')).toBeNull();
  });

  it("passes on only a sentence the brain wrote", () => {
    expect(agentProblem(new BrainError(409, "An agent with that name already exists."))).toBe(
      "An agent with that name already exists.",
    );
    expect(agentProblem(new BrainError(422, "Give the agent a name."))).toBe("Give the agent a name.");
    expect(agentProblem(new BrainError(500, "boom with secrets"))).toBe("Something went wrong. Try again.");
    expect(agentProblem(new Error("network down"))).toBe("Something went wrong. Try again.");
  });
});
