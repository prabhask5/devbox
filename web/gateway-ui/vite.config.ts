import { readFileSync } from "node:fs";
import { defineConfig } from "vite";
import type { Plugin } from "vite";
import { tokensCss } from "@devbox/shared";
import type { Tokens } from "@devbox/shared";

const tokens = JSON.parse(
  readFileSync(new URL("../shared/tokens.json", import.meta.url), "utf8"),
) as Tokens;

/**
 * Makes the build one self-contained index.html: the design tokens, the stylesheet and the
 * script are inlined, so the gateway can embed and serve the boot screen as a single file that
 * needs no further request.
 */
function singleFile(): Plugin {
  return {
    name: "devbox-single-file",
    enforce: "post",
    generateBundle(_options, bundle) {
      const html = bundle["index.html"];
      if (html?.type !== "asset" || typeof html.source !== "string") {
        throw new Error("index.html is not in the bundle");
      }
      let page = html.source;
      let styles = tokensCss(tokens);
      for (const [name, file] of Object.entries(bundle)) {
        if (file.type === "chunk") {
          // A function as the replacement, so "$" in the code is not read as a pattern.
          const tag = new RegExp(`<script[^>]*src="/${name}"[^>]*></script>`);
          if (!tag.test(page)) throw new Error(`no script tag for ${name}`);
          const code = file.code.replaceAll("</script", "<\\/script");
          page = page
            .replace(tag, "")
            .replace("</body>", () => `<script type="module">${code}</script></body>`);
        } else if (name.endsWith(".css") && typeof file.source === "string") {
          const tag = new RegExp(`<link[^>]*href="/${name}"[^>]*>`);
          if (!tag.test(page)) throw new Error(`no link tag for ${name}`);
          page = page.replace(tag, "");
          styles += file.source;
        } else {
          continue;
        }
        // eslint-disable-next-line @typescript-eslint/no-dynamic-delete -- the bundle is keyed by file name
        delete bundle[name];
      }
      html.source = page.replace("</head>", () => `<style>${styles}</style></head>`);
    },
  };
}

export default defineConfig({
  build: { target: "es2022", sourcemap: false, modulePreload: false },
  plugins: [singleFile()],
});
