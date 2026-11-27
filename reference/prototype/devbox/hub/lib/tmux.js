'use strict';
// Thin async wrapper around the tmux CLI. Every agent session is a tmux session, so it survives
// browser/SSH disconnects, hub restarts and (with stateful stop) the VM going to sleep.
const { execFile } = require('node:child_process');

function tmux(args, { timeout = 5000 } = {}) {
  return new Promise((resolve, reject) => {
    execFile('tmux', args, { timeout, maxBuffer: 16e6 }, (err, stdout, stderr) => {
      if (err) { err.stderr = stderr; return reject(err); }
      resolve(stdout);
    });
  });
}

// tmux replaces control characters (even tabs) in -F output, so use a printable separator.
const SEP = '|:|';
const FIELDS = ['name', 'created', 'attached', 'activity', 'command', 'path', 'pid', 'width', 'height', 'devboxId'];
const FORMAT = ['#{session_name}', '#{session_created}', '#{session_attached}', '#{window_activity}',
  '#{pane_current_command}', '#{pane_current_path}', '#{pane_pid}', '#{window_width}', '#{window_height}', '#{@devbox_id}'].join(SEP);

async function list() {
  let out;
  try { out = await tmux(['list-sessions', '-F', FORMAT]); } catch (e) {
    if (/no server running|error connecting|No such file/i.test(e.stderr || e.message)) return [];
    throw e;
  }
  return out.split('\n').filter(Boolean).map((line) => {
    const v = line.split(SEP); const o = {};
    FIELDS.forEach((f, i) => { o[f] = v[i] ?? ''; });
    for (const k of ['created', 'attached', 'activity', 'pid', 'width', 'height']) o[k] = +o[k] || 0;
    o.created *= 1000; o.activity *= 1000;
    return o;
  });
}

const safeName = (s) => String(s).replace(/[^A-Za-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'session';

// Runs argv inside an interactive bash (so ~/.bashrc PATH/mise apply). When the agent exits the pane
// drops to a shell instead of vanishing, so you can read its last output and restart it.
async function newSession({ name, cwd, argv, env = {} }) {
  const script = '"$@"; code=$?; printf "\\n\\033[2m[%s exited (%s) — this is now a shell]\\033[0m\\n" "$1" "$code"; exec bash -i';
  const args = ['new-session', '-d', '-s', name, '-c', cwd, '-x', '120', '-y', '40'];
  for (const [k, v] of Object.entries(env)) args.push('-e', `${k}=${v}`);
  args.push('bash', '-ic', script, '_', ...argv);
  await tmux(args);
  if (env.DEVBOX_SESSION) await tmux(['set-option', '-t', name, '@devbox_id', env.DEVBOX_SESSION]);
}

const capture = (name, lines = 400) => tmux(['capture-pane', '-p', '-e', '-t', `${name}:`, '-S', `-${lines}`]);
const sendKeys = (name, keys) => tmux(['send-keys', '-t', `${name}:`, ...keys]);
const sendText = (name, text) => tmux(['send-keys', '-t', `${name}:`, '-l', '--', text]);
const kill = (name) => tmux(['kill-session', '-t', name]);
const rename = (name, to) => tmux(['rename-session', '-t', name, to]);
const setId = (name, id) => tmux(['set-option', '-t', name, '@devbox_id', id]);
const resize = (name, cols, rows) => tmux(['resize-window', '-t', `${name}:`, '-x', String(cols), '-y', String(rows)]);
const restoreSize = (name) => tmux(['set-option', '-w', '-t', `${name}:`, 'window-size', 'latest']);
async function clients(name) {
  try { return (await tmux(['list-clients', '-t', name, '-F', '#{client_name}'])).split('\n').filter(Boolean); } catch { return []; }
}
async function sessionOfPane(pane) {
  try { return (await tmux(['display-message', '-p', '-t', pane, '#{session_name}'])).trim() || null; } catch { return null; }
}

module.exports = { tmux, list, newSession, capture, sendKeys, sendText, kill, rename, setId, resize, restoreSize, clients, sessionOfPane, safeName };
