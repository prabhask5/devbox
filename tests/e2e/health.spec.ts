import { expect, test } from "@playwright/test";
import type { APIRequestContext } from "@playwright/test";
import { CODE, WAKE_PASSWORD, reset, set } from "./stack.ts";

interface Health {
  status: "ok" | "problem";
  problems: string[];
  vm: string;
}

async function health(request: APIRequestContext): Promise<{ code: number; body: Health }> {
  const response = await request.get(`${CODE}/__devbox/health`);
  return { code: response.status(), body: (await response.json()) as Health };
}

const problems = (request: APIRequestContext) => async () => (await health(request)).body.problems;

// Scenario 7 (docs/spec/15-testing.md): the gateway's health follows the faults injected around it.
test("health follows Incus, a failed wake and the VM's services", async ({ request }) => {
  await reset(request);
  await expect.poll(async () => (await health(request)).body.vm).toBe("stopped");
  expect(await health(request)).toMatchObject({ code: 200, body: { status: "ok", problems: [] } });

  // The Incus daemon goes away and comes back.
  await set(request, { incus: "down" });
  await expect
    .poll(problems(request))
    .toEqual(["Incus API unreachable: incus: connect: no such file or directory"]);
  expect((await health(request)).code).toBe(503);
  await set(request, { incus: "up" });
  await expect.poll(problems(request)).toEqual([]);

  // A wake that fails is a problem until one succeeds.
  await set(request, { start: "fail" });
  const login = await request.post(`${CODE}/__devbox/login`, { data: { password: WAKE_PASSWORD } });
  expect(login.status()).toBe(202);
  await expect
    .poll(problems(request))
    .toEqual(['last wake failed: incus: Failed to start device "eth0"']);
  await set(request, { start: "ok", startMs: "0" });
  expect((await request.post(`${CODE}/__devbox/wake`, { data: {} })).status()).toBe(202);
  await expect.poll(problems(request)).toEqual([]);
  expect((await health(request)).body.vm).toBe("running");

  // The VM is up but its services stop answering, one after the other.
  await set(request, { hub: "down" });
  await expect.poll(problems(request)).toEqual(["VM is up but the agents hub is not responding"]);
  await set(request, { code: "down" });
  await expect
    .poll(problems(request))
    .toEqual([
      "VM is up but code-server is not responding",
      "VM is up but the agents hub is not responding",
    ]);
  await set(request, { code: "up", hub: "up" });
  await expect.poll(problems(request)).toEqual([]);

  // Asleep is healthy.
  expect((await request.post(`${CODE}/__devbox/sleep`, { data: {} })).status()).toBe(202);
  await expect.poll(async () => (await health(request)).body.vm).toBe("stopped");
  expect(await health(request)).toMatchObject({ code: 200, body: { status: "ok", problems: [] } });
});
