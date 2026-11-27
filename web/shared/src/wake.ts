import type { PhaseTiming, Wake, WakePhaseId } from "./proto/types.ts";

/** The phases of a wake, in order, with what the boot screen calls them. */
export const PHASES: readonly { id: WakePhaseId; label: string }[] = [
  { id: "start", label: "Starting the VM" },
  { id: "code", label: "Waiting for code-server" },
  { id: "hub", label: "Waiting for the agents hub" },
  { id: "sync", label: "Syncing repos from GitHub" },
];

interface Progress {
  /** Width of the progress bar, 0 to 100. */
  percent: number;
  /** Time since the wake was requested; frozen once it is done or failed. */
  elapsedMs: number;
  /** Expected time left: the estimates of the phases not started, plus what is left of the current one. */
  remainingMs: number;
  /** The current phase has run longer than its estimate. */
  overrun: boolean;
}

function finished(timing: PhaseTiming | undefined): boolean {
  return timing !== undefined && (timing.end !== undefined || timing.skipped === true);
}

/** Where a wake stands at `now` (server time, ms). */
export function progress(wake: Wake, now: number): Progress {
  let remainingMs = 0;
  let overrun = false;
  let lastEnd = wake.requestedAt;
  for (const { id } of PHASES) {
    const timing = wake.phases[id];
    const eta = wake.eta.phases[id];
    if (timing?.end !== undefined) lastEnd = Math.max(lastEnd, timing.end);
    if (finished(timing)) continue;
    if (timing !== undefined && wake.current === id) {
      const running = now - timing.start;
      remainingMs += Math.max(0, eta - running);
      overrun = running > eta;
    } else {
      remainingMs += eta;
    }
  }
  const ended = wake.done || wake.error !== undefined;
  const elapsedMs = Math.max(0, (ended ? lastEnd : now) - wake.requestedAt);
  const percent = wake.done
    ? 100
    : Math.min(97, (100 * elapsedMs) / Math.max(1, elapsedMs + remainingMs));
  return { percent, elapsedMs, remainingMs: ended ? 0 : remainingMs, overrun: !ended && overrun };
}

/** A duration for display: one decimal below ten seconds, whole seconds from there. */
export function seconds(ms: number): string {
  return ms < 10000 ? `${(ms / 1000).toFixed(1)}s` : `${String(Math.round(ms / 1000))}s`;
}
