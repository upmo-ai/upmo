import { copyFileSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { build, createServer } from "vite";
import type { Plugin } from "vite";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { HEAD_MARKER, brandHead, brandKit, fillOrigin, kitWebDir, normalizeOrigin } from "./brand-kit";

// What the site must serve from its root, written out here and not read from
// the plugin, so a wrong entry in the plugin cannot make this test agree with it.
const SERVED: Record<string, string> = {
  "favicon.svg": "image/svg+xml",
  "favicon.ico": "image/x-icon",
  "apple-touch-icon.png": "image/png",
  "icon-192.png": "image/png",
  "icon-512.png": "image/png",
  "icon-512-maskable.png": "image/png",
  "og.png": "image/png",
  "site.webmanifest": "application/manifest+json",
};

const KIT_DIR = kitWebDir();
const kitHead = readFileSync(join(KIT_DIR, "head.html"), "utf8");
const indexHtml = readFileSync("index.html", "utf8");

const ogTags = (html: string) => html.split("\n").filter((line) => line.includes('property="og:'));

describe("UPM-179 brand kit head tags", () => {
  it("keeps the icon, manifest and theme-color tags with or without an origin", () => {
    for (const origin of [undefined, "https://upmo.example"]) {
      const head = brandHead(kitHead, origin);
      expect(head).toContain('<link rel="icon" href="/favicon.svg" type="image/svg+xml">');
      expect(head).toContain('<link rel="icon" href="/favicon.ico" sizes="48x48">');
      expect(head).toContain('<link rel="apple-touch-icon" href="/apple-touch-icon.png">');
      expect(head).toContain('<link rel="manifest" href="/site.webmanifest">');
      expect(head).toMatch(/<meta name="theme-color" content="#[0-9a-f]{6}">/);
    }
  });

  it("emits the og:image tags with an absolute URL when an origin is set", () => {
    const head = brandHead(kitHead, "https://upmo.example");
    expect(head).toContain('<meta property="og:image" content="https://upmo.example/og.png">');
    expect(head).toContain('<meta property="og:image:type" content="image/png">');
    expect(head).toContain('<meta property="og:image:width" content="1200">');
    expect(head).toContain('<meta property="og:image:height" content="630">');
    expect(head).toContain('<meta property="og:image:alt" content="Upmo">');
    expect(head).not.toContain("{{");
  });

  it("omits every og tag when no origin is set, never a relative or empty URL", () => {
    for (const origin of [undefined, "", "   "]) {
      const head = brandHead(kitHead, origin);
      expect(ogTags(head)).toEqual([]);
      expect(head).not.toContain("og:");
      expect(head).not.toContain("{{");
      expect(head).not.toMatch(/content="\/?og\.png"/);
    }
  });

  it("drops a trailing slash so the image URL has a single slash", () => {
    const head = brandHead(kitHead, "https://upmo.example/");
    expect(head).toContain('content="https://upmo.example/og.png"');
    expect(head).not.toContain("//og.png");
  });

  it("keeps a port on the origin", () => {
    expect(normalizeOrigin("http://localhost:4173")).toBe("http://localhost:4173");
  });

  it.each(["http://[::1]:8080", "https://[2001:db8::1]", "http://127.0.0.1:5173", "https://sub-domain.upmo.example"])(
    "accepts the origin %s",
    (good) => {
      expect(normalizeOrigin(good)).toBe(good);
    },
  );

  it.each([
    "/relative",
    "upmo.example",
    "localhost:3000",
    "ftp://upmo.example",
    "https://upmo.example/app",
    "https://upmo.example/?x=1",
    'https://a"onfocus="x.example',
    "https://a$`b.example",
    "https://a$&b.example",
  ])(
    "rejects the origin %s",
    (bad) => {
      expect(() => normalizeOrigin(bad)).toThrow(/VITE_SITE_ORIGIN/);
      expect(() => brandHead(kitHead, bad)).toThrow(/VITE_SITE_ORIGIN/);
    },
  );

  it("fails when a kept line still has a placeholder", () => {
    expect(() => brandHead('<link rel="icon" href="{{origin}}/favicon.svg">', undefined)).toThrow(/placeholder/);
  });
});

describe("UPM-179 installed kit", () => {
  it("has every file the app serves, and the head and manifest only point at those", () => {
    const names = Object.keys(SERVED);
    expect(names.filter((name) => readFileSync(join(KIT_DIR, name)).length === 0)).toEqual([]);

    const served = new Set(names);
    const hrefs = [...brandHead(kitHead, "https://upmo.example").matchAll(/href="\/([^"]+)"/g)].map((m) => m[1] ?? "");
    expect(hrefs.length).toBeGreaterThan(0);
    expect(hrefs.filter((href) => !served.has(href))).toEqual([]);

    const manifest = JSON.parse(readFileSync(join(KIT_DIR, "site.webmanifest"), "utf8")) as { icons: { src: string }[] };
    expect(manifest.icons.length).toBeGreaterThan(0);
    expect(manifest.icons.map((icon) => icon.src.replace(/^\//, "")).filter((src) => !served.has(src))).toEqual([]);
  });

  it("is marked in index.html so the tags have a place to go", () => {
    expect(indexHtml.split(HEAD_MARKER).length - 1).toBe(1);
  });
});

describe("UPM-179 brand kit plugin", () => {
  let root = "";

  beforeAll(() => {
    root = mkdtempSync(join(tmpdir(), "brand-kit-"));
    writeFileSync(join(root, "index.html"), indexHtml.replace(/<script[^>]*><\/script>\s*/, ""));
  });

  afterAll(() => {
    rmSync(root, { recursive: true, force: true });
  });

  const kitFile = (name: string) => readFileSync(join(KIT_DIR, name));
  const options = { configFile: false, root, logLevel: "silent" } as const;

  it("serves the kit files at the site root in dev with the right content types", async () => {
    const server = await createServer({
      ...options,
      plugins: [brandKit({ dir: KIT_DIR })],
      server: { host: "127.0.0.1", port: 0, hmr: false, watch: null },
    });
    try {
      await server.listen();
      const base = server.resolvedUrls?.local[0];
      expect(base).toBeTruthy();

      const got: Record<string, { status: number; type: string | null; bytes: Uint8Array }> = {};
      const want: typeof got = {};
      for (const [name, type] of Object.entries(SERVED)) {
        const res = await fetch(`${base}${name}`);
        got[name] = { status: res.status, type: res.headers.get("content-type"), bytes: new Uint8Array(await res.arrayBuffer()) };
        want[name] = { status: 200, type, bytes: new Uint8Array(kitFile(name)) };
      }
      expect(got).toEqual(want);

      const page = await fetch(`${base}some/route`);
      expect(page.headers.get("content-type")).toContain("text/html");
      const html = await page.text();
      expect(html).toContain('rel="manifest" href="/site.webmanifest"');
      expect(html).not.toContain(HEAD_MARKER);
      expect(html).not.toContain("og:image");
    } finally {
      await server.close();
    }
  });

  it("emits the kit files and the tags in a build", async () => {
    const outDir = join(root, "dist");
    await build({
      ...options,
      plugins: [brandKit({ dir: KIT_DIR, origin: "https://upmo.example" })],
      build: { outDir, emptyOutDir: true },
    });

    const emitted: Record<string, Uint8Array> = {};
    const want: typeof emitted = {};
    for (const name of Object.keys(SERVED)) {
      emitted[name] = new Uint8Array(readFileSync(join(outDir, name)));
      want[name] = new Uint8Array(kitFile(name));
    }
    expect(emitted).toEqual(want);
    const html = readFileSync(join(outDir, "index.html"), "utf8");
    expect(html).toContain('rel="apple-touch-icon" href="/apple-touch-icon.png"');
    expect(html).toContain('property="og:image" content="https://upmo.example/og.png"');
    expect(html).not.toContain(HEAD_MARKER);
  }, 30_000);

  it("builds without og tags when no origin is set", async () => {
    const outDir = join(root, "dist-no-origin");
    await build({
      ...options,
      plugins: [brandKit({ dir: KIT_DIR, origin: "" })],
      build: { outDir, emptyOutDir: true },
    });
    const html = readFileSync(join(outDir, "index.html"), "utf8");
    expect(html).toContain('rel="icon" href="/favicon.svg"');
    expect(html).not.toContain("og:");
  }, 30_000);

  it("fails the build when the kit lacks a file", async () => {
    const empty = mkdtempSync(join(tmpdir(), "brand-kit-empty-"));
    try {
      await expect(
        build({ ...options, plugins: [brandKit({ dir: empty })], build: { outDir: join(root, "dist-bad"), emptyOutDir: true } }),
      ).rejects.toThrow(/missing/);
    } finally {
      rmSync(empty, { recursive: true, force: true });
    }
  });

  it("fails when index.html has no marker", async () => {
    const bare = mkdtempSync(join(tmpdir(), "brand-kit-bare-"));
    writeFileSync(join(bare, "index.html"), "<!doctype html><html><head></head><body></body></html>");
    try {
      await expect(
        build({ ...options, root: bare, plugins: [brandKit({ dir: KIT_DIR })], build: { outDir: join(bare, "dist"), emptyOutDir: true } }),
      ).rejects.toThrow(/brand-kit/);
    } finally {
      rmSync(bare, { recursive: true, force: true });
    }
  });
});

describe("UPM-197 brand kit origin and replacers", () => {
  let root = "";
  const temp: string[] = [];
  const tempDir = (prefix: string) => {
    const dir = mkdtempSync(join(tmpdir(), prefix));
    temp.push(dir);
    return dir;
  };

  beforeAll(() => {
    root = tempDir("brand-kit-env-");
    writeFileSync(join(root, "index.html"), indexHtml.replace(/<script[^>]*><\/script>\s*/, ""));
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  afterAll(() => {
    for (const dir of temp) rmSync(dir, { recursive: true, force: true });
  });

  const buildWith = async (plugin: Plugin, envDir?: string) => {
    const outDir = join(tempDir("brand-kit-out-"), "dist");
    await build({ configFile: false, root, logLevel: "silent", envDir, plugins: [plugin], build: { outDir, emptyOutDir: true } });
    return readFileSync(join(outDir, "index.html"), "utf8");
  };

  it("reads VITE_SITE_ORIGIN from the Vite env, from the installed kit", async () => {
    // A value in the process env would win over the .env file.
    vi.stubEnv("VITE_SITE_ORIGIN", undefined);
    const envDir = tempDir("brand-kit-dotenv-");
    // The key is spelled out here, not taken from the plugin, so renaming it there fails this test.
    writeFileSync(join(envDir, ".env"), "VITE_SITE_ORIGIN=https://env.example\n");

    const html = await buildWith(brandKit(), envDir);
    expect(html).toContain('property="og:image" content="https://env.example/og.png"');
  }, 30_000);

  it("fails the build, naming VITE_SITE_ORIGIN, when the env value is not an origin", async () => {
    vi.stubEnv("VITE_SITE_ORIGIN", undefined);
    const envDir = tempDir("brand-kit-dotenv-bad-");
    writeFileSync(join(envDir, ".env"), "VITE_SITE_ORIGIN=upmo.example\n");

    await expect(buildWith(brandKit(), envDir)).rejects.toThrow(/VITE_SITE_ORIGIN/);
  });

  it("finds the kit through the package as an absolute path, not under the working directory", () => {
    const dir = kitWebDir();
    expect(dir).toMatch(/^\/.*\/dist\/web$/);
    expect(readFileSync(join(dir, "head.html"), "utf8")).toBe(kitHead);
  });

  it("keeps a dollar pattern in the origin literal", () => {
    const line = '<meta property="og:image" content="{{origin}}/og.png">';
    for (const origin of ["https://a$&b.example", "https://a$`b.example", "https://a$'b.example", "https://a$$b.example"]) {
      expect(fillOrigin(line, origin)).toBe(`<meta property="og:image" content="${origin}/og.png">`);
    }
  });

  it("keeps a dollar pattern in the head literal when it goes into index.html", async () => {
    const dir = tempDir("brand-kit-dollar-");
    for (const name of Object.keys(SERVED)) copyFileSync(join(KIT_DIR, name), join(dir, name));
    const note = "<meta name=\"note\" content=\"$& $$ $' $`\">";
    writeFileSync(join(dir, "head.html"), `<link rel="icon" href="/favicon.svg" type="image/svg+xml">\n${note}\n`);

    const html = await buildWith(brandKit({ dir, origin: "https://upmo.example" }));
    expect(html).toContain(note);
  }, 30_000);

  // Node drops the body of a HEAD response by itself, so a real request cannot
  // show a body being sent. The middleware is called with a recording response.
  type Recorded = { handled: boolean; status: number; headers: Record<string, string>; ended: boolean; body: unknown };
  type Handler = (req: { method: string; url: string }, res: unknown, next: () => void) => void;

  const middleware = () => {
    const plugin = brandKit({ dir: KIT_DIR, origin: "" });
    (plugin.configResolved as (config: unknown) => void)({ root, env: {} });
    let handler: Handler | undefined;
    (plugin.configureServer as (server: unknown) => void)({
      middlewares: {
        use: (fn: Handler) => {
          handler = fn;
        },
      },
    });
    return (method: string, url: string): Recorded => {
      const rec: Recorded = { handled: true, status: 0, headers: {}, ended: false, body: undefined };
      const res = {
        statusCode: 0,
        setHeader: (name: string, value: string) => {
          rec.headers[name.toLowerCase()] = value;
        },
        end: (body?: unknown) => {
          rec.ended = true;
          rec.body = body;
          rec.status = res.statusCode;
        },
      };
      handler?.({ method, url }, res, () => {
        rec.handled = false;
      });
      return rec;
    };
  };

  it("sends the headers but no body for HEAD, and the bytes for GET", () => {
    const request = middleware();
    for (const name of ["favicon.svg", "og.png", "site.webmanifest"]) {
      const bytes = new Uint8Array(readFileSync(join(KIT_DIR, name)));
      const want = { "content-type": SERVED[name], "content-length": String(bytes.length) };

      const head = request("HEAD", `/${name}`);
      expect(head).toMatchObject({ handled: true, status: 200, ended: true, headers: want });
      expect(head.body).toBeUndefined();

      const get = request("GET", `/${name}?v=2`);
      expect(get).toMatchObject({ handled: true, status: 200, ended: true, headers: want });
      expect(new Uint8Array(get.body as Uint8Array)).toEqual(bytes);
    }
  });

  it("passes other methods and other paths on", () => {
    const request = middleware();
    expect(request("POST", "/favicon.svg").handled).toBe(false);
    expect(request("GET", "/some/route").handled).toBe(false);
  });
});
