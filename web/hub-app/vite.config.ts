import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { defineConfig } from "vite";
import type { Plugin } from "vite";
import { tokensCss } from "@devbox/shared";
import type { Tokens } from "@devbox/shared";

const tokens = JSON.parse(
  readFileSync(new URL("../shared/tokens.json", import.meta.url), "utf8"),
) as Tokens;
const publicDir = new URL("./public/", import.meta.url);

/**
 * Finishes the build the hub embeds: the design tokens go in front of the stylesheet, and the
 * service worker learns the files of the shell and a hash of all of them, so every build that
 * changes the app is a new cache and the app can say it was updated.
 */
function shell(): Plugin {
  return {
    name: "devbox-shell",
    enforce: "post",
    generateBundle(_options, bundle) {
      const hash = createHash("sha256");
      const files = ["/"];
      const worker = bundle["sw.js"];
      if (worker?.type !== "chunk") throw new Error("sw.js is not in the bundle");
      for (const name of Object.keys(bundle).sort()) {
        const file = bundle[name];
        if (!file || name === "sw.js") continue;
        if (file.type === "asset" && name.endsWith(".css") && typeof file.source === "string") {
          file.source = tokensCss(tokens) + file.source;
        }
        hash.update(name).update(file.type === "chunk" ? file.code : file.source);
        if (name !== "index.html") files.push(`/${name}`);
      }
      for (const name of readdirSync(publicDir).sort()) {
        hash.update(name).update(readFileSync(new URL(name, publicDir)));
        files.push(`/${name}`);
      }
      hash.update(worker.code);
      worker.code = worker.code
        .replaceAll("__BUILD__", JSON.stringify(hash.digest("hex").slice(0, 12)))
        .replaceAll("__SHELL__", JSON.stringify(files));
    },
  };
}

// File names are stable, because the build is committed (internal/hub/webapp): the hub serves
// them with a validator, and the service worker's cache is what is versioned.
export default defineConfig({
  build: {
    target: "es2022",
    sourcemap: false,
    modulePreload: false,
    rolldownOptions: {
      input: { app: "index.html", sw: "src/sw.ts" },
      output: {
        entryFileNames: "[name].js",
        chunkFileNames: "[name].js",
        assetFileNames: "[name][extname]",
      },
    },
  },
  plugins: [shell()],
});
