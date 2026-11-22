import { createRequire } from "node:module";

// The official image ships browsers, not the npm package. e2e/run.sh installs
// playwright@1.61.1 here. NODE_PATH does not apply to ESM imports.
const require = createRequire("/opt/playwright/package.json");
const { chromium } = require("playwright");

const url = process.env.WEB_URL;
if (!url) {
  throw new Error("WEB_URL is not set");
}

function fail(message) {
  throw new Error(`brand check: ${message}`);
}

const typeOf = (response) => (response.headers.get("content-type") ?? "").split(";")[0].trim().toLowerCase();

async function fetchBytes(path) {
  const response = await fetch(new URL(path, url));
  return { response, bytes: new Uint8Array(await response.arrayBuffer()) };
}

// Width and height from the IHDR chunk of a PNG, or null when it is not one.
function pngSize(bytes) {
  const signature = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
  if (bytes.length < 24 || signature.some((byte, i) => bytes[i] !== byte)) return null;
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  return { width: view.getUint32(16), height: view.getUint32(20) };
}

const isIco = (bytes) =>
  bytes.length > 6 && bytes[0] === 0 && bytes[1] === 0 && bytes[2] === 1 && bytes[3] === 0;

const isSvg = (bytes) => new TextDecoder().decode(bytes).includes("<svg");

// The site files, each served from the root with its own content type and
// not the HTML fallback.
const served = [
  { path: "/favicon.svg", types: ["image/svg+xml"], valid: isSvg },
  { path: "/favicon.ico", types: ["image/x-icon", "image/vnd.microsoft.icon"], valid: isIco },
  { path: "/apple-touch-icon.png", types: ["image/png"], valid: (b) => pngSize(b)?.width === 180 && pngSize(b)?.height === 180 },
  { path: "/site.webmanifest", types: ["application/manifest+json", "application/json"], valid: () => true },
];

for (const file of served) {
  const { response, bytes } = await fetchBytes(file.path);
  if (response.status !== 200) fail(`${file.path} returned ${response.status}`);
  if (!file.types.includes(typeOf(response))) {
    fail(`${file.path} content type is ${typeOf(response)}, want ${file.types.join(" or ")}`);
  }
  if (!file.valid(bytes)) fail(`${file.path} is not the expected file`);
  console.log(`${file.path} ${typeOf(response)} ${bytes.length} bytes`);
}

// The manifest lists icons, and every one resolves to a PNG of its declared size.
const manifestBody = await fetchBytes("/site.webmanifest");
const manifest = JSON.parse(new TextDecoder().decode(manifestBody.bytes));
if (!Array.isArray(manifest.icons) || manifest.icons.length === 0) {
  fail("the manifest lists no icons");
}
for (const icon of manifest.icons) {
  const { response, bytes } = await fetchBytes(icon.src);
  if (response.status !== 200) fail(`manifest icon ${icon.src} returned ${response.status}`);
  if (typeOf(response) !== icon.type) {
    fail(`manifest icon ${icon.src} content type is ${typeOf(response)}, want ${icon.type}`);
  }
  const size = pngSize(bytes);
  if (!size || `${size.width}x${size.height}` !== icon.sizes) {
    fail(`manifest icon ${icon.src} is ${size ? `${size.width}x${size.height}` : "not a PNG"}, want ${icon.sizes}`);
  }
  console.log(`manifest icon ${icon.src} ${icon.sizes}`);
}

const browser = await chromium.launch({
  args: ["--no-sandbox", "--disable-dev-shm-usage"],
});

try {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(url, { waitUntil: "domcontentloaded" });

  const head = await page.evaluate(() => ({
    icons: [...document.head.querySelectorAll("link[rel~='icon']")].map((link) => link.getAttribute("href")),
    touch: [...document.head.querySelectorAll("link[rel='apple-touch-icon']")].map((link) => link.getAttribute("href")),
    manifest: [...document.head.querySelectorAll("link[rel='manifest']")].map((link) => link.getAttribute("href")),
    themeColor: [...document.head.querySelectorAll("meta[name='theme-color']")].map((meta) => meta.getAttribute("content")),
    ogImage: [...document.head.querySelectorAll("meta[property^='og:image']")].map((meta) => [
      meta.getAttribute("property"),
      meta.getAttribute("content"),
    ]),
  }));

  for (const [label, want, got] of [
    ["icon", ["/favicon.svg", "/favicon.ico"], head.icons],
    ["apple-touch-icon", ["/apple-touch-icon.png"], head.touch],
    ["manifest", ["/site.webmanifest"], head.manifest],
  ]) {
    if (JSON.stringify(got) !== JSON.stringify(want)) {
      fail(`head ${label} links are ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    }
  }
  if (head.themeColor.length !== 1 || !/^#[0-9a-f]{6}$/i.test(head.themeColor[0] ?? "")) {
    fail(`head theme-color is ${JSON.stringify(head.themeColor)}, want one hex color`);
  }

  // Every linked file resolves, so the head and the server cannot drift apart.
  for (const href of [...head.icons, ...head.touch, ...head.manifest]) {
    const response = await context.request.get(new URL(href, url).href);
    if (response.status() !== 200) fail(`head links ${href}, which returned ${response.status()}`);
  }

  // og:image tags are allowed only with an absolute URL; the share image must
  // never be linked by a relative or empty one. The dev server has no site
  // origin, so normally there are none.
  if (head.ogImage.length > 0) {
    const og = head.ogImage.find(([property]) => property === "og:image");
    if (!og || !/^https?:\/\/[^/]/.test(og[1] ?? "")) {
      fail(`og:image content is ${JSON.stringify(og?.[1])}, want an absolute URL`);
    }
  }

  console.log("brand check passed");
} finally {
  await browser.close();
}
