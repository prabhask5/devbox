// opencode plugin: reports session lifecycle to the devbox agents hub (agents.prabhas.io), using the
// same event vocabulary as the Claude Code / Codex hooks. Linked to ~/.config/opencode/plugins/.
export const DevboxPlugin = async ({ directory }) => {
  const port = process.env.DEVBOX_HUB_PORT || 8090;
  const post = (event, payload = {}) =>
    fetch(`http://127.0.0.1:${port}/internal/event`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({
        agent: 'opencode', event,
        devboxSession: process.env.DEVBOX_SESSION || '', tmuxPane: process.env.TMUX_PANE || '',
        cwd: directory, payload: { cwd: directory, ...payload },
      }),
      signal: AbortSignal.timeout(2000),
    }).catch(() => {});

  return {
    event: async ({ event }) => {
      const p = event.properties || {};
      const sid = p.sessionID || p.info?.id;
      switch (event.type) {
        case 'session.created': return post('SessionStart', { session_id: sid });
        case 'session.status':
          if (p.status?.type === 'busy') return post('PreToolUse', { session_id: sid });
          if (p.status?.type === 'idle') return post('Stop', { session_id: sid });
          return;
        case 'session.idle': return post('Stop', { session_id: sid });
        case 'permission.asked':
        case 'permission.updated':
          return post('PermissionRequest', { session_id: sid, message: `Permission: ${[p.permission || p.type || p.title, ...(p.patterns || [])].filter(Boolean).join(' ')}` });
        case 'session.error':
          return post('Error', { session_id: sid, error: p.error?.data?.message || p.error?.name || 'error' });
        default: return;
      }
    },
    'tool.execute.before': async (input) => {
      if (input.tool === 'question') return post('PermissionRequest', { session_id: input.sessionID, message: 'opencode has a question for you' });
      return post('PreToolUse', { session_id: input.sessionID, tool_name: input.tool });
    },
    'chat.message': async (input, output) => {
      try {
        const text = (output?.parts || []).filter((x) => x.type === 'text').map((x) => x.text).join(' ');
        if (text) post('UserPromptSubmit', { session_id: input.sessionID, prompt: text });
      } catch { /* shape varies across versions */ }
    },
  };
};
