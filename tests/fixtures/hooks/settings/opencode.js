// Installed by devbox: reports this opencode session's lifecycle to the devbox hub, in the same
// vocabulary as the Claude Code and Codex hooks. Rewritten whenever devbox provisions the VM.
import { spawn } from "node:child_process";

const RELAY = "/home/dev/.local/lib/devbox/current/devbox-hub";

export const DevboxPlugin = async ({ directory }) => {
  // The relay prints nothing and never fails; it is not waited for.
  const report = (event, payload = {}) => {
    try {
      const relay = spawn(RELAY, ["event", "opencode", event], {
        cwd: directory,
        stdio: ["pipe", "ignore", "ignore"],
      });
      relay.on("error", () => {});
      relay.stdin.end(JSON.stringify({ cwd: directory, ...payload }));
    } catch {
      // The hub being unreachable must never disturb the agent.
    }
  };

  return {
    event: async ({ event }) => {
      const p = event.properties || {};
      const session_id = p.sessionID || p.info?.id;
      switch (event.type) {
        case "session.created":
          return report("SessionStart", { session_id });
        case "session.status":
          if (p.status?.type === "busy") return report("PreToolUse", { session_id });
          if (p.status?.type === "idle") return report("Stop", { session_id });
          return;
        case "session.idle":
          return report("Stop", { session_id });
        case "permission.asked":
        case "permission.updated": {
          const what = [p.permission || p.type || p.title, ...(p.patterns || [])].filter(Boolean);
          return report("PermissionRequest", { session_id, message: `Permission: ${what.join(" ")}` });
        }
        case "session.error":
          return report("Error", {
            session_id,
            error: p.error?.data?.message || p.error?.name || "error",
          });
        default:
          return;
      }
    },
    "tool.execute.before": async (input) => {
      if (input.tool === "question") {
        return report("PermissionRequest", {
          session_id: input.sessionID,
          message: "opencode has a question for you",
        });
      }
      return report("PreToolUse", { session_id: input.sessionID, tool_name: input.tool });
    },
    "chat.message": async (input, output) => {
      const text = (output?.parts || [])
        .filter((part) => part.type === "text")
        .map((part) => part.text)
        .join(" ");
      if (text) report("UserPromptSubmit", { session_id: input.sessionID, prompt: text });
    },
  };
};
