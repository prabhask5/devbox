'use strict';
// One entry per kind of agent. The hub is agent-agnostic: everything agent-specific lives here
// (how to launch it, which keys answer its prompts) plus a hook/plugin in ~/dotfiles/agents/ that
// reports its lifecycle events to the hub.
//
// kind 'tmux' = a terminal program in a tmux session (all current agents).
// kind 'chat' = reserved for a future local-LLM assistant that the hub talks to directly over an API
//               (messages instead of a terminal screen). The app already has a tab for it.
const { execFileSync } = require('node:child_process');

const has = (bin) => { try { execFileSync('bash', ['-ic', `command -v ${bin}`], { stdio: 'ignore', timeout: 4000 }); return true; } catch { return false; } };

const COMMON_KEYS = [
  { label: 'Enter', keys: ['Enter'] }, { label: 'Esc', keys: ['Escape'] },
  { label: '↑', keys: ['Up'] }, { label: '↓', keys: ['Down'] }, { label: 'Tab', keys: ['Tab'] },
  { label: '^C', keys: ['C-c'] },
];

const ADAPTERS = {
  claude: {
    label: 'Claude Code', short: 'Claude', kind: 'tmux', bin: 'claude', hooks: true,
    argv: ({ prompt, resume }) => ['claude', ...(resume ? ['--continue'] : []), ...(prompt ? [prompt] : [])],
    // Claude's permission prompt: 1 = yes, 2 = yes + don't ask again, 3/Esc = no
    answerKeys: [{ label: 'Yes', keys: ['1'], tone: 'ok' }, { label: 'Always', keys: ['2'] }, { label: 'No', keys: ['Escape'], tone: 'bad' }],
  },
  codex: {
    label: 'Codex', short: 'Codex', kind: 'tmux', bin: 'codex', hooks: true,
    argv: ({ prompt, resume }) => ['codex', ...(resume ? ['resume', '--last'] : []), ...(prompt ? [prompt] : [])],
    answerKeys: [{ label: 'Yes', keys: ['y'], tone: 'ok' }, { label: 'Always', keys: ['a'] }, { label: 'No', keys: ['Escape'], tone: 'bad' }],
  },
  opencode: {
    label: 'opencode', short: 'opencode', kind: 'tmux', bin: 'opencode', hooks: true,
    argv: ({ prompt, resume }) => ['opencode', ...(resume ? ['--continue'] : []), ...(prompt ? ['--prompt', prompt] : [])],
    answerKeys: [{ label: 'Allow', keys: ['Enter'], tone: 'ok' }, { label: 'Reject', keys: ['Escape'], tone: 'bad' }],
  },
  shell: {
    label: 'Shell', short: 'Shell', kind: 'tmux', bin: 'bash', hooks: false,
    argv: ({ prompt }) => (prompt ? ['bash', '-ic', `${prompt}; exec bash -i`] : ['bash', '-i']),
    answerKeys: [],
  },
};

let availability = null;
function available() {
  if (!availability) availability = Object.fromEntries(Object.entries(ADAPTERS).map(([k, a]) => [k, has(a.bin)]));
  return availability;
}
setInterval(() => { availability = null; }, 10 * 60000).unref();

// Guess which agent is running in a tmux session the hub didn't create.
function guess(command) {
  const c = String(command || '').toLowerCase();
  if (c.includes('claude')) return 'claude';
  if (c.includes('codex')) return 'codex';
  if (c.includes('opencode')) return 'opencode';
  return 'shell';
}

function describe() {
  const av = available();
  return Object.fromEntries(Object.entries(ADAPTERS).map(([k, a]) => [k, {
    label: a.label, short: a.short || a.label, kind: a.kind, available: av[k], answerKeys: a.answerKeys, hooks: a.hooks,
  }]));
}

module.exports = { ADAPTERS, COMMON_KEYS, available, guess, describe };
