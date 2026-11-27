#!/usr/bin/env node
'use strict';
// devbox agents hub — runs inside the devbox VM (systemd --user unit devbox-hub.service).
//
//  * agent sessions (Claude Code, Codex, opencode, shells, ...) live in tmux, so they never get cut off
//  * agents report lifecycle events through hooks -> POST /internal/event (see ~/dotfiles/agents/)
//  * web push to your phone when an agent needs you / finishes / errors
//  * a mobile PWA (agents.prabhas.io) to watch, answer and start sessions
//  * tells the host gateway whether the VM is busy (idle detection) and runs the repo sync on wake
//
// Trust model: requests that came through the gateway carry X-Forwarded-Host and need the login
// cookie. Requests without it come from the gateway itself (only /devbox/*) or from inside the VM
// on loopback (everything, used by hooks and the `agent` CLI).

const http = require('node:http');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawn, execFile } = require('node:child_process');

const store = require('./lib/store');
const tmux = require('./lib/tmux');
const adapters = require('./lib/adapters');

const HOME = os.homedir();
const CFG = {
  port: +(process.env.HUB_PORT || 8090),
  host: process.env.HUB_HOST || '0.0.0.0',
  codeDir: process.env.DEVBOX_CODE_DIR || path.join(HOME, 'code'),
  syncBin: process.env.DEVBOX_SYNC_BIN || path.join(__dirname, '..', 'bin', 'devbox-sync'),
  contextBin: path.join(__dirname, '..', 'bin', 'devbox-context'),
};
const config = new store.JsonFile(path.join(store.CONFIG_DIR, 'hub.json'), {}, 0o600);

// ------------------------------------------------------------------ CLI: `node server.js passwd`
if (process.argv[2] === 'passwd') {
  const rl = require('node:readline').createInterface({ input: process.stdin, output: process.stdout, terminal: true });
  rl._writeToOutput = (s) => { if (s.includes('password')) process.stdout.write(s); };
  rl.question('New agents-app password: ', (pw) => {
    rl.close(); process.stdout.write('\n');
    if (pw.length < 8) { console.error('Use at least 8 characters.'); process.exit(1); }
    const salt = crypto.randomBytes(16);
    config.data.passwordHash = `scrypt$${salt.toString('hex')}$${crypto.scryptSync(pw, salt, 32).toString('hex')}`;
    config.data.cookieSecret = crypto.randomBytes(32).toString('hex'); // logs out every device
    config.flush();
    console.log('Saved. Restart the hub: systemctl --user restart devbox-hub');
  });
  return;
}

if (!config.data.cookieSecret) { config.data.cookieSecret = crypto.randomBytes(32).toString('hex'); config.flush(); }
config.data.vapidSubject ||= 'mailto:prabhas.kurapati@gmail.com';
config.data.finishedNotifyAfterSec ??= 30;
config.data.needsInputDelaySec ??= 5;
let push = null;
try { push = require('./lib/push'); push.init(config); } catch (e) { console.error('web push disabled:', e.message); }

const sessions = new store.JsonFile(path.join(store.STATE_DIR, 'sessions.json'), { list: [] });
const subs = new store.JsonFile(path.join(store.STATE_DIR, 'push-subscriptions.json'), { list: [] }, 0o600);
const now = () => Date.now();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const newId = () => crypto.randomBytes(4).toString('hex');
const byId = (id) => sessions.data.list.find((s) => s.id === id);
const clip = (s, n = 200) => { s = String(s || '').replace(/\s+/g, ' ').trim(); return s.length > n ? `${s.slice(0, n - 1)}…` : s; };
const ACTIVE = new Set(['starting', 'working', 'needs_input', 'idle']);

// ------------------------------------------------------------------ change fan-out (SSE)
const streams = new Set();
let changeTimer = null;
function changed() {
  sessions.save();
  clearTimeout(changeTimer);
  changeTimer = setTimeout(() => { const msg = `data: ${JSON.stringify(snapshot())}\n\n`; for (const r of streams) r.write(msg); }, 120);
}
function publicSession(s) {
  const { live, ...rest } = s;
  return { ...rest, attached: live ? live.attached > 0 : false, size: live ? { cols: live.width, rows: live.height } : null, viewers: viewers.get(s.id) || 0 };
}
function snapshot() {
  return {
    type: 'state', serverNow: now(),
    sessions: sessions.data.list.map(publicSession).sort((a, b) => (b.stateSince || 0) - (a.stateSince || 0)),
    devbox: devboxStatus(),
    adapters: adapters.describe(),
    commonKeys: adapters.COMMON_KEYS,
    vapidPublicKey: config.data.vapid?.publicKey || null,
    pushDevices: subs.data.list.length,
  };
}

