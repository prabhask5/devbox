import { expect, test } from "vitest";
import type { Wake } from "@devbox/shared/proto/types";
import { progress, seconds } from "@devbox/shared";

const T0 = 1_760_000_000_000;

function wake(overrides: Partial<Wake>): Wake {
  return {
    id: "w",
    kind: "restore",
    reason: "test",
    requestedAt: T0,
    phases: {},
    done: false,
    eta: { phases: { start: 6000, code: 1000, hub: 1000, sync: 8000 }, samples: 3 },
    log: [],
    ...overrides,
  };
}

test("before any phase starts everything is still to come", () => {
  expect(progress(wake({}), T0)).toEqual({
    percent: 0,
    elapsedMs: 0,
    remainingMs: 16000,
    overrun: false,
  });
});

test("remaining is what is left of the current phase plus the phases not started", () => {
  const p = progress(wake({ current: "start", phases: { start: { start: T0 } } }), T0 + 2000);
  expect(p.elapsedMs).toBe(2000);
  expect(p.remainingMs).toBe(4000 + 1000 + 1000 + 8000);
  expect(p.percent).toBeCloseTo((100 * 2000) / 16000);
  expect(p.overrun).toBe(false);
});

test("finished phases count with what they really took", () => {
  const p = progress(
    wake({
      current: "code",
      phases: { start: { start: T0, end: T0 + 9000 }, code: { start: T0 + 9000 } },
    }),
    T0 + 9500,
  );
  expect(p.elapsedMs).toBe(9500);
  expect(p.remainingMs).toBe(500 + 1000 + 8000);
});

test("a phase that overruns its estimate adds nothing more and says so", () => {
  const p = progress(
    wake({
      current: "sync",
      phases: {
        start: { start: T0, end: T0 + 1000 },
        code: { start: T0 + 1000, end: T0 + 2000 },
        hub: { start: T0 + 2000, end: T0 + 3000 },
        sync: { start: T0 + 3000 },
      },
    }),
    T0 + 60000,
  );
  expect(p.remainingMs).toBe(0);
  expect(p.overrun).toBe(true);
  expect(p.percent).toBe(97); // never full before the wake is done
});

test("skipped phases are not waited for", () => {
  const p = progress(
    wake({
      current: "sync",
      phases: {
        start: { start: T0, end: T0 + 1000 },
        code: { start: T0 + 1000, end: T0 + 2000 },
        hub: { start: T0 + 2000, end: T0 + 62000, skipped: true },
        sync: { start: T0 + 62000, end: T0 + 62000, skipped: true },
      },
    }),
    T0 + 62000,
  );
  expect(p.remainingMs).toBe(0);
  expect(p.percent).toBe(97);
});

test("a finished wake is at 100 % and its elapsed time stops", () => {
  const done = wake({
    done: true,
    phases: {
      start: { start: T0, end: T0 + 1000 },
      code: { start: T0 + 1000, end: T0 + 2000 },
      hub: { start: T0 + 2000, end: T0 + 3000 },
      sync: { start: T0 + 3000, end: T0 + 3500 },
    },
  });
  expect(progress(done, T0 + 999_999)).toEqual({
    percent: 100,
    elapsedMs: 3500,
    remainingMs: 0,
    overrun: false,
  });
});

test("a failed wake stops where it failed", () => {
  const failed = wake({
    error: "code-server did not come up",
    phases: { start: { start: T0, end: T0 + 1000 }, code: { start: T0 + 1000, end: T0 + 181_000 } },
  });
  const p = progress(failed, T0 + 999_999);
  expect(p.elapsedMs).toBe(181_000);
  expect(p.remainingMs).toBe(0);
  expect(p.percent).toBeLessThan(100);
});

test("a clock that runs behind never shows negative time", () => {
  expect(progress(wake({}), T0 - 5000).elapsedMs).toBe(0);
});

test("durations show a decimal below ten seconds", () => {
  expect([0, 640, 2500, 9949, 10000, 61499].map(seconds)).toEqual([
    "0.0s",
    "0.6s",
    "2.5s",
    "9.9s",
    "10s",
    "61s",
  ]);
});
