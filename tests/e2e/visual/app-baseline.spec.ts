import { expect, test } from "@playwright/test";
import { SCREENS, readFile, show } from "./app-screens.ts";
import type { Served } from "./app-screens.ts";
import { VIEWPORT } from "./states.ts";

// Writes the app's visual baselines: the prototype's app, which is the approved design, rendered
// with the fixture data in this browser on this platform. Run by `scripts/e2e.sh
// --update-baselines`; an ordinary run leaves the baselines alone.
test.use(VIEWPORT);
const screens = process.env.DEVBOX_BASELINE === "1" ? SCREENS : [];

const dir = "reference/prototype/devbox/hub/public";
const tokens = JSON.parse(readFile("web/shared/tokens.json").toString()) as {
  font: { sans: string };
};
// The prototype's two pages named their fonts differently; the design's one stack (spec 16) is
// the boot screen's, so the app's baseline is drawn with it too.
const PROTOTYPE_FONT =
  '-apple-system, BlinkMacSystemFont, "Segoe UI", Inter, system-ui, sans-serif';
const css = readFile(`${dir}/app.css`).toString();
if (!css.includes(PROTOTYPE_FONT)) throw new Error("the prototype's font stack has changed");

interface Key {
  label: string;
  key: string;
}

/** The fixture in the shapes the prototype's page expects. */
const prototype: Served = {
  file: (path) => {
    if (path === "/") return { body: readFile(`${dir}/index.html`), contentType: "text/html" };
    if (path === "/app.js")
      return { body: readFile(`${dir}/app.js`), contentType: "text/javascript" };
    if (path === "/app.css") {
      return { body: css.replace(PROTOTYPE_FONT, tokens.font.sans), contentType: "text/css" };
    }
    if (path === "/icon.svg")
      return { body: readFile(`${dir}/icon.svg`), contentType: "image/svg+xml" };
    return undefined;
  },
  state: (state) => {
    const adapters = state.adapters as (Record<string, unknown> & {
      id: string;
      answerKeys: Key[];
    })[];
    const sessions = state.sessions as Record<string, unknown>[];
    return {
      ...state,
      sessions: sessions.map((s) => ({ ...s, external: s.kind === "external" })),
      adapters: Object.fromEntries(
        adapters.map((adapter) => [
          adapter.id,
          {
            ...adapter,
            answerKeys: adapter.answerKeys.map(({ label, key }, index) => ({
              label,
              keys: [key],
              tone: key === "Escape" ? "bad" : index === 0 ? "ok" : "",
            })),
          },
        ]),
      ),
      commonKeys: (state.commonKeys as Key[]).map(({ label, key }) => ({ label, keys: [key] })),
    };
  },
  frame: (frame) => ({ ...frame, text: frame.text ?? null }),
  asleep: (status) => ({ ...status, devbox: "asleep" }),
};

for (const screen of screens) {
  test(`baseline: app ${screen.name}`, async ({ page }, testInfo) => {
    await show(page, prototype, screen);
    await expect(page.locator("#app, .sess").first()).toBeVisible();
    await page.screenshot({
      path: testInfo.snapshotPath(`app-${screen.name}.png`),
      animations: "disabled",
    });
  });
}