// ------------------------------------------------------------------ notifications
const viewers = new Map(); // sessionId -> number of open screen streams
const lastPush = new Map();
async function notify(s, kind, title, body, { urgent = false } = {}) {
  if ((viewers.get(s.id) || 0) > 0) return; // you're looking at it already
  const key = `${s.id}:${kind}`;
  if (now() - (lastPush.get(key) || 0) < 15000) return;
  lastPush.set(key, now());
  s.unread = true;
  store.appendEvent({ type: 'notify', sessionId: s.id, kind, title, body });
  if (!push || !subs.data.list.length) return;
  const badge = sessions.data.list.filter((x) => x.state === 'needs_input' || x.unread).length;
  push.sendAll(subs, { title, body: clip(body, 180), url: `/#/s/${s.id}`, tag: `s-${s.id}`, sessionId: s.id, badge, urgency: urgent ? 'high' : 'normal' }).catch(() => {});
}
async function notifySystem(title, body) {
  store.appendEvent({ type: 'notify', kind: 'system', title, body });
  if (push && subs.data.list.length) push.sendAll(subs, { title, body: clip(body, 180), url: '/#/', tag: 'devbox' }).catch(() => {});
}

// ------------------------------------------------------------------ sessions
function repoOf(cwd) {
  const rel = path.relative(CFG.codeDir, cwd || '');
  return rel && !rel.startsWith('..') && !path.isAbsolute(rel) ? rel.split(path.sep)[0] : null;
}
function setState(s, state, extra = {}) {
  if (s.state !== state) { s.state = state; s.stateSince = now(); }
  Object.assign(s, extra);
  changed();
}
function uniqueTmuxName(base, live) {
  const taken = new Set([...live.map((t) => t.name), ...sessions.data.list.map((s) => s.tmux)]);
  let name = tmux.safeName(base); let i = 2;
  while (taken.has(name)) name = `${tmux.safeName(base)}-${i++}`;
  return name;
}

async function createSession({ agent = 'claude', cwd, name, prompt, resume }) {
  const a = adapters.ADAPTERS[agent];
  if (!a) throw Object.assign(new Error(`unknown agent ${agent}`), { status: 400 });
  if (a.kind !== 'tmux') throw Object.assign(new Error(`${a.label} sessions are not supported yet`), { status: 400 });
  let dir = cwd ? (path.isAbsolute(cwd) ? cwd : path.join(CFG.codeDir, cwd)) : HOME;
  dir = path.resolve(dir);
  if (!dir.startsWith(HOME)) throw Object.assign(new Error(`${dir} is outside your home directory`), { status: 400 });
  if (!fs.existsSync(dir)) throw Object.assign(new Error(`no such directory: ${dir}`), { status: 400 });
  const repo = repoOf(dir);
  const live = await tmux.list();
  const id = newId();
  const tname = uniqueTmuxName(name || `${agent}-${repo || path.basename(dir) || 'home'}`, live);
  await ensureTmuxServer();
  await tmux.newSession({ name: tname, cwd: dir, argv: a.argv({ prompt, resume }), env: { DEVBOX_SESSION: id, DEVBOX_AGENT: agent } });
  const s = {
    id, name: name || tname, tmux: tname, agent, kind: 'tmux', cwd: dir, repo, createdAt: now(),
    state: 'starting', stateSince: now(), title: prompt ? clip(prompt, 80) : '', lastPrompt: prompt ? clip(prompt, 300) : '',
  };
  if (prompt && a.hooks) s.turnStartedAt = now();
  sessions.data.list.push(s);
  store.appendEvent({ type: 'session.create', sessionId: id, agent, repo, cwd: dir, prompt: prompt ? clip(prompt, 500) : undefined });
  changed();
  setTimeout(reconcile, 300);
  return s;
}

async function ensureTmuxServer() {
  // Prefer the systemd-managed tmux server so sessions don't live in the hub's cgroup.
  await new Promise((r) => execFile('systemctl', ['--user', 'start', 'devbox-tmux.service'], { timeout: 5000 }, () => r()));
}

function hasChildren(pid) {
  if (!pid) return true;
  try { return fs.readFileSync(`/proc/${pid}/task/${pid}/children`, 'utf8').trim().length > 0; } catch { return true; }
}

