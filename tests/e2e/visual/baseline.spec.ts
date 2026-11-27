import { expect, test } from "@playwright/test";
import { STATES, VIEWPORT, fixture, read, show } from "./states.ts";

// Writes the visual baselines: the prototype's boot screen, which is the approved design,
// rendered with the fixture data in this browser on this platform. Run by
// `scripts/e2e.sh --update-baselines`; an ordinary run leaves the baselines alone.
test.use(VIEWPORT);
const states = process.env.DEVBOX_BASELINE === "1" ? STATES : [];

const prototype = read("reference/prototype/host/devbox-gateway/boot.html");
const PHASES = [
  { id: "start", label: "Starting the VM" },
  { id: "code", label: "Waiting for code-server" },
  { id: "hub", label: "Waiting for the agents hub" },
  { id: "sync", label: "Syncing repos from GitHub" },
];

/** The fixture's gateway status in the shape the prototype's page expects. */
function prototypeState(status: Record<string, unknown>): Record<string, unknown> {
  const wake = status.wake as (Record<string, unknown> & { log: string[] }) | undefined;
  return {
    ...status,
    phaseDefs: PHASES,
    wake: wake && { ...wake, log: wake.log.map((m) => ({ at: status.serverNow, m })) },
  };
}

for (const { name, heading } of states) {
  test(`baseline: ${name}`, async ({ page }, testInfo) => {
    const { boot, status, now } = fixture(name);
    const html = prototype.replace("/*__BOOT__*/null", () => JSON.stringify(boot));
    expect(html).not.toBe(prototype);
    const events = status ? `data: ${JSON.stringify(prototypeState(status))}\n\n` : "";
    await show(page, html, events, now);
    await expect(page.getByRole("heading")).toHaveText(heading);
    await page.screenshot({
      path: testInfo.snapshotPath(`boot-${name}.png`),
      animations: "disabled",
    });
  });
}
