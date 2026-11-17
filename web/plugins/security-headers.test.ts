import { describe, expect, it } from "vitest";
import { DEVELOPMENT_CSP, PRODUCTION_CSP, documentHeaders } from "./security-headers";

// Written out here and not derived from the plugin, so a wrong directive in
// the plugin cannot make this test agree with it.
const PRODUCTION =
  "default-src 'self'; script-src 'self'; style-src 'self'; style-src-elem 'self' 'unsafe-inline'; " +
  "style-src-attr 'unsafe-inline'; font-src 'self'; " +
  "img-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; frame-src 'self' about:; " +
  "frame-ancestors 'none'; object-src 'none'; base-uri 'self'; form-action 'self'";

const directive = (policy: string, name: string) =>
  policy
    .split("; ")
    .find((part) => part.startsWith(`${name} `))
    ?.slice(name.length + 1);

describe("UPM-282 document security headers", () => {
  it("locks the production policy and keeps script-src to 'self'", () => {
    expect(PRODUCTION_CSP).toBe(PRODUCTION);
    expect(directive(PRODUCTION_CSP, "script-src")).toBe("'self'");
    expect(directive(PRODUCTION_CSP, "style-src")).toBe("'self'");
    expect(directive(PRODUCTION_CSP, "style-src-elem")).toBe("'self' 'unsafe-inline'");
    expect(directive(PRODUCTION_CSP, "font-src")).toBe("'self'");
    expect(PRODUCTION_CSP.match(/unsafe-inline/g)).toHaveLength(2);
    expect(directive(PRODUCTION_CSP, "style-src-attr")).toBe("'unsafe-inline'");
    expect(PRODUCTION_CSP).not.toContain("unsafe-eval");
  });

  it("lets only the dev policy run inline scripts and connect over WebSocket", () => {
    expect(directive(DEVELOPMENT_CSP, "script-src")).toBe("'self' 'unsafe-inline'");
    expect(directive(DEVELOPMENT_CSP, "connect-src")).toBe("'self' ws: wss:");
    expect(directive(DEVELOPMENT_CSP, "style-src-elem")).toBe("'self' 'unsafe-inline'");
    expect(
      DEVELOPMENT_CSP.replace("script-src 'self' 'unsafe-inline';", "script-src 'self';").replace(" ws: wss:", ""),
    ).toBe(PRODUCTION);
    expect(directive(PRODUCTION_CSP, "script-src")).not.toContain("unsafe-inline");
  });

  it("sends HSTS only for a secure request and allows passkey sign-in", () => {
    const plain = documentHeaders(PRODUCTION_CSP, false);
    const secure = documentHeaders(PRODUCTION_CSP, true);
    expect(plain["Strict-Transport-Security"]).toBeUndefined();
    expect(secure["Strict-Transport-Security"]).toBe("max-age=15552000");
    expect(plain["X-Content-Type-Options"]).toBe("nosniff");
    expect(plain["X-Frame-Options"]).toBe("DENY");
    expect(plain["Referrer-Policy"]).toBe("strict-origin-when-cross-origin");
    expect(plain["Permissions-Policy"]).toContain("publickey-credentials-get=(self)");
    expect(plain["Permissions-Policy"]).not.toContain("publickey-credentials-get=()");
  });
});