let reconciling = false;
async function reconcile() {
  if (reconciling) return; reconciling = true;
  try {
    const live = await tmux.list();
    const list = sessions.data.list;
    const t = now();
    for (const s of list) {
      if (s.kind !== 'tmux') continue;
      const l = live.find((x) => x.name === s.tmux) || live.find((x) => x.devboxId && x.devboxId === s.id);
      if (!l) { if (s.state !== 'gone') setState(s, 'gone', { live: null }); else s.live = null; continue; }
      if (l.name !== s.tmux) { s.tmux = l.name; changed(); }
      const prev = s.live; s.live = l;
      if (!prev || prev.attached !== l.attached || prev.width !== l.width) changed();
      if (s.agent === 'shell') {
        const st = t - l.activity < 4000 ? 'working' : 'idle';
        if (s.state !== st && s.state !== 'gone') setState(s, st);
        continue;
      }
      // The pane runs `bash -ic '<agent>; exec bash -i'`: while the agent runs, that bash has a child;
      // once the agent exits it becomes a plain shell with none. (pane_current_command is unreliable:
      // script-based agents show up as their interpreter.)
      const agentGone = t - s.createdAt > 8000 && !hasChildren(l.pid);
      if (agentGone && ACTIVE.has(s.state)) setState(s, 'exited', { waitingFor: null });
      if (!agentGone && s.state === 'exited') setState(s, 'idle');
      // a missed Stop hook (crash, interrupt): the TUI animates while working, so silence = not working
      if (s.state === 'working' && t - l.activity > 45000 && t - (s.lastEventAt || 0) > 45000) setState(s, 'idle');
      if (s.state === 'starting' && t - s.createdAt > 20000) setState(s, 'idle');
    }
    // adopt tmux sessions created elsewhere (e.g. `tmux new -s foo` in a terminal)
    for (const l of live) {
      if (list.some((s) => s.tmux === l.name || (l.devboxId && s.id === l.devboxId))) continue;
      const id = l.devboxId || newId();
      const agent = adapters.guess(l.command);
      list.push({ id, name: l.name, tmux: l.name, agent, kind: 'tmux', cwd: l.path, repo: repoOf(l.path), createdAt: l.created || t,
        state: agent === 'shell' ? 'idle' : 'idle', stateSince: t, adopted: true, title: '', live: l });
      tmux.setId(l.name, id).catch(() => {});
      changed();
    }
    // forget finished sessions after a day
    const before = list.length;
    sessions.data.list = list.filter((s) => !((s.state === 'gone' || s.state === 'ended') && t - s.stateSince > 864e5));
    if (sessions.data.list.length !== before) changed();
    updateIdle();
  } catch (e) {
    console.error('reconcile:', e.message);
  } finally { reconciling = false; }
}
setInterval(reconcile, 2000).unref();

// ------------------------------------------------------------------ agent hook events
function lastAssistantText(transcriptPath) {
  try {
    if (!transcriptPath || !fs.existsSync(transcriptPath)) return '';
    const st = fs.statSync(transcriptPath);
    const fd = fs.openSync(transcriptPath, 'r');
    const len = Math.min(st.size, 256 * 1024);
    const buf = Buffer.alloc(len); fs.readSync(fd, buf, 0, len, st.size - len); fs.closeSync(fd);
    const lines = buf.toString('utf8').split('\n').reverse();
    for (const line of lines) {
      try {
        const j = JSON.parse(line);
        if (j.type !== 'assistant') continue;
        const c = j.message?.content;
        const text = Array.isArray(c) ? c.filter((x) => x.type === 'text').map((x) => x.text).join(' ') : typeof c === 'string' ? c : '';
        if (text.trim()) return text;
      } catch { /* partial line */ }
    }
  } catch { /* ignore */ }
  return '';
}

function toolSummary(p) {
  const name = p.tool_name || p.tool || '';
  const inp = p.tool_input || p.input || {};
  const detail = inp.command || inp.file_path || inp.path || inp.url || inp.pattern || inp.description || '';
  return clip(name + (detail ? `: ${detail}` : ''), 140);
}

