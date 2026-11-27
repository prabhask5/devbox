import { describe, expect, test } from "vitest";
import {
  ago,
  badgeCount,
  base64UrlToBytes,
  deviceName,
  duration,
  groups,
  pill,
  sessionLine,
  tone,
} from "@devbox/hub-app/format.ts";
import type { GatewayStatus, HubState, Session } from "@devbox/shared/proto/types";

function session(change: Partial<Session>): Session {
  return {
    v: 1,
    id: "a1b2c3d4",
    name: "claude-devbox",
    agent: "claude",
    kind: "tmux",
    cwd: "/home/dev/code/devbox",
    createdAt: 0,
    state: "idle",
    stateSince: 0,
    title: "",
    unread: false,
    adopted: false,
    attached: false,
    viewers: 0,
    ...change,
  };
}

describe("the line under a session's name", () => {
  test.each<[Partial<Session>, string]>([
    [{ state: "needs_input", waitingFor: "Run cargo test?" }, "Run cargo test?"],
    [{ state: "needs_input" }, "Waiting for you"],
    [{ state: "error", waitingFor: "API rate limit reached" }, "API rate limit reached"],
    [{ state: "error" }, "Error"],
    [{ state: "working", lastTool: "Bash", lastPrompt: "fix it" }, "⚙︎ Bash"],
    [{ state: "working", lastPrompt: "fix it" }, "“fix it”"],
    [{ state: "working" }, "Working…"],
    [{ state: "starting" }, "Starting…"],
    [{ state: "exited" }, "Agent exited — open to restart"],
    [{ state: "gone" }, "tmux session ended"],
    [{ state: "ended" }, "Session ended"],
    [{ state: "idle", lastReply: "Done.", title: "Fix the test" }, "Done."],
    [{ state: "idle", title: "Fix the test" }, "Fix the test"],
    [{ state: "idle" }, "Ready for a prompt"],
    [{ state: "idle", agent: "shell" }, "Shell"],
  ])("%j → %s", (change, line) => {
    expect(sessionLine(session(change))).toBe(line);
  });
});

test("sessions are grouped by what they want, empty groups left out", () => {
  const list = (["idle", "needs_input", "working", "error", "gone", "starting"] as const).map(
    (state, index) => session({ state, id: String(index) }),
  );
  expect(
    groups(list).map((group) => [group.name, group.sessions.map((s) => s.state).join(" ")]),
  ).toEqual([
    ["Needs you", "needs_input error"],
    ["Working", "working starting"],
    ["Idle", "idle"],
    ["Finished", "gone"],
  ]);
  expect(groups([session({ state: "exited" }), session({ state: "ended" })])).toHaveLength(1);
  expect(groups([])).toEqual([]);
});

test("relative times and durations", () => {
  const now = 1_000_000_000;
  expect(ago(undefined, now)).toBe("");
  expect(ago(now - 44_000, now)).toBe("now");
  expect(ago(now - 5 * 60_000, now)).toBe("5m");
  expect(ago(now - 3 * 3600_000, now)).toBe("3h");
  expect(ago(now - 2 * 86400_000, now)).toBe("2d");
  expect(ago(now + 5000, now)).toBe("now");
  expect(duration(40)).toBe("40s");
  expect(duration(12 * 60)).toBe("12m");
  expect(duration(5400)).toBe("1.5h");
});

describe("the status pill", () => {
  const devbox = (change: Partial<HubState["devbox"]>): HubState["devbox"] => ({
    v: 1,
    version: "test",
    uptime: 100,
    idle: { busy: false, reasons: [], idleSeconds: 120 },
    sync: { state: "idle", total: 0, done: 0, results: [] },
    ...change,
  });
  const gateway = (idleMs: number) =>
    ({ idle: { idleMs, gatewayIdleMs: idleMs, limitMs: 900_000 } }) as GatewayStatus;

  test("a running sync comes first", () => {
    const syncing = devbox({ sync: { state: "running", total: 11, done: 4, results: [] } });
    expect(pill(syncing, gateway(0))).toEqual({ dot: "accent pulse", text: "Syncing 4/11" });
    const counting = devbox({ sync: { state: "running", total: 0, done: 0, results: [] } });
    expect(pill(counting, undefined).text).toBe("Syncing 0/…");
  });
  test("then busy", () => {
    const busy = devbox({ idle: { busy: true, reasons: ["agent working"], idleSeconds: 0 } });
    expect(pill(busy, gateway(0))).toEqual({ dot: "warn pulse", text: "Busy" });
  });
  test("then when it will sleep", () => {
    expect(pill(devbox({}), gateway(300_000))).toEqual({ dot: "ok", text: "Sleeps in 10m" });
    expect(pill(devbox({}), gateway(870_000)).text).toBe("Sleeping soon");
    expect(pill(devbox({}), gateway(999_000)).text).toBe("Sleeping soon");
  });
  test("without the gateway, how long it has been idle", () => {
    expect(pill(devbox({}), undefined)).toEqual({ dot: "ok", text: "Idle 2m" });
  });
});

test("the badge counts sessions that need input or are unread, once each", () => {
  expect(
    badgeCount([
      session({ state: "needs_input", unread: true }),
      session({ state: "needs_input" }),
      session({ unread: true }),
      session({ state: "working" }),
    ]),
  ).toBe(3);
});

test("a VAPID key becomes the bytes PushManager wants", () => {
  expect([...base64UrlToBytes("-_8A")]).toEqual([251, 255, 0]);
  expect([...base64UrlToBytes("AQ")]).toEqual([1]);
  expect(base64UrlToBytes("BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U")).toHaveLength(65); // prettier-ignore
});

test("devices are named after what they are", () => {
  expect(deviceName("Mozilla/5.0 (iPhone; CPU iPhone OS 26_0 like Mac OS X)")).toBe("iPhone");
  expect(deviceName("Mozilla/5.0 (iPad; CPU OS 26_0 like Mac OS X)")).toBe("iPad");
  expect(deviceName("Mozilla/5.0 (Linux; Android 16)")).toBe("Android");
  expect(deviceName("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")).toBe("Mac");
  expect(deviceName("Mozilla/5.0 (X11; Linux x86_64)")).toBe("Browser");
});

test("the usual answer is green, the one that declines red", () => {
  const keys = [
    { label: "Yes", key: "1" },
    { label: "Always", key: "2" },
    { label: "No", key: "Escape" },
  ];
  expect(keys.map(tone)).toEqual(["ok", "", "bad"]);
  expect(tone({ label: "Reject", key: "Escape" }, 0)).toBe("bad");
});
