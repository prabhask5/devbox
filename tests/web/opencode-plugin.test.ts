import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { expect, test } from "vitest";

// The plugin devbox installs into opencode (docs/spec/05-agents-and-hooks.md), run as opencode
// runs it: its exported function is called with the project's directory, and what it returns
// is called with opencode's events — `event` with { type, properties }, `tool.execute.before`
// with { tool, sessionID, callID }, `chat.message` with the message and its parts. The relay is
// a script that writes down what it was started with and what it was given.

type Hook = (...args: unknown[]) => Promise<unknown>;
interface Reported {
  event: string;
  payload: Record<string, unknown>;
}

async function plugin(): Promise<{
  hooks: Record<string, Hook>;
  reported: () => Promise<Reported[]>;
}> {
  const dir = mkdtempSync(join(tmpdir(), "dbx-plugin-"));
  const log = join(dir, "reported");
  const relay = join(dir, "relay");
  writeFileSync(relay, `#!/bin/sh\nprintf '%s\\t%s\\n' "$*" "$(cat)" >>"${log}"\n`);
  chmodSync(relay, 0o755);
  writeFileSync(log, "");
  const source = readFileSync(
    new URL("../../internal/hooks/opencode-plugin.js", import.meta.url),
    "utf8",
  );
  const file = join(dir, "devbox.mjs");
  writeFileSync(file, source.replaceAll("__RELAY__", relay));
  const module = (await import(/* @vite-ignore */ pathToFileURL(file).href)) as Record<
    string,
    unknown
  >;
  // opencode loads every export of a plugin file, and each must be a function.
  expect(Object.values(module).every((exported) => typeof exported === "function")).toBe(true);
  const make = module.DevboxPlugin as (context: {
    directory: string;
  }) => Promise<Record<string, Hook>>;
  const hooks = await make({ directory: dir });
  const read = (): Reported[] =>
    readFileSync(log, "utf8")
      .split("\n")
      .filter(Boolean)
      .map((line) => {
        const [args = "", body = "{}"] = line.split("\t");
        expect(args.startsWith("event opencode ")).toBe(true);
        const payload = JSON.parse(body) as Record<string, unknown>;
        expect(payload.cwd).toBe(dir);
        return { event: args.replace("event opencode ", ""), payload };
      });
  // The relay is not waited for by the plugin: the test waits until nothing more arrives.
  const reported = async (): Promise<Reported[]> => {
    let seen = -1;
    for (let quiet = 0; quiet < 4;) {
      await new Promise((resolve) => setTimeout(resolve, 50));
      const now = read().length;
      quiet = now === seen ? quiet + 1 : 0;
      seen = now;
    }
    return read();
  };
  return { hooks, reported };
}

const event = (type: string, properties: Record<string, unknown>) => ({
  event: { id: "e", type, properties },
});

test("a turn of opencode is reported once, in the hub's vocabulary", async () => {
  const { hooks, reported } = await plugin();
  const send = async (name: string, ...args: unknown[]) => {
    await hooks[name]?.(...args);
    // One at a time, as they happen: the relays are separate processes.
    await reported();
  };
  await send("event", event("session.created", { sessionID: "ses_1", info: { id: "ses_1" } }));
  await send(
    "chat.message",
    { sessionID: "ses_1" },
    { message: {}, parts: [{ type: "text", text: "run the tests" }] },
  );
  await send("event", event("session.status", { sessionID: "ses_1", status: { type: "busy" } }));
  await send(
    "tool.execute.before",
    { tool: "bash", sessionID: "ses_1", callID: "c1" },
    { args: {} },
  );
  await send(
    "event",
    event("permission.asked", {
      id: "per_1",
      sessionID: "ses_1",
      permission: "bash",
      patterns: ["cargo test"],
    }),
  );
  await send(
    "event",
    event("permission.replied", { sessionID: "ses_1", requestID: "per_1", reply: "once" }),
  );
  // The end of a turn arrives as two events of opencode's; it is one Stop.
  await send("event", event("session.status", { sessionID: "ses_1", status: { type: "idle" } }));
  await send("event", event("session.idle", { sessionID: "ses_1" }));
  await send(
    "tool.execute.before",
    { tool: "question", sessionID: "ses_1", callID: "c2" },
    { args: {} },
  );
  await send(
    "event",
    event("session.error", {
      sessionID: "ses_1",
      error: { name: "APIError", data: { message: "rate limited" } },
    }),
  );
  // What the plugin has no use for is not reported.
  await send("event", event("message.updated", { sessionID: "ses_1" }));
  await send("event", event("session.status", { sessionID: "ses_1", status: { type: "retry" } }));

  const all = await reported();
  expect(all.map((one) => one.event)).toEqual([
    "SessionStart",
    "UserPromptSubmit",
    "PreToolUse",
    "PreToolUse",
    "PermissionRequest",
    "PreToolUse",
    "Stop",
    "PermissionRequest",
    "Error",
  ]);
  expect(all.every((one) => one.payload.session_id === "ses_1")).toBe(true);
  expect(all[1]?.payload.prompt).toBe("run the tests");
  expect(all[3]?.payload.tool_name).toBe("bash");
  expect(all[4]?.payload.message).toBe("Permission: bash cargo test");
  expect(all[7]?.payload.message).toBe("opencode has a question for you");
  expect(all[8]?.payload.error).toBe("rate limited");
});

test("a relay that is not there never disturbs opencode", async () => {
  const dir = mkdtempSync(join(tmpdir(), "dbx-plugin-"));
  const source = readFileSync(
    new URL("../../internal/hooks/opencode-plugin.js", import.meta.url),
    "utf8",
  );
  const file = join(dir, "devbox.mjs");
  writeFileSync(file, source.replaceAll("__RELAY__", join(dir, "no-such-relay")));
  const module = (await import(/* @vite-ignore */ pathToFileURL(file).href)) as {
    DevboxPlugin: (context: { directory: string }) => Promise<Record<string, Hook>>;
  };
  const hooks = await module.DevboxPlugin({ directory: dir });
  await expect(
    hooks.event?.(event("session.created", { sessionID: "ses_1" })),
  ).resolves.toBeUndefined();
  await new Promise((resolve) => setTimeout(resolve, 100));
});