async function onEvent(body) {
  let p = body.payload;
  if (typeof p === 'string') { try { p = JSON.parse(p || '{}'); } catch { p = {}; } }
  p = p || {};
  const agent = body.agent || 'unknown';
  let ev = body.event;
  if (ev === 'Notification') {
    const nt = p.notification_type || '';
    if (/permission|elicitation_dialog|agent_needs_input/.test(nt)) ev = 'PermissionRequest';
    else return; // idle_prompt, auth_success, ... — nothing to do
  }
  if (ev === 'PostToolUse') ev = 'PreToolUse';
  const agentSid = p.session_id || p.sessionID || p.sessionId || null;

  // find the session this event belongs to
  let s = body.devboxSession ? byId(body.devboxSession) : null;
  if (!s && body.tmuxPane) {
    const name = await tmux.sessionOfPane(body.tmuxPane);
    if (name) { await reconcile(); s = sessions.data.list.find((x) => x.tmux === name) || null; }
  }
  if (!s && agentSid) s = sessions.data.list.find((x) => x.agentSessionId === agentSid) || null;
  if (!s) {
    const cwd = p.cwd || body.cwd || HOME;
    s = { id: newId(), name: `${agent} · ${repoOf(cwd) || path.basename(cwd)}`, agent, kind: 'external', external: true, cwd, repo: repoOf(cwd),
      createdAt: now(), state: 'idle', stateSince: now(), title: '' };
    sessions.data.list.push(s);
  }
  if (s.agent === 'shell' && agent !== 'shell') s.agent = agent; // adopted session turned out to run an agent
  if (agentSid) s.agentSessionId = agentSid;
  if (p.transcript_path) s.transcriptPath = p.transcript_path;
  s.lastEventAt = now();

  const label = s.title ? `${s.name} — ${clip(s.title, 60)}` : s.name;
  const agentLabel = adapters.ADAPTERS[s.agent]?.label || s.agent;
  switch (ev) {
    case 'SessionStart':
      if (!['working', 'needs_input'].includes(s.state)) setState(s, 'idle');
      break;
    case 'UserPromptSubmit': {
      const prompt = p.prompt || p.user_prompt || '';
      if (!s.title && prompt) s.title = clip(prompt, 80);
      setState(s, 'working', { lastPrompt: clip(prompt, 300), turnStartedAt: now(), waitingFor: null, unread: false });
      store.appendEvent({ type: 'prompt', sessionId: s.id, agent: s.agent, repo: s.repo, prompt: clip(prompt, 2000) });
      break;
    }
    case 'PreToolUse':
      setState(s, 'working', { lastTool: toolSummary(p), waitingFor: null });
      break;
    case 'PermissionRequest': {
      const what = clip(p.message || (p.tool_name ? `Permission to use ${toolSummary(p)}` : 'Needs your input'), 240);
      // Claude fires both PermissionRequest (immediately) and Notification (after ~6s) for one prompt.
      const repeat = s.state === 'needs_input' && (s.waitingFor === what || ev !== body.event);
      setState(s, 'needs_input', { waitingFor: repeat && ev !== body.event ? s.waitingFor : what });
      if (repeat) break;
      store.appendEvent({ type: 'needs_input', sessionId: s.id, agent: s.agent, repo: s.repo, what });
      // Don't buzz your phone if you answer within a few seconds (longer when a terminal is attached).
      const token = s.stateSince;
      const delay = (s.live?.attached ? 60 : config.data.needsInputDelaySec) * 1000;
      setTimeout(() => {
        const cur = byId(s.id);
        if (cur && cur.state === 'needs_input' && cur.stateSince === token) notify(cur, 'needs_input', `🔐 ${label}`, cur.waitingFor || what, { urgent: true });
      }, delay);
      break;
    }
    case 'Stop': {
      const took = s.turnStartedAt ? (now() - s.turnStartedAt) / 1000 : 0;
      const text = p.last_assistant_message || p['last-assistant-message'] || lastAssistantText(s.transcriptPath);
      setState(s, 'idle', { waitingFor: null, lastReply: clip(text, 400), turnStartedAt: null });
      store.appendEvent({ type: 'turn.done', sessionId: s.id, agent: s.agent, repo: s.repo, seconds: Math.round(took) });
      if (took >= config.data.finishedNotifyAfterSec) notify(s, 'done', `✅ ${label}`, text || `${agentLabel} finished after ${Math.round(took)}s`);
      break;
    }
    case 'Error':
      setState(s, 'error', { waitingFor: clip(p.error || p.message || 'error', 240) });
      notify(s, 'error', `❌ ${label}`, p.error || p.message || `${agentLabel} hit an error`, { urgent: true });
      break;
    case 'SessionEnd':
      setState(s, s.external ? 'ended' : 'exited', { waitingFor: null });
      break;
    default:
      break;
  }
  changed();
}

// ------------------------------------------------------------------ idle detection (read by the gateway)
let lastBusy = now();
let idleInfo = { busy: true, reasons: ['starting up'], idleSeconds: 0 };
function updateIdle() {
  const t = now();
  const reasons = [];
  for (const s of sessions.data.list) {
    if (s.state !== 'working') continue;
    if (s.agent === 'shell') reasons.push(`output in ${s.name}`);
    else if (s.kind === 'external' && t - (s.lastEventAt || 0) > 30 * 60000) continue; // stale
    else reasons.push(`${s.agent} working in ${s.name}`);
  }
  if (sync.state === 'running') reasons.push('repo sync running');
  const load = os.loadavg()[0]; const cpus = os.cpus().length;
  if (load > 0.7 * cpus) reasons.push(`cpu load ${load.toFixed(1)}`);
  if (reasons.length) lastBusy = t;
  idleInfo = { busy: reasons.length > 0, reasons, idleSeconds: Math.round((t - lastBusy) / 1000) };
}

