import type { AnswerKey, GatewayStatus, HubState, Session } from "@devbox/shared/proto/types";

/** How long ago, in the shortest form: now, 5m, 3h, 2d. */
export function ago(at: number | undefined, now: number): string {
  if (!at) return "";
  const s = Math.max(0, (now - at) / 1000);
  if (s < 45) return "now";
  if (s < 3600) return `${String(Math.round(s / 60))}m`;
  if (s < 86400) return `${String(Math.round(s / 3600))}h`;
  return `${String(Math.round(s / 86400))}d`;
}

/** A duration in seconds, in the shortest form: 40s, 12m, 1.5h. */
export function duration(sec: number): string {
  if (sec < 60) return `${String(Math.round(sec))}s`;
  if (sec < 3600) return `${String(Math.round(sec / 60))}m`;
  return `${(sec / 3600).toFixed(1)}h`;
}

export const LETTER: Partial<Record<string, string>> = {
  claude: "C",
  codex: "X",
  opencode: "O",
  shell: "$",
};

export const STATE_LABEL: Record<Session["state"], string> = {
  working: "Working",
  needs_input: "Needs you",
  idle: "Idle",
  starting: "Starting",
  exited: "Exited",
  gone: "Gone",
  ended: "Ended",
  error: "Error",
};

/** The one line under a session's name: what it waits for, does, was asked or said last. */
export function sessionLine(s: Session): string {
  switch (s.state) {
    case "needs_input":
      return s.waitingFor ?? "Waiting for you";
    case "error":
      return s.waitingFor ?? "Error";
    case "working":
      if (s.lastTool) return `⚙︎ ${s.lastTool}`;
      return s.lastPrompt ? `“${s.lastPrompt}”` : "Working…";
    case "starting":
      return "Starting…";
    case "exited":
      return "Agent exited — open to restart";
    case "gone":
      return "tmux session ended";
    case "ended":
      return "Session ended";
    case "idle":
      return s.lastReply ?? (s.title || (s.agent === "shell" ? "Shell" : "Ready for a prompt"));
  }
}

/** The sessions in the groups the list shows, empty groups left out. */
export function groups(sessions: Session[]): { name: string; sessions: Session[] }[] {
  const of = (...states: Session["state"][]) => sessions.filter((s) => states.includes(s.state));
  return [
    { name: "Needs you", sessions: of("needs_input", "error") },
    { name: "Working", sessions: of("working", "starting") },
    { name: "Idle", sessions: of("idle") },
    { name: "Finished", sessions: of("exited", "gone", "ended") },
  ].filter((group) => group.sessions.length > 0);
}

/** The status pill at the top: syncing, busy, when it will sleep, or how long it has been idle. */
export function pill(
  devbox: HubState["devbox"],
  gateway: GatewayStatus | undefined,
): { dot: string; text: string } {
  const sync = devbox.sync;
  if (sync.state === "running") {
    return {
      dot: "accent pulse",
      text: `Syncing ${String(sync.done)}/${sync.total ? String(sync.total) : "…"}`,
    };
  }
  if (devbox.idle.busy) return { dot: "warn pulse", text: "Busy" };
  if (gateway?.idle?.limitMs) {
    const left = Math.max(0, gateway.idle.limitMs - gateway.idle.idleMs);
    return {
      dot: "ok",
      text: left < 60000 ? "Sleeping soon" : `Sleeps in ${String(Math.ceil(left / 60000))}m`,
    };
  }
  return { dot: "ok", text: `Idle ${duration(devbox.idle.idleSeconds)}` };
}

/** How many sessions want the owner: the number on the app's icon. */
export function badgeCount(sessions: Session[]): number {
  return sessions.filter((s) => s.state === "needs_input" || s.unread).length;
}

/** A base64url string (a VAPID public key) as bytes, for PushManager.subscribe. */
export function base64UrlToBytes(text: string): Uint8Array<ArrayBuffer> {
  const padded = (text + "=".repeat((4 - (text.length % 4)) % 4))
    .replace(/-/g, "+")
    .replace(/_/g, "/");
  return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
}

/** What to call this device in the list of devices that get notifications. */
export function deviceName(userAgent: string): string {
  if (userAgent.includes("iPhone")) return "iPhone";
  if (userAgent.includes("iPad")) return "iPad";
  if (userAgent.includes("Android")) return "Android";
  if (userAgent.includes("Mac")) return "Mac";
  return "Browser";
}

/** How an answer button looks: the first is the usual answer, one that sends Escape declines. */
export function tone(key: AnswerKey, index: number): string {
  if (key.key === "Escape") return "bad";
  return index === 0 ? "ok" : "";
}
