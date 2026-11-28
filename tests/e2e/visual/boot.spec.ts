import { expect, test } from "@playwright/test";
import { NEW_STATES, STATES, VIEWPORT, fixture, read, show } from "./states.ts";

// Visual regression (docs/spec/16-design.md): every boot screen state must look like the
// approved design, which is the baselines next to this file. A difference beyond the threshold
// needs the owner's approval, and new baselines (scripts/e2e.sh --update-baselines).
test.use(VIEWPORT);

const page = read("internal/gateway/boot.gen.html");

for (const { name, heading } of [...STATES, ...NEW_STATES]) {
  test(`boot screen: ${name}`, async ({ page: tab }) => {
    const { boot, status, now } = fixture(name);
    const html = page.replace(
      /(<script id="devbox-boot" type="application\/json">)\s*\{\}\s*(<\/script>)/,
      (_slot, open: string, close: string) => open + JSON.stringify(boot) + close,
    );
    expect(html).not.toBe(page);
    await show(tab, html, status ? `data: ${JSON.stringify(status)}\n\n` : "", now);
    await expect(tab.getByRole("heading")).toHaveText(heading);
    await expect(tab).toHaveScreenshot(`boot-${name}.png`);
  });
}
