import { expect, test } from "@playwright/test";
import type { APIRequestContext } from "@playwright/test";
import {
  AGENTS,
  CODE,
  WAKE_PASSWORD,
  incus,
  reset,
  sessions,
  set,
  startSession,
  typeInto,
} from "./stack.ts";

interface Status {
  vm: string;
  phase: string;
  authed?: boolean;
  idle?: { hubReasons: string[] };
  lastStop?: { reason: string; mode: string };
}

async function status(request: APIRequestContext): Promise<Status> {
  return (await (await request.get(`${CODE}/__devbox/status`)).json()) as Status;
}

/** Signs in, which wakes the devbox, and waits until it is up and settled. */
async function wake(request: APIRequestContext) {
  const login = await request.post(`${CODE}/__devbox/login`, { data: { password: WAKE_PASSWORD } });
  expect(login.status()).toBe(202);
  await expect
    .poll(async () => {
      const now = await status(request);
      return `${now.vm} ${now.phase} ${now.idle ? "polled" : "not polled"}`;
    })
    .toBe("running idle polled");
}

const state = (request: APIRequestContext, id: string) => async () =>
  (await sessions(request)).find((session) => session.id === id)?.state;

test.beforeEach(async ({ request }) => {
  await reset(request);
});

// Scenario 3 (docs/spec/15-testing.md): no traffic and an idle hub → the gateway puts the VM to
// sleep with its memory saved → a request of the app gets the asleep status → the app wakes it.
test("an idle devbox is put to sleep, and the app wakes it", async ({ request }) => {
  await wake(request);
  // Awake, the app's requests reach the hub, which wants its own sign-in.
  expect((await request.get(`${AGENTS}/api/state`)).status()).toBe(401);

  await set(request, { idle: "1s" });
  await expect.poll(async () => (await incus(request)).status).toBe("Stopped");
  const stopped = await incus(request);
  expect(stopped.stateful).toBe(true);
  expect(stopped.stateChanges?.at(-1)).toEqual({ action: "stop", stateful: true, timeout: 120 });

  const asleep = await request.get(`${AGENTS}/api/state`);
  expect(asleep.status()).toBe(503);
  const body = (await asleep.json()) as Status;
  expect(body).toMatchObject({
    vm: "stopped",
    phase: "idle",
    authed: true,
    lastStop: { mode: "stateful" },
  });
  expect(body.lastStop?.reason).toMatch(/^idle \d+m$/);
  // That request did not wake it.
  expect((await incus(request)).status).toBe("Stopped");

  await set(request, { idle: "15m" });
  expect((await request.post(`${AGENTS}/__devbox/wake`, { data: {} })).status()).toBe(202);
  await expect.poll(async () => (await status(request)).vm).toBe("running");
  await expect.poll(async () => (await request.get(`${AGENTS}/api/state`)).status()).toBe(401);
  expect((await incus(request)).stateChanges?.at(-1)).toEqual({
    action: "start",
    stateful: true,
    timeout: -1,
  });
});

// Scenario 4: an agent that is working keeps the VM awake; one that waits for the owner does not.
test("a working agent keeps the devbox awake, one waiting for an answer does not", async ({
  request,
}) => {
  await wake(request);
  const session = await startSession(request, {
    agent: "claude",
    cwd: "devbox",
    prompt: "a long job",
  });
  expect(session.name).toBe("claude-devbox");
  await expect.poll(state(request, session.id)).toBe("working");

  await set(request, { idle: "1s" });
  await expect
    .poll(async () => (await status(request)).idle?.hubReasons)
    .toEqual(["claude working in claude-devbox"]);
  // Several idle periods pass and the VM is still up.
  await new Promise((resolve) => setTimeout(resolve, 3000));
  expect((await incus(request)).status).toBe("Running");
  expect(await state(request, session.id)()).toBe("working");

  // The agent asks for permission: it now waits for the owner, and the VM may sleep.
  await typeInto(request, session.id, "ask");
  await expect.poll(state(request, session.id)).toBe("needs_input");
  await expect.poll(async () => (await incus(request)).status).toBe("Stopped");
  expect((await status(request)).lastStop?.mode).toBe("stateful");

  // It is still at the same prompt when the devbox comes back.
  await set(request, { idle: "15m" });
  expect((await request.post(`${CODE}/__devbox/wake`, { data: {} })).status()).toBe(202);
  await expect.poll(async () => (await status(request)).vm).toBe("running");
  const waiting = (await sessions(request)).find((candidate) => candidate.id === session.id);
  expect(waiting).toMatchObject({
    state: "needs_input",
    waitingFor: "Permission to use Bash: cargo test",
  });
});
