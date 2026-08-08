import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { fresh, PasskeyRejected } from "./passkeys";

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

  it("fresh() on a cancelled prompt does not surface an error and does not run the change", async () => {
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

    // Mock navigator.credentials.get to simulate cancelled prompt
    vi.stubGlobal("navigator", {
      credentials: {
        get: vi.fn<() => Promise<Credential | null>>(async () => null),
      },
    });

    // fresh() catches the cancelled prompt and returns undefined without throwing
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
