/** A duration for display: one decimal below ten seconds, whole seconds from there. */
export function seconds(ms: number): string {
  return ms < 10000 ? `${(ms / 1000).toFixed(1)}s` : `${String(Math.round(ms / 1000))}s`;
}
