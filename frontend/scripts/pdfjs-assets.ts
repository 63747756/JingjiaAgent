import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";

export function pdfjsOfflineAssets(): Plugin {
  const root = path.dirname(fileURLToPath(import.meta.resolve("pdfjs-dist/package.json")));
  const files = new Map<string, string>();
  for (const group of ["cmaps", "standard_fonts", "wasm", "iccs"]) {
    for (const name of readdirSync(path.join(root, group))) {
      // JavaScript evaluation of PDF actions is deliberately disabled. Its
      // QuickJS engine is not included in the shipped preview assets either.
      if (name.startsWith("quickjs-eval")) continue;
      files.set(`${group}/${name}`, path.join(root, group, name));
    }
  }
  return {
    name: "work-pdf-offline-assets",
    configureServer(server) {
      server.middlewares.use("/pdfjs", (req, res, next) => {
        const name = (req.url || "").split("?")[0].slice(1);
        const filename = files.get(name);
        if (!filename) return next();
        const contentType = name.endsWith(".wasm") ? "application/wasm"
          : name.endsWith(".js") ? "text/javascript" : "application/octet-stream";
        res.setHeader("Content-Type", contentType);
        res.setHeader("X-Content-Type-Options", "nosniff");
        res.end(readFileSync(filename));
      });
    },
    generateBundle() {
      for (const [name, filename] of files) {
        this.emitFile({ type: "asset", fileName: `pdfjs/${name}`, source: readFileSync(filename) });
      }
      this.emitFile({ type: "asset", fileName: "pdfjs/LICENSE", source: readFileSync(path.join(root, "LICENSE")) });
    },
  };
}
