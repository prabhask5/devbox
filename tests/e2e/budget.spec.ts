import { appendFileSync, mkdirSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { expect, test } from "@playwright/test";
import { AGENTS, WAKE_PASSWORD, reset } from "./stack.ts";

const ROOT = resolve(import.meta.dirname, "../..");

/** A budget of budgets.toml (docs/spec/13-performance.md). */
function budget(metric: string): number {
  const line = readFileSync(resolve(ROOT, "budgets.toml"), "utf8")
    .split("\n")
    .find((candidate) => candidate.startsWith(`${metric} =`));
  if (line === undefined) throw new Error(`${metric} is not in budgets.toml`);
  return Number(line.split("=")[1]);
}

// The agents app on a mid-range phone: Chromium with its CPU slowed four times, the app's shell
// already in the service worker's cache. Time from the start of the navigation to the session
// list being on the screen, the median of five loads.
test("the app's first render on a mid-range phone is within its budget", async ({
  page,
  request,
}) => {
  await reset(request);
  await page.addInitScript(() => {
    const seen = () => {
      if (document.querySelector("h1")?.textContent === "Agents") {
        (window as unknown as { rendered?: number }).rendered ??= performance.now();
      }
    };
    new MutationObserver(seen).observe(document, {
      childList: true,
      subtree: true,
      characterData: true,
    });
  });
  await page.goto(`${AGENTS}/`);
  await page.getByLabel("Wake password").fill(WAKE_PASSWORD);
  await page.getByRole("button", { name: "Wake" }).click();
  await page.getByLabel("Password").fill(WAKE_PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Agents");
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await page.reload();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Agents");

  const devtools = await page.context().newCDPSession(page);
  await devtools.send("Emulation.setCPUThrottlingRate", { rate: 4 });
  const times: number[] = [];
  for (let run = 0; run < 5; run++) {
    await page.reload();
    const rendered = await page.waitForFunction(
      () => (window as unknown as { rendered?: number }).rendered,
    );
    const time = await rendered.jsonValue();
    if (time === undefined) throw new Error("the page did not say when it rendered");
    times.push(time);
  }
  await devtools.send("Emulation.setCPUThrottlingRate", { rate: 1 });
  times.sort((a, b) => a - b);
  const median = times[2];
  if (median === undefined) throw new Error("five loads were measured");

  // The budgets job collects what was measured into one table (scripts/budgets.sh).
  const out = process.env.DEVBOX_BUDGETS_OUT;
  if (out !== undefined) {
    const file = resolve(ROOT, out);
    mkdirSync(dirname(file), { recursive: true });
    appendFileSync(file, `hub_app_first_render_ms = ${median.toFixed(1)}\n`);
  }
  expect(
    median,
    `first renders: ${times.map((t) => t.toFixed(0)).join(", ")} ms`,
  ).toBeLessThanOrEqual(budget("hub_app_first_render_ms") * 1.1);
});