// ------------------------------------------------------------------ repo sync
const sync = { runId: null, state: 'idle', startedAt: null, finishedAt: null, total: 0, done: 0, current: null, results: [], reason: null };
function startSync(reason) {
  if (sync.state === 'running') return sync.runId;
  Object.assign(sync, { runId: `${now().toString(36)}`, state: 'running', startedAt: now(), finishedAt: null, total: 0, done: 0, current: null, results: [], reason });
  changed();
  const child = spawn(CFG.syncBin, ['--json'], { env: { ...process.env }, stdio: ['ignore', 'pipe', 'pipe'] });
  let buf = '';
  child.stdout.on('data', (d) => {
    buf += d;
    let i;
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i); buf = buf.slice(i + 1);
      let m; try { m = JSON.parse(line); } catch { continue; }
      if (m.type === 'start') sync.total = m.total;
      if (m.type === 'begin') sync.current = m.name;
      if (m.type === 'repo') { sync.results.push({ name: m.name, status: m.status, detail: m.detail || '' }); sync.done = sync.results.length; sync.current = null; }
      changed();
    }
  });
  child.stderr.on('data', (d) => process.stderr.write(`[sync] ${d}`));
  const finish = (code) => {
    if (sync.state !== 'running') return;
    Object.assign(sync, { state: 'done', finishedAt: now(), current: null, exitCode: code });
    store.appendEvent({ type: 'sync', reason, results: sync.results });
    const bad = sync.results.filter((r) => r.status === 'conflict' || r.status === 'error');
    if (bad.length) notifySystem(`⚠️ ${bad.length} repo${bad.length > 1 ? 's' : ''} couldn't sync`, bad.map((r) => `${r.name}: ${r.detail}`).join('\n'));
    changed();
  };
  child.on('exit', finish);
  child.on('error', (e) => { sync.results.push({ name: 'sync', status: 'error', detail: e.message }); finish(-1); });
  return sync.runId;
}
function devboxStatus() {
  return { idle: idleInfo, sync, uptime: Math.round(os.uptime()), load: os.loadavg()[0], hubStartedAt: STARTED };
}
const STARTED = now();

function fixClock(gatewayNow) {
  if (!gatewayNow) return;
  const skew = gatewayNow - now();
  if (Math.abs(skew) < 5000) return;
  console.log(`clock is off by ${Math.round(skew / 1000)}s after restore; fixing`);
  execFile('sudo', ['-n', 'date', '-s', `@${Math.round(gatewayNow / 1000)}`], { timeout: 5000 }, () => {
    execFile('sudo', ['-n', 'systemctl', 'restart', 'systemd-timesyncd'], { timeout: 10000 }, () => {});
  });
}
function maybeRefreshContext() {
  const marker = path.join(store.STATE_DIR, 'context-last-run');
  let age = Infinity; try { age = now() - fs.statSync(marker).mtimeMs; } catch { /* never ran */ }
  if (age > 6 * 3600e3) execFile('systemctl', ['--user', 'start', '--no-block', 'devbox-context.service'], () => {});
}

