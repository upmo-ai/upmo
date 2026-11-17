import type { Plugin } from "vite";

// The document policy a deployment serves with the app. Every source is the
// app's own origin; the editor origin is not named anywhere, because the app
// never frames or fetches it. style-src-elem and style-src-attr allow the inline
// style elements and attributes that Mantine and the components write; scripts
// get no inline allowance. data: is for
// image previews, blob: for the Monaco worker, about: for the srcDoc iframes.
export const PRODUCTION_CSP = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self'",
  "style-src-elem 'self' 'unsafe-inline'",
  "style-src-attr 'unsafe-inline'",
  "font-src 'self'",
  "img-src 'self' data:",
  "connect-src 'self'",
  "worker-src 'self' blob:",
  "frame-src 'self' about:",
  "frame-ancestors 'none'",
  "object-src 'none'",
  "base-uri 'self'",
  "form-action 'self'",
].join("; ");

// The dev server's policy: the production one, plus the inline refresh
// preamble the React plugin injects and the HMR WebSocket.
export const DEVELOPMENT_CSP = PRODUCTION_CSP.replace("script-src 'self';", "script-src 'self' 'unsafe-inline';").replace(
  "connect-src 'self'",
  "connect-src 'self' ws: wss:",
);

const HSTS = "max-age=15552000";

// The headers of a document response. HSTS is only for a request that came in
// over TLS; on a plain HTTP server it would be ignored at best.
export function documentHeaders(policy: string, secure: boolean): Record<string, string> {
  const headers: Record<string, string> = {
    "Content-Security-Policy": policy,
    "X-Content-Type-Options": "nosniff",
    "X-Frame-Options": "DENY",
    "Referrer-Policy": "strict-origin-when-cross-origin",
    "Permissions-Policy": "camera=(), microphone=(), geolocation=(), payment=(), publickey-credentials-get=(self)",
  };
  if (secure) headers["Strict-Transport-Security"] = HSTS;
  return headers;
}

// The parts of the Node request and response the middleware uses, so the
// plugin does not need the Node typings.
type Req = { socket?: { encrypted?: boolean } };
type Res = { setHeader(name: string, value: string): void };
type Use = (handler: (req: unknown, res: unknown, next: () => void) => void) => unknown;

function middleware(policy: string) {
  return (incoming: unknown, outgoing: unknown, next: () => void) => {
    const secure = (incoming as Req).socket?.encrypted === true;
    for (const [name, value] of Object.entries(documentHeaders(policy, secure))) {
      (outgoing as Res).setHeader(name, value);
    }
    next();
  };
}

// Sends the document headers on every response of the dev server and of
// `vite preview`. It is registered ahead of the other plugins' middleware so
// their responses keep the headers.
export function securityHeaders(): Plugin {
  return {
    name: "upmo:security-headers",

    configureServer(server) {
      (server.middlewares.use as Use)(middleware(DEVELOPMENT_CSP));
    },

    configurePreviewServer(server) {
      (server.middlewares.use as Use)(middleware(PRODUCTION_CSP));
    },
  };
}
