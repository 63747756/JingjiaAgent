// Match the source aliases and TypeScript paths used by the Web bundler when
// running existing contracts with Node. This hook is only used by the tests.
import { existsSync, statSync } from "node:fs";
import { registerHooks } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";

const sourceRoot = new URL("../src/", import.meta.url);

function sourceCandidates(url) {
  const filename = fileURLToPath(url);
  return [filename, filename.replace(/\.js$/, ".ts"), filename + ".ts", filename + ".tsx",
    filename + "/index.ts", filename + "/index.tsx"];
}

registerHooks({
  resolve(specifier, context, nextResolve) {
    const alias = specifier.startsWith("@/");
    try {
      if (!alias) return nextResolve(specifier, context);
    } catch (error) {
      if (!["ERR_MODULE_NOT_FOUND", "ERR_UNSUPPORTED_DIR_IMPORT"].includes(error.code)) throw error;
      if (!specifier.startsWith(".") && !specifier.startsWith("file:")) throw error;
    }
    const url = alias ? new URL(specifier.slice(2), sourceRoot) : new URL(specifier, context.parentURL);
    const match = sourceCandidates(url).find(name => existsSync(name) && statSync(name).isFile());
    return nextResolve(match ? pathToFileURL(match).href : specifier, context);
  },
});
