import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const webRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const packages = path.resolve(webRoot, "../design-system/packages");

function run(label, cwd, command, args) {
  const result = spawnSync(command, args, { cwd, stdio: "inherit" });
  if (result.status !== 0) {
    console.error(`prepare-design-system: ${label} failed`);
    process.exit(result.status ?? 1);
  }
}

run("tokens", path.join(packages, "tokens"), "node", ["scripts/build.ts"]);
run("icons", path.join(packages, "icons"), "node", ["scripts/build.ts"]);
run("ui", webRoot, path.join(webRoot, "node_modules/.bin/tsc"), ["-p", "tsconfig.design-system-ui.json"]);
