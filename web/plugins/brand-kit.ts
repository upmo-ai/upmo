import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import type { Plugin } from "vite";

// The site files of @upmo-ai/brand, by the name they are served under at the
// site root, with the content type they are served as. head.html is not here:
// it is read and turned into tags, not served.
export const BRAND_FILES: Readonly<Record<string, string>> = {
  "favicon.svg": "image/svg+xml",
  "favicon.ico": "image/x-icon",
  "apple-touch-icon.png": "image/png",
  "icon-192.png": "image/png",
  "icon-512.png": "image/png",
  "icon-512-maskable.png": "image/png",
  "og.png": "image/png",
  "site.webmanifest": "application/manifest+json",
};

// The directory of the installed kit's web files, found through the package
// and not through a path under node_modules.
export function kitWebDir(): string {
  let manifest: string;
  try {
    manifest = createRequire(import.meta.url).resolve("@upmo-ai/brand/package.json");
  } catch {
    throw new Error("@upmo-ai/brand is not installed; run pnpm install");
  }
  return join(dirname(manifest), "dist", "web");
}

// The line in index.html that the kit's head tags replace.
export const HEAD_MARKER = "<!-- brand-kit -->";

// The build-time variable that holds the absolute origin of the site.
export const ORIGIN_ENV = "VITE_SITE_ORIGIN";

// A host the origin may have: a plain hostname or an IPv6 literal, with an
// optional port. The URL parser also lets through characters such as quotes,
// "$" and backticks in a host, which must never reach the HTML.
const HOST = /^(?:[a-z0-9.-]+|\[[0-9a-f:.]+\])(?::\d+)?$/;

// Returns the bare origin (scheme, host, port) of a configured site URL, or
// undefined when none is configured. Anything that is set but is not an
// absolute http(s) origin with a plain host throws, so a typo cannot turn into
// a relative or broken share image URL or into markup.
export function normalizeOrigin(raw: string | undefined): string | undefined {
  const value = raw?.trim() ?? "";
  if (value === "") return undefined;

  const fail = () =>
    new Error(`${ORIGIN_ENV} must be an absolute http(s) origin such as https://upmo.example, got "${value}"`);

  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw fail();
  }
  const bare = url.pathname === "/" && url.search === "" && url.hash === "" && url.username === "" && url.password === "";
  if ((url.protocol !== "http:" && url.protocol !== "https:") || !bare || !HOST.test(url.host)) throw fail();
  return url.origin;
}

// Puts the origin into a head line. A function replacer keeps "$&", "$$" and
// the like in the origin literal instead of being read as replacement patterns.
export function fillOrigin(line: string, origin: string): string {
  return line.replaceAll("{{origin}}", () => origin);
}

// Turns the kit's head.html into the tags for index.html. The og:image tags
// carry an absolute URL, so they are kept only when an origin is configured
// and are dropped otherwise.
export function brandHead(headHtml: string, rawOrigin: string | undefined): string {
  const origin = normalizeOrigin(rawOrigin);
  const lines = headHtml
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");

  const out =
    origin === undefined
      ? lines.filter((line) => !line.includes('property="og:'))
      : lines.map((line) => fillOrigin(line, origin));

  if (out.length === 0) throw new Error("brand kit head.html has no tags");
  if (out.some((line) => line.includes("{{"))) {
    throw new Error("brand kit head.html has a placeholder other than {{origin}} on a line that is kept");
  }
  return out.join("\n    ");
}

// The parts of the Node request and response the middleware uses, so the
// plugin does not need the Node typings.
type Req = { method?: string; url?: string };
type Res = {
  statusCode: number;
  setHeader(name: string, value: string): void;
  end(body?: Uint8Array): void;
};

export type BrandKitOptions = {
  // Absolute origin of the site. Defaults to VITE_SITE_ORIGIN.
  origin?: string;
  // Directory that holds the kit's web files. Defaults to the installed package.
  dir?: string;
};

// Serves the brand kit's site files from the site root in dev, emits them in
// build, and fills the head-tag marker in index.html. Nothing is copied into
// the source tree.
export function brandKit(options: BrandKitOptions = {}): Plugin {
  const files = new Map<string, { type: string; body: Uint8Array }>();
  let head = "";

  const readKit = (dir: string, name: string): Uint8Array => {
    try {
      return readFileSync(join(dir, name));
    } catch {
      throw new Error(`brand kit file ${name} is missing from ${dir}; run pnpm install`);
    }
  };

  return {
    name: "upmo:brand-kit",

    configResolved(config) {
      const dir = options.dir ?? kitWebDir();
      const origin = options.origin ?? (config.env[ORIGIN_ENV] as string | undefined);

      files.clear();
      for (const [name, type] of Object.entries(BRAND_FILES)) {
        files.set(name, { type, body: readKit(dir, name) });
      }
      head = brandHead(new TextDecoder().decode(readKit(dir, "head.html")), origin);
    },

    transformIndexHtml(html) {
      if (!html.includes(HEAD_MARKER)) {
        throw new Error(`index.html is missing ${HEAD_MARKER}, so the brand kit tags would not be added`);
      }
      return html.replace(HEAD_MARKER, () => head);
    },

    configureServer(server) {
      server.middlewares.use((incoming, outgoing, next) => {
        const req = incoming as Req;
        const res = outgoing as unknown as Res;
        if (req.method !== "GET" && req.method !== "HEAD") return next();
        const pathname = (req.url ?? "").split("?")[0] ?? "";
        const file = pathname.startsWith("/") ? files.get(pathname.slice(1)) : undefined;
        if (!file) return next();

        res.statusCode = 200;
        res.setHeader("Content-Type", file.type);
        res.setHeader("Content-Length", String(file.body.length));
        res.end(req.method === "HEAD" ? undefined : file.body);
      });
    },

    generateBundle() {
      for (const [fileName, file] of files) {
        this.emitFile({ type: "asset", fileName, source: file.body });
      }
    },
  };
}
