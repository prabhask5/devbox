import { expect, test } from "@playwright/test";
import { SCREENS, readFile, show } from "./app-screens.ts";
import type { Served } from "./app-screens.ts";
import { VIEWPORT } from "./states.ts";

// Visual regression (docs/spec/16-design.md): every screen of the agents app must look like the
// approved design. The baselines are screenshots of the prototype with the same data
// (app-baseline.spec.ts); a difference beyond the threshold needs the owner's approval.
test.use(VIEWPORT);

const TYPES: Record<string, string> = {
  html: "text/html; charset=utf-8",
  js: "text/javascript",
  css: "text/css",
  svg: "image/svg+xml",
  png: "image/png",
  webmanifest: "application/manifest+json",
};

/** The app as the hub embeds it, without its service worker: every request is the test's. */
const app: Served = {
  file: (path) => {
    const name = path === "/" ? "index.html" : path.slice(1);
    const type = TYPES[name.split(".").pop() ?? ""];
    if (!type || name === "sw.js" || name.includes("/")) return undefined;
    try {
      return { body: readFile(`internal/hub/webapp/${name}`), contentType: type };
    } catch {
      return undefined;
    }
  },
  state: (state) => state,
  frame: (frame) => frame,
  asleep: (status) => status,
};

for (const screen of SCREENS) {
  test(`app: ${screen.name}`, async ({ page }) => {
    await show(page, app, screen);
    await expect(page).toHaveScreenshot(`app-${screen.name}.png`);
  });
}
