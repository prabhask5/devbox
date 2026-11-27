import type { APIRequestContext } from "@playwright/test";

/** The stack tests/e2e/harness runs: where it listens and what the tests know about it. */
const CONTROL = "http://127.0.0.1:47100";
export const CODE = "https://127.0.0.1:47101";
export const AGENTS = "https://127.0.0.1:47102";
export const WAKE_PASSWORD = "FAKE-password";

/** Back to "asleep with saved memory, every service fine" behind a gateway that remembers nothing. */
export async function reset(request: APIRequestContext): Promise<void> {
  const response = await request.post(`${CONTROL}/reset`);
  if (!response.ok()) throw new Error(`reset: ${await response.text()}`);
}

/** Steers the fakes, e.g. `{ incus: "down" }`, `{ hub: "down" }`, `{ sync: "hold" }`, `{ start: "fail" }`. */
export async function set(
  request: APIRequestContext,
  changes: Record<string, string>,
): Promise<void> {
  const response = await request.post(`${CONTROL}/set`, { params: changes });
  if (!response.ok()) throw new Error(`set: ${await response.text()}`);
}

export interface IncusState {
  status: string;
  stateful: boolean;
  stateChanges: { action: string; stateful?: boolean }[] | null;
}

/** What the fake Incus has been asked to do. */
export async function incus(request: APIRequestContext): Promise<IncusState> {
  return (await (await request.get(`${CONTROL}/incus`)).json()) as IncusState;
}

export interface AgentSession {
  id: string;
  name: string;
  state: string;
  waitingFor?: string;
}

/** Starts an agent session in the hub, as the app and the `agent` command do. */
export async function startSession(
  request: APIRequestContext,
  session: { agent: string; cwd?: string; prompt?: string },
): Promise<AgentSession> {
  const response = await request.post(`${CONTROL}/sessions`, { data: session });
  if (!response.ok()) throw new Error(`session: ${await response.text()}`);
  return (await response.json()) as AgentSession;
}

/** Types a line into a session's terminal. */
export async function typeInto(request: APIRequestContext, id: string, text: string) {
  const response = await request.post(`${CONTROL}/sessions/${id}/type`, { data: { text } });
  if (!response.ok()) throw new Error(`type: ${await response.text()}`);
}

/** The hub's sessions. */
export async function sessions(request: APIRequestContext): Promise<AgentSession[]> {
  const state = (await (await request.get(`${CONTROL}/sessions`)).json()) as {
    sessions: AgentSession[];
  };
  return state.sessions;
}
