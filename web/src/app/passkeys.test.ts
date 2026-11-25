import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BrainError } from "./api";
import { fresh, PasskeyRejected, withStepUp } from "./passkeys";

describe("passkey step-up and fresh()", () => {
  beforeEach(() => {
    vi.stubGlobal("window", {
      location: { hostname: "localhost" },
      PublicKeyCredential: {
        isConditionalMediationAvailable: async () => false,
        parseCreationOptionsFromJSON: (options: unknown) => options,
        parseRequestOptionsFromJSON: (options: unknown) => options,
      },
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fresh() on a canceled prompt does not surface an error and does not run the change", async () => {
    let runs = 0;
    const action = vi.fn<() => Promise<never>>(async () => {
      runs += 1;
      throw new PasskeyRejected(403, "sign in again");
    });

    vi.stubGlobal(
      "fetch",
      vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
        if (String(input).includes("/api/passkeys/step-up/options")) {
          return new Response(JSON.stringify({ publicKey: {} }), {
            status: 200,
            headers: { "content-type": "application/json" },
          });
        }
        return new Response("{}", { status: 200 });
      }),
    );

    // Mock navigator.credentials.get to simulate canceled prompt
    vi.stubGlobal("navigator", {
      credentials: {
        get: vi.fn<() => Promise<Credential | null>>(async () => null),
      },
    });

    // fresh() catches the canceled prompt and returns undefined without throwing
    const result = await fresh(action);
    expect(result).toBeUndefined();
    // The action ran once, failed, and did not run again after cancellation
    expect(runs).toBe(1);
  });

  it("a 403 that is not 'sign in again' surfaces as an error", async () => {
    let runs = 0;
    const action = vi.fn<() => Promise<never>>(async () => {
      runs += 1;
      throw new PasskeyRejected(403, "forbidden");
    });

    await expect(fresh(action)).rejects.toThrow("passkey 403: forbidden");
    expect(runs).toBe(1);
  });

  it("retries and runs the change when step-up succeeds", async () => {
    let runs = 0;
    const action = vi.fn<() => Promise<string>>(async () => {
      runs += 1;
      if (runs === 1) {
        throw new PasskeyRejected(403, "sign in again");
      }
      return "success";
    });

    vi.stubGlobal(
      "fetch",
      vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
        if (String(input).includes("/api/passkeys/step-up/options")) {
          return new Response(JSON.stringify({ publicKey: {} }), {
            status: 200,
            headers: { "content-type": "application/json" },
          });
        }
        if (String(input).includes("/api/passkeys/step-up/finish")) {
          return new Response("{}", { status: 200 });
        }
        return new Response("{}", { status: 200 });
      }),
    );

    vi.stubGlobal("navigator", {
      credentials: {
        get: vi.fn<() => Promise<unknown>>(async () => ({
          toJSON: () => ({ id: "cred-1" }),
        })),
      },
    });

    const result = await fresh(action);
    expect(result).toBe("success");
    expect(runs).toBe(2);
  });
});

describe("withStepUp()", () => {
  let paths: string[];

  // The brain's step-up routes answer; every request is recorded by path.
  const stubFetch = () => {
    paths = [];
    vi.stubGlobal(
      "fetch",
      vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
        const path = String(input);
        paths.push(path);
        if (path === "/api/passkeys/step-up/options") {
          return new Response(JSON.stringify({ publicKey: { challenge: "c" } }), {
            status: 200,
            headers: { "content-type": "application/json" },
          });
        }
        return new Response("{}", { status: 200 });
      }),
    );
  };

  const stubPrompt = (answer: () => Promise<unknown>) => {
    const get = vi.fn<() => Promise<unknown>>(answer);
    vi.stubGlobal("navigator", { credentials: { get } });
    return get;
  };

  beforeEach(() => {
    vi.stubGlobal("window", {
      location: { hostname: "localhost" },
      PublicKeyCredential: {
        parseCreationOptionsFromJSON: (options: unknown) => options,
        parseRequestOptionsFromJSON: (options: unknown) => options,
      },
    });
    stubFetch();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("on a stale session asks for the passkey once and returns the retried result", async () => {
    const get = stubPrompt(async () => ({ toJSON: () => ({ id: "cred-1" }) }));
    let runs = 0;
    const result = await withStepUp(async () => {
      runs += 1;
      if (runs === 1) throw new BrainError(403, "sign in again");
      return "saved";
    });
    expect(result).toBe("saved");
    expect(runs).toBe(2);
    expect(get).toHaveBeenCalledTimes(1);
    expect(paths).toEqual(["/api/passkeys/step-up/options", "/api/passkeys/step-up/finish"]);
  });

  it("on a canceled prompt throws the original error and does not retry", async () => {
    stubPrompt(async () => {
      throw new DOMException("The operation either timed out or was not allowed.", "NotAllowedError");
    });
    const stale = new BrainError(403, "sign in again", "Failed to save connector");
    let runs = 0;
    await expect(
      withStepUp(async () => {
        runs += 1;
        throw stale;
      }),
    ).rejects.toBe(stale);
    expect(runs).toBe(1);
    expect(paths).toEqual(["/api/passkeys/step-up/options"]);
  });

  it.each([
    ["a 403 that is not sign in again", new BrainError(403, "forbidden")],
    ["a 500", new BrainError(500, "sign in again")],
  ])("passes %s through without a prompt", async (_name, failure) => {
    const get = stubPrompt(async () => ({ toJSON: () => ({}) }));
    let runs = 0;
    await expect(
      withStepUp(async () => {
        runs += 1;
        throw failure;
      }),
    ).rejects.toBe(failure);
    expect(runs).toBe(1);
    expect(get).not.toHaveBeenCalled();
    expect(paths).toEqual([]);
  });
});
