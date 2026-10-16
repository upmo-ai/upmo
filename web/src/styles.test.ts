import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const src = "src";

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...sourceFiles(path));
    else if (/\.(ts|tsx|css)$/.test(entry.name)) out.push(path);
  }
  return out;
}

describe("styles", () => {
  it("imports mantine, tokens, then the component styles, before any app CSS", () => {
    const main = readFileSync(join(src, "main.tsx"), "utf8");
    const mantine = main.indexOf('"@mantine/core/styles.css"');
    const tokens = main.indexOf('"@upmo-ai/tokens/tokens.css"');
    const components = main.indexOf('"@upmo-ai/components/styles.css"');
    const app = main.indexOf('from "./App"');

    expect(mantine).toBeGreaterThanOrEqual(0);
    expect(tokens).toBeGreaterThan(mantine);
    expect(components).toBeGreaterThan(tokens);
    expect(app).toBeGreaterThan(components);
    expect(main).not.toMatch(/import "\.\/.*\.css"/);
  });

  it("self-hosts the three token fonts", () => {
    const main = readFileSync(join(src, "main.tsx"), "utf8");
    for (const weight of [400, 500, 600, 700]) {
      expect(main).toContain(`@fontsource/manrope/${weight}.css`);
    }
    expect(main).toContain("@fontsource/familjen-grotesk/700.css");
    expect(main).toContain("@fontsource/fira-code/400.css");
  });

  it("has no colour literals under web/src", () => {
    const colour = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(/;
    for (const path of sourceFiles(src)) {
      const hit = readFileSync(path, "utf8").match(colour);
      expect(hit ? `${path} ${hit[0]}` : "").toBe("");
    }
  });
});