// ------------------------------------------------------------------ auth
const sign = (v) => crypto.createHmac('sha256', config.data.cookieSecret).update(v).digest('base64url');
function cookies(req) {
  const out = {};
  for (const part of String(req.headers.cookie || '').split(';')) { const i = part.indexOf('='); if (i > 0) out[part.slice(0, i).trim()] = decodeURIComponent(part.slice(i + 1).trim()); }
  return out;
}
function loggedIn(req) {
  const c = cookies(req).hub_session; if (!c) return false;
  const [exp, sig] = c.split('.'); if (!exp || !sig || +exp < now()) return false;
  const a = Buffer.from(sig), b = Buffer.from(sign(exp));
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
function checkPassword(pw) {
  const h = config.data.passwordHash; if (!h) return false;
  const [, salt, hash] = h.split('$');
  const got = crypto.scryptSync(String(pw || ''), Buffer.from(salt, 'hex'), 32), want = Buffer.from(hash, 'hex');
  return got.length === want.length && crypto.timingSafeEqual(got, want);
}
const attempts = new Map();
function rateLimited(ip) {
  const t = now(); const xs = (attempts.get(ip) || []).filter((x) => t - x < 60000); xs.push(t); attempts.set(ip, xs); return xs.length > 6;
}
const isLoopback = (req) => ['127.0.0.1', '::1', '::ffff:127.0.0.1'].includes(req.socket.remoteAddress);
const forwarded = (req) => !!req.headers['x-forwarded-host'];

// ------------------------------------------------------------------ HTTP helpers
function send(res, code, body, type = 'application/json') {
  res.writeHead(code, { 'content-type': type, 'cache-control': 'no-store' });
  res.end(type === 'application/json' ? JSON.stringify(body) : body);
}
function readJsonBody(req, max = 256 * 1024) {
  return new Promise((resolve, reject) => {
    let b = ''; req.setEncoding('utf8');
    req.on('data', (c) => { b += c; if (b.length > max) { reject(Object.assign(new Error('body too large'), { status: 413 })); req.destroy(); } });
    req.on('end', () => { if (!b) return resolve({}); try { resolve(JSON.parse(b)); } catch { reject(Object.assign(new Error('bad json'), { status: 400 })); } });
  });
}
const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.png': 'image/png', '.webmanifest': 'application/manifest+json', '.json': 'application/json' };
const PUBLIC = path.join(__dirname, 'public');
function serveStatic(req, res, pathname) {
  let rel = pathname === '/' ? '/index.html' : pathname;
  const file = path.join(PUBLIC, path.normalize(rel).replace(/^(\.\.[/\\])+/, ''));
  if (!file.startsWith(PUBLIC) || !fs.existsSync(file) || !fs.statSync(file).isFile()) return send(res, 404, { error: 'not found' });
  const ext = path.extname(file);
  res.writeHead(200, { 'content-type': MIME[ext] || 'application/octet-stream', 'cache-control': ext === '.html' || rel === '/sw.js' ? 'no-cache' : 'public, max-age=300' });
  fs.createReadStream(file).pipe(res);
}

async function listRepos() {
  let names = [];
  try { names = fs.readdirSync(CFG.codeDir, { withFileTypes: true }).filter((d) => d.isDirectory() && !d.name.startsWith('.')).map((d) => d.name); } catch { /* no ~/code */ }
  const out = await Promise.all(names.map((name) => new Promise((resolve) => {
    execFile('git', ['-C', path.join(CFG.codeDir, name), 'status', '--porcelain=v1', '--branch'], { timeout: 4000 }, (err, stdout) => {
      if (err) return resolve({ name, git: false });
      const lines = stdout.split('\n').filter(Boolean);
      const head = (lines[0] || '').replace(/^## /, '');
      resolve({ name, git: true, branch: head.split('...')[0], dirty: lines.length - 1, ahead: +(head.match(/ahead (\d+)/)?.[1] || 0), behind: +(head.match(/behind (\d+)/)?.[1] || 0) });
    });
  })));
  const mtime = (n) => { try { return fs.statSync(path.join(CFG.codeDir, n)).mtimeMs; } catch { return 0; } };
  return out.sort((a, b) => mtime(b.name) - mtime(a.name));
}

// ------------------------------------------------------------------ screen streaming
function screenStream(req, res, s, url) {
  res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store', connection: 'keep-alive', 'x-accel-buffering': 'no' });
  viewers.set(s.id, (viewers.get(s.id) || 0) + 1);
  if (s.unread) { s.unread = false; changed(); }
  const cols = Math.max(20, Math.min(400, +url.searchParams.get('cols') || 0));
  const rows = Math.max(10, Math.min(200, +url.searchParams.get('rows') || 0));
  let resized = false;
  (async () => {
    if (s.kind === 'tmux' && url.searchParams.get('cols') && (await tmux.clients(s.tmux)).length === 0) {
      try { await tmux.resize(s.tmux, cols, rows); resized = true; } catch { /* ignore */ }
    }
  })();
  let last = ''; let closed = false;
  const tick = async () => {
    if (closed) return;
    if (s.kind !== 'tmux' || !s.tmux || s.state === 'gone') {
      res.write(`data: ${JSON.stringify({ text: null, reason: s.kind === 'external' ? 'external' : 'gone' })}\n\n`);
    } else {
      try {
        const text = await tmux.capture(s.tmux, 300);
        if (text !== last) { last = text; res.write(`data: ${JSON.stringify({ text, cols: s.live?.width, rows: s.live?.height })}\n\n`); }
      } catch { /* session vanished mid-capture */ }
    }
    setTimeout(tick, 350);
  };
  tick();
  const ping = setInterval(() => res.write(': ping\n\n'), 20000);
  req.on('close', () => {
    closed = true; clearInterval(ping);
    const n = (viewers.get(s.id) || 1) - 1; viewers.set(s.id, n);
    if (n <= 0 && resized && s.tmux) tmux.restoreSize(s.tmux).catch(() => {});
  });
}

// ------------------------------------------------------------------ router
async function route(req, res, url) {
  const p = url.pathname; const m = req.method;
  const internal = !forwarded(req) && isLoopback(req);

  // ---- for the host gateway (and local tools): no cookie, never via the public proxy
  if (p.startsWith('/devbox/')) {
    if (forwarded(req)) return send(res, 404, { error: 'not found' });
    if (p === '/devbox/status' && m === 'GET') return send(res, 200, devboxStatus());
    if (p === '/devbox/wake' && m === 'POST') {
      const body = await readJsonBody(req);
      fixClock(body.now);
      lastBusy = now();
      const recent = sync.state === 'done' && now() - sync.finishedAt < 60000;
      const runId = recent ? sync.runId : startSync(`wake (${body.kind || '?'})`);
      maybeRefreshContext();
      setTimeout(reconcile, 200);
      return send(res, 200, { runId });
    }
    return send(res, 404, { error: 'not found' });
  }

  // ---- hooks + CLI inside the VM
  if (p.startsWith('/internal/')) {
    if (!internal) return send(res, 403, { error: 'forbidden' });
    if (p === '/internal/event' && m === 'POST') { await onEvent(await readJsonBody(req, 8e6)); return send(res, 200, { ok: true }); }
    return send(res, 404, { error: 'not found' });
  }

  // ---- auth
  if (p === '/api/login' && m === 'POST') {
    const ip = String(req.headers['cf-connecting-ip'] || req.headers['x-forwarded-for'] || req.socket.remoteAddress).split(',')[0];
    if (!config.data.passwordHash) return send(res, 503, { error: 'No password set yet. On the devbox run: agent passwd' });
    if (rateLimited(ip)) return send(res, 429, { error: 'Too many attempts, wait a minute.' });
    const { password } = await readJsonBody(req);
    if (!checkPassword(password)) return send(res, 401, { error: 'Wrong password.' });
    const exp = String(now() + 180 * 864e5);
    const secure = req.headers['x-forwarded-proto'] === 'https' ? ' Secure;' : '';
    res.setHeader('set-cookie', `hub_session=${exp}.${sign(exp)}; Path=/; Max-Age=${180 * 86400}; HttpOnly;${secure} SameSite=Lax`);
    return send(res, 200, { ok: true });
  }
  if (p === '/api/logout' && m === 'POST') { res.setHeader('set-cookie', 'hub_session=; Path=/; Max-Age=0'); return send(res, 200, { ok: true }); }

  if (p.startsWith('/api/')) {
    if (!internal && !loggedIn(req)) return send(res, 401, { error: 'login required', passwordSet: !!config.data.passwordHash });

    if (p === '/api/state' && m === 'GET') return send(res, 200, snapshot());
    if (p === '/api/stream' && m === 'GET') {
      res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store', connection: 'keep-alive', 'x-accel-buffering': 'no' });
      res.write(`data: ${JSON.stringify(snapshot())}\n\n`);
      streams.add(res);
      const ping = setInterval(() => res.write(': ping\n\n'), 20000);
      req.on('close', () => { streams.delete(res); clearInterval(ping); });
      return;
    }
    if (p === '/api/repos' && m === 'GET') return send(res, 200, { codeDir: CFG.codeDir, repos: await listRepos() });
    if (p === '/api/sessions' && m === 'POST') { const s = await createSession(await readJsonBody(req)); return send(res, 201, publicSession(s)); }

    const sm = p.match(/^\/api\/sessions\/([a-f0-9]+)(\/[a-z]+)?$/);
    if (sm) {
      const s = byId(sm[1]); if (!s) return send(res, 404, { error: 'no such session' });
      const action = sm[2] || '';
      if (action === '/screen' && m === 'GET') return screenStream(req, res, s, url);
      if (action === '' && m === 'DELETE') {
        if (s.kind === 'tmux' && s.state !== 'gone') await tmux.kill(s.tmux).catch(() => {});
        sessions.data.list = sessions.data.list.filter((x) => x !== s);
        store.appendEvent({ type: 'session.kill', sessionId: s.id });
        changed(); return send(res, 200, { ok: true });
      }
      if (s.kind !== 'tmux' || s.state === 'gone') return send(res, 409, { error: s.kind === 'external' ? 'This agent runs outside tmux; open it where it runs.' : 'Session is gone.' });
      if (action === '/input' && m === 'POST') {
        const b = await readJsonBody(req);
        if (Array.isArray(b.keys) && b.keys.length) await tmux.sendKeys(s.tmux, b.keys.map(String).slice(0, 50));
        if (typeof b.text === 'string' && b.text) {
          await tmux.sendText(s.tmux, b.text.slice(0, 20000));
          if (b.enter !== false) { await sleep(150); await tmux.sendKeys(s.tmux, ['Enter']); }
          if (adapters.ADAPTERS[s.agent]?.hooks && ['idle', 'needs_input'].includes(s.state) && b.enter !== false) setState(s, 'working', { turnStartedAt: now() });
        }
        if (s.state === 'needs_input') setState(s, 'working', { waitingFor: null });
        s.unread = false;
        return send(res, 200, { ok: true });
      }
      if (action === '/rename' && m === 'POST') {
        const { name } = await readJsonBody(req);
        if (!name || !String(name).trim()) return send(res, 400, { error: 'name required' });
        s.name = clip(name, 60); changed(); return send(res, 200, publicSession(s));
      }
      if (action === '/restart' && m === 'POST') {
        const a = adapters.ADAPTERS[s.agent];
        if (!a || s.state !== 'exited') return send(res, 409, { error: 'Only an exited agent can be restarted.' });
        const argv = a.argv({ resume: true });
        await tmux.sendText(s.tmux, argv.map((x) => `'${x.replace(/'/g, `'\\''`)}'`).join(' '));
        await tmux.sendKeys(s.tmux, ['Enter']);
        setState(s, 'starting', { createdAt: now() });
        return send(res, 200, { ok: true });
      }
      return send(res, 404, { error: 'not found' });
    }

    if (p === '/api/push/subscribe' && m === 'POST') {
      const { subscription, device } = await readJsonBody(req);
      if (!subscription?.endpoint) return send(res, 400, { error: 'subscription required' });
      subs.data.list = subs.data.list.filter((x) => x.subscription.endpoint !== subscription.endpoint);
      subs.data.list.push({ subscription, device: clip(device || 'device', 80), createdAt: now() });
      subs.save(); changed(); return send(res, 200, { ok: true, devices: subs.data.list.length });
    }
    if (p === '/api/push/unsubscribe' && m === 'POST') {
      const { endpoint } = await readJsonBody(req);
      subs.data.list = subs.data.list.filter((x) => x.subscription.endpoint !== endpoint);
      subs.save(); changed(); return send(res, 200, { ok: true });
    }
    if (p === '/api/push/test' && m === 'POST') {
      if (!push) return send(res, 500, { error: 'web-push not installed (npm ci in devbox/hub)' });
      await push.sendAll(subs, { title: '🔔 Devbox', body: 'Notifications are working.', url: '/#/', tag: 'test' });
      return send(res, 200, { ok: true, devices: subs.data.list.length });
    }
    if (p === '/api/settings' && m === 'POST') {
      const b = await readJsonBody(req);
      if (b.finishedNotifyAfterSec !== undefined) config.data.finishedNotifyAfterSec = Math.max(0, Math.min(3600, +b.finishedNotifyAfterSec || 0));
      config.flush(); return send(res, 200, { finishedNotifyAfterSec: config.data.finishedNotifyAfterSec });
    }
    if (p === '/api/settings' && m === 'GET') return send(res, 200, { finishedNotifyAfterSec: config.data.finishedNotifyAfterSec });
    if (p === '/api/sync' && m === 'POST') return send(res, 200, { runId: startSync('manual') });
    return send(res, 404, { error: 'not found' });
  }

  if (m === 'GET' || m === 'HEAD') return serveStatic(req, res, p);
  send(res, 404, { error: 'not found' });
}

const server = http.createServer((req, res) => {
  let url; try { url = new URL(req.url, 'http://hub'); } catch { return send(res, 400, { error: 'bad url' }); }
  route(req, res, url).catch((e) => { console.error(req.method, url.pathname, e.message); if (!res.headersSent) send(res, e.status || 500, { error: e.message }); else res.end(); });
});
server.listen(CFG.port, CFG.host, () => {
  console.log(`devbox hub on ${CFG.host}:${CFG.port} (code dir ${CFG.codeDir}); password ${config.data.passwordHash ? 'set' : 'NOT set — run: agent passwd'}`);
  reconcile();
  if (os.uptime() < 300) setTimeout(() => startSync('boot'), 1500);
});
for (const sig of ['SIGTERM', 'SIGINT']) process.on(sig, () => { sessions.flush(); subs.flush(); process.exit(0); });
