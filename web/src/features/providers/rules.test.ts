import { describe, expect, it } from "vitest";

import { BrainError } from "../../app/api";
import {
  adapterKey,
  adapterLabel,
  baseUrlRequired,
  keyRequired,
  keysOffSentence,
  providerProblem,
  showsKeyField,
} from "./rules";

describe("adapterKey", () => {
  it("sends the fake adapter as fake and every other picker adapter as openai_compat", () => {
    expect(adapterKey("fake")).toBe("fake");
    for (const picked of ["anthropic", "openai", "gemini", "nebius", "ollama", "lmstudio", "openai_compat"]) {
      expect(adapterKey(picked)).toBe("openai_compat");
    }
  });
});

describe("adapterLabel", () => {
  it("names the brain's adapters and shows any other key as sent", () => {
    expect(adapterLabel("openai_compat")).toBe("OpenAI-compatible");
    expect(adapterLabel("fake")).toBe("Test adapter");
    expect(adapterLabel("anthropic")).toBe("anthropic");
    // A key that is also an object property name is not mistaken for a label.
    expect(adapterLabel("constructor")).toBe("constructor");
  });
});

describe("keyRequired", () => {
  it("needs a key for a metered or subscription account that has none saved", () => {
    expect(keyRequired("metered")).toBe(true);
    expect(keyRequired("subscription")).toBe(true);
  });

  it("lets a local account go without, and a saved key stand in for a typed one", () => {
    expect(keyRequired("local")).toBe(false);
    expect(keyRequired("metered", true)).toBe(false);
    expect(keyRequired("subscription", true)).toBe(false);
  });
});

describe("showsKeyField", () => {
  it("shows the field for a provider that takes a key, and for any provider off the local plan", () => {
    expect(showsKeyField(true, "local")).toBe(true);
    expect(showsKeyField(true, "metered")).toBe(true);
    expect(showsKeyField(false, "metered")).toBe(true);
    expect(showsKeyField(false, "subscription")).toBe(true);
  });

  it("hides the field only for a provider that takes no key on the local plan", () => {
    expect(showsKeyField(false, "local")).toBe(false);
  });
});

describe("baseUrlRequired", () => {
  it("requires a base URL for every adapter except the built in test one", () => {
    for (const picked of ["anthropic", "openai", "deepseek", "ollama", "lmstudio", "openai_compat"]) {
      expect(baseUrlRequired(picked)).toBe(true);
    }
    expect(baseUrlRequired("fake")).toBe(false);
  });
});

describe("providerProblem", () => {
  it("passes on the sentence the brain wrote for a refusal the person can fix", () => {
    expect(providerProblem(new BrainError(422, "The name must be 1 to 60 characters."))).toBe(
      "The name must be 1 to 60 characters.",
    );
    expect(providerProblem(new BrainError(409, "Used by route chat, agent-dispatch"))).toBe(
      "Used by route chat, agent-dispatch",
    );
    expect(providerProblem(new BrainError(409, "You already have a provider with that name."))).toBe(
      "You already have a provider with that name.",
    );
    expect(providerProblem(new BrainError(403, "This provider is shared. Only workspace managers can change it."))).toBe(
      "This provider is shared. Only workspace managers can change it.",
    );
    expect(providerProblem(new BrainError(429, "Too many tests. Try again in a minute."))).toBe(
      "Too many tests. Try again in a minute.",
    );
  });

  it("words a 503 as saving keys being turned off, whatever the brain said", () => {
    expect(providerProblem(new BrainError(503, "Saving provider keys is not set up on this server."))).toBe(
      keysOffSentence,
    );
    expect(keysOffSentence).toBe("Saving keys is turned off on this server.");
  });

  it("shows one fixed sentence for anything else, so no other text reaches the screen", () => {
    const fixed = "Something went wrong. Try again.";
    expect(providerProblem(new BrainError(500, "providers"))).toBe(fixed);
    expect(providerProblem(new BrainError(422))).toBe(fixed);
    expect(providerProblem(new BrainError(400, "bad request"))).toBe(fixed);
    expect(providerProblem(new TypeError("Failed to fetch https://x/?key=sk-secret"))).toBe(fixed);
    expect(providerProblem("sk-secret")).toBe(fixed);
  });
});
