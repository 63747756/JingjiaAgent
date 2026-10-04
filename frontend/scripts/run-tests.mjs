import { readdirSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = new URL("../", import.meta.url);
const tests = readdirSync(new URL("test/", root))
  .filter(name => /\.test\.(ts|mjs)$/.test(name)).sort()
  .map(name => fileURLToPath(new URL("test/" + name, root)));
const result = spawnSync(process.execPath, [
  "--experimental-transform-types", "--import", new URL("test-loader.mjs", import.meta.url).href,
  "--test", "--test-concurrency=2", ...process.argv.slice(2), ...tests,
], { cwd: fileURLToPath(root), stdio: "inherit" });
if (result.error) throw result.error;
process.exit(result.status ?? 1);
