import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { Page } from "@playwright/test";

const root = join(import.meta.dirname, "../../..");

/** One boot screen state in tests/fixtures/boot. */
export interface BootFixture {
  boot: Record<string, unknown>;
  status?: Record<string, unknown>;
  now?: number;
}

/** The states with a screenshot, and the heading that shows the state has rendered. */
export const STATES: readonly { name: string; heading: string }[] = [
  { name: "asleep-signin", heading: "Devbox is asleep" },
  { name: "signin-error", heading: "Devbox is asleep" },
  { name: "waking", heading: "Waking your devbox" },
  { name: "syncing", heading: "Waking your devbox" },
  { name: "syncing-results", heading: "Waking your devbox" },
  { name: "cold-first", heading: "Booting your devbox" },
  { name: "hub-skipped", heading: "Waking your devbox" },
  { name: "error", heading: "Couldn't wake the devbox" },
  { name: "stopping", heading: "Going to sleep…" },
];

/** States the prototype did not have; their baselines come from the boot screen itself. */
export const NEW_STATES: readonly { name: string; heading: string }[] = [
  { name: "unconfigured", heading: "devbox is not set up yet" },
];

export function fixture(name: string): BootFixture {
  return JSON.parse(
    readFileSync(join(root, "tests/fixtures/boot", `${name}.json`), "utf8"),
  ) as BootFixture;
}

export function read(path: string): string {
  return readFileSync(join(root, path), "utf8");
}

/** The design's reference viewport (docs/spec/16-design.md). */
export const VIEWPORT = { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 };

/**
 * Shows `html` as the page at a made-up origin, with `events` as the body of its event stream
 * and the clock stopped at `now`, so the same state always renders the same pixels.
 */
export async function show(page: Page, html: string, events: string, now: number | undefined) {
  if (now !== undefined) await page.clock.setFixedTime(new Date(now));
  await page.route("https://devbox.test/**", async (route) => {
    if (new URL(route.request().url()).pathname === "/__devbox/events") {
      await route.fulfill({ contentType: "text/event-stream", body: events });
    } else {
      await route.fulfill({ contentType: "text/html; charset=utf-8", body: html });
    }
  });
  await page.goto("https://devbox.test/");
}
