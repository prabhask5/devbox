#!/usr/bin/env node
'use strict';
// devbox-gateway — sits between Traefik and the devbox VM on the host (cestmoi).
//
//  * proxies code.prabhas.io -> VM:8080 (code-server) and agents.prabhas.io -> VM:8090 (agents hub),
//    including websockets, while the VM is up
//  * when the VM is asleep, serves a boot screen, wakes it through the Incus API, and shows live
//    progress with an ETA learned from previous wakes
//  * proxies SSH (TCP) and wakes the VM on connect
//  * puts the VM to sleep (stateful stop: RAM saved to disk, CPU + memory freed) after IDLE_MINUTES
//    with no activity here and no busy work reported by the hub inside the VM
//  * exposes /__devbox/health for Uptime Kuma (never wakes the VM; asleep = healthy)
//
// No dependencies: plain Node >= 20.

const http = require('node:http');
const net = require('node:net');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');

// `node server.js hash-password '<pw>'` prints a WAKE_PASSWORD_HASH value and exits.
if (process.argv[2] === 'hash-password') {
  const salt = crypto.randomBytes(16);
  // ':'-separated so it can sit in a docker compose .env file ('$' would be interpolated)
  console.log(`scrypt:${salt.toString('hex')}:${crypto.scryptSync(process.argv[3] || '', salt, 32).toString('hex')}`);
  process.exit(0);
}

const env = (k, d) => (process.env[k] === undefined || process.env[k] === '' ? d : process.env[k]);
const CFG = {
  listen: +env('LISTEN_PORT', 8080),
  sshListen: +env('SSH_LISTEN_PORT', 2222),
  incusSocket: env('INCUS_SOCKET', '/var/lib/incus/unix.socket'),
  instance: env('INSTANCE', 'devbox'),
  vmHost: env('VM_HOST', '10.77.0.10'),
  codePort: +env('CODE_PORT', 8080),
  hubPort: +env('HUB_PORT', 8090),
  sshPort: +env('VM_SSH_PORT', 22),
  codeHost: env('CODE_HOST', 'code.prabhas.io'),
  agentsHost: env('AGENTS_HOST', 'agents.prabhas.io'),
  idleMinutes: +env('IDLE_MINUTES', 15),
  minUpMinutes: +env('MIN_UP_MINUTES', 5),
  activityBytesPerMin: +env('ACTIVITY_BYTES_PER_MIN', 4096),
  stopMode: env('STOP_MODE', 'stateful'), // stateful | shutdown | off
  dataDir: env('DATA_DIR', '/data'),
  wakePassword: env('WAKE_PASSWORD', ''),
  wakePasswordHash: env('WAKE_PASSWORD_HASH', ''),
  cookieSecret: env('COOKIE_SECRET', ''),
  syncWaitSeconds: +env('SYNC_WAIT_SECONDS', 90),
  pollSeconds: +env('POLL_SECONDS', 15),
  tickMs: +env('TICK_MS', 1000),
};

// ---------------------------------------------------------------- utils
const now = () => Date.now();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
function log(...a) { console.log(new Date().toISOString(), ...a); }
fs.mkdirSync(CFG.dataDir, { recursive: true });
const readJson = (f, d) => { try { return JSON.parse(fs.readFileSync(f, 'utf8')); } catch { return d; } };
const writeJson = (f, v) => { const t = `${f}.tmp`; fs.writeFileSync(t, JSON.stringify(v, null, 1)); fs.renameSync(t, f); };
const median = (xs) => { if (!xs.length) return null; const s = [...xs].sort((a, b) => a - b); const m = s.length >> 1; return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2; };
const hostOf = (req) => String(req.headers['x-forwarded-host'] || req.headers.host || '').split(',')[0].trim().split(':')[0].toLowerCase();
const clientIp = (req) => String(req.headers['cf-connecting-ip'] || req.headers['x-forwarded-for'] || req.socket.remoteAddress || '').split(',')[0].trim();

const secretFile = path.join(CFG.dataDir, 'cookie-secret');
const COOKIE_SECRET = CFG.cookieSecret || (() => {
  try { return fs.readFileSync(secretFile, 'utf8').trim(); } catch {
    const s = crypto.randomBytes(32).toString('hex'); fs.writeFileSync(secretFile, s, { mode: 0o600 }); return s;
  }
})();

// ---------------------------------------------------------------- incus client (REST over unix socket)
function incus(method, p, body, timeoutMs = 30000) {
  return new Promise((resolve, reject) => {
    const data = body ? JSON.stringify(body) : null;
    const req = http.request({
      socketPath: CFG.incusSocket, method, path: p, timeout: timeoutMs,
      headers: data ? { 'content-type': 'application/json', 'content-length': Buffer.byteLength(data) } : {},
    }, (res) => {
      let buf = ''; res.setEncoding('utf8');
      res.on('data', (c) => (buf += c));
      res.on('end', () => {
        let j; try { j = JSON.parse(buf); } catch { return reject(new Error(`incus: bad response (${res.statusCode})`)); }
        if (j.type === 'error' || res.statusCode >= 400) return reject(new Error(`incus: ${j.error || res.statusCode}`));
        resolve(j);
      });
    });
    req.on('timeout', () => req.destroy(new Error('incus: timeout')));
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}
async function incusOp(method, p, body) {
  const r = await incus(method, p, body);
  if (r.type !== 'async') return r;
  const w = await incus('GET', `${r.operation}/wait?timeout=600`, null, 610000);
  const md = w.metadata || {};
  if (md.status_code >= 400 || md.err) throw new Error(`incus: ${md.err || md.status}`);
  return w;
}
const INST = `/1.0/instances/${encodeURIComponent(CFG.instance)}`;
async function vmInfo() {
  const [i, s] = await Promise.all([incus('GET', INST), incus('GET', `${INST}/state`)]);
  return { status: s.metadata.status, stateful: !!i.metadata.stateful };
}

// ---------------------------------------------------------------- probes into the VM
function httpGet(port, p, timeoutMs = 2000) {
  return new Promise((resolve) => {
    const req = http.request({ host: CFG.vmHost, port, path: p, method: 'GET', timeout: timeoutMs }, (res) => {
      let buf = ''; res.setEncoding('utf8');
      res.on('data', (c) => { if (buf.length < 1e6) buf += c; });
      res.on('end', () => resolve({ status: res.statusCode, body: buf }));
    });
    req.on('timeout', () => req.destroy());
    req.on('error', () => resolve(null));
    req.end();
  });
}
function httpPost(port, p, body, timeoutMs = 5000) {
  return new Promise((resolve) => {
    const data = JSON.stringify(body || {});
    const req = http.request({ host: CFG.vmHost, port, path: p, method: 'POST', timeout: timeoutMs,
      headers: { 'content-type': 'application/json', 'content-length': Buffer.byteLength(data) } }, (res) => {
      let buf = ''; res.setEncoding('utf8');
      res.on('data', (c) => (buf += c));
      res.on('end', () => { try { resolve(JSON.parse(buf)); } catch { resolve(null); } });
    });
    req.on('timeout', () => req.destroy());
    req.on('error', () => resolve(null));
    req.end(data);
  });
}
// sshd is ready when it sends its "SSH-2.0-..." banner, not merely when the port accepts.
function sshReady(timeoutMs = 2500) {
  return new Promise((resolve) => {
    const s = net.connect({ host: CFG.vmHost, port: CFG.sshPort, timeout: timeoutMs });
    const done = (v) => { s.destroy(); resolve(v); };
    s.once('data', (d) => done(String(d).startsWith('SSH-')));
    s.once('timeout', () => done(false));
    s.once('error', () => done(false));
    s.once('close', () => resolve(false));
  });
}
const codeHealthy = async () => (await httpGet(CFG.codePort, '/healthz'))?.status === 200;
async function hubStatus() {
  const r = await httpGet(CFG.hubPort, '/devbox/status');
  if (!r || r.status !== 200) return null;
  try { return JSON.parse(r.body); } catch { return null; }
}
async function waitFor(fn, timeoutMs, everyMs = 400) {
  const end = now() + timeoutMs;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (now() > end) throw new Error('timed out');
    await sleep(everyMs);
  }
}

// ---------------------------------------------------------------- state
const PHASES = [
  { id: 'start', label: 'Starting the VM' },
  { id: 'code', label: 'Waiting for code-server' },
  { id: 'hub', label: 'Waiting for the agents hub' },
  { id: 'sync', label: 'Syncing repos from GitHub' },
];
const DEFAULT_ETA = {
  restore: { start: 6000, code: 1000, hub: 1000, sync: 8000 },
  cold: { start: 4000, code: 6000, hub: 2000, sync: 10000 },
  attach: { start: 0, code: 3000, hub: 2000, sync: 0 },
};
const historyFile = path.join(CFG.dataDir, 'history.json');
const S = {
  vm: 'unknown', // running | stopped | error | unknown
  vmStatus: '',
  stateful: false,
  phase: 'idle', // idle | waking | stopping
  wake: null,
  lastActivity: now(),
  upSince: null,
  hub: null,
  hubSeenAt: 0,
  codeOkAt: 0,
  incusOkAt: 0,
  incusError: null,
  lastStop: readJson(path.join(CFG.dataDir, 'last-stop.json'), null),
  lastStopError: null,
  idle: null,
  history: readJson(historyFile, []),
  minuteBytes: 0,
  bytesLog: [], // last 60 minutes of client->server bytes, for calibrating ACTIVITY_BYTES_PER_MIN
  conns: new Set(),
};

function estimate(kind) {
  const same = S.history.filter((h) => h.kind === kind).slice(-10);
  const eta = {};
  for (const p of PHASES) {
    const m = median(same.map((h) => h.phases[p.id]).filter((x) => typeof x === 'number'));
    eta[p.id] = m ?? DEFAULT_ETA[kind]?.[p.id] ?? 3000;
  }
  return { phases: eta, samples: same.length };
}

function markActivity(why) { S.lastActivity = now(); if (why) S.lastActivityWhy = why; }
function countBytes(n) { S.minuteBytes += n; }
setInterval(() => {
  S.bytesLog.push({ at: now(), bytes: S.minuteBytes });
  if (S.bytesLog.length > 60) S.bytesLog.shift();
  if (S.minuteBytes >= CFG.activityBytesPerMin) markActivity('traffic');
  S.minuteBytes = 0;
}, 60000).unref();

// ---------------------------------------------------------------- SSE to boot pages
const sse = new Set();
function publicState() {
  const w = S.wake;
  return {
    gateway: 'ok',
    serverNow: now(),
    vm: S.vm,
    phase: S.phase,
    idle: S.idle,
    lastStop: S.lastStop,
    wake: w && {
      id: w.id, kind: w.kind, reason: w.reason, requestedAt: w.requestedAt, current: w.current,
      phases: w.phases, done: w.done, error: w.error, eta: w.eta, log: w.log.slice(-30), sync: w.sync,
    },
    phaseDefs: PHASES,
    typical: { restore: estimate('restore'), cold: estimate('cold') },
  };
}
let lastBroadcast = 0;
function broadcast() {
  lastBroadcast = now();
  const msg = `data: ${JSON.stringify(publicState())}\n\n`;
  for (const res of sse) res.write(msg);
}
setInterval(() => { if (S.phase !== 'idle' && now() - lastBroadcast > 900) broadcast(); }, 1000).unref();
setInterval(() => { for (const res of sse) res.write(': ping\n\n'); }, 20000).unref();

// ---------------------------------------------------------------- wake
let wakePromise = null;
function requestWake(reason) {
  if (S.phase === 'waking') return wakePromise;
  if (S.vm === 'running' && S.phase === 'idle' && S.codeOkAt) return Promise.resolve();
  if (!wakePromise) wakePromise = doWake(reason).finally(() => { wakePromise = null; });
  return wakePromise;
}

async function doWake(reason) {
  if (stopPromise) { try { await stopPromise; } catch { /* ignore */ } }
  const w = { id: crypto.randomUUID(), reason, requestedAt: now(), phases: {}, current: null, done: false, error: null, log: [], sync: null };
  const wlog = (m) => { w.log.push({ at: now(), m }); log(`[wake] ${m}`); broadcast(); };
  S.wake = w; S.phase = 'waking';
  markActivity('wake');
  const phase = async (id, fn) => {
    w.current = id; w.phases[id] = { start: now() }; broadcast();
    try {
      const r = await fn();
      w.phases[id].end = now();
      if (r === 'skipped') w.phases[id].skipped = true;
    } finally { broadcast(); }
  };
  try {
    let info;
    try { info = await vmInfo(); S.incusOkAt = now(); } catch (e) { S.incusError = e.message; throw new Error(`can't reach Incus: ${e.message}`); }
    w.kind = info.status === 'Running' ? 'attach' : info.stateful ? 'restore' : 'cold';
    w.eta = estimate(w.kind);
    wlog(w.kind === 'restore' ? 'Restoring the VM from its saved memory' : w.kind === 'cold' ? 'Booting the VM' : 'VM already running');

    await phase('start', async () => {
      if (info.status === 'Running') return 'skipped';
      try {
        await incusOp('PUT', `${INST}/state`, { action: 'start', timeout: -1 });
      } catch (e) {
        if (w.kind !== 'restore') throw e;
        // A broken saved state must never lock you out: fall back to a clean boot.
        wlog(`Restore failed (${e.message}); booting fresh instead`);
        w.kind = 'cold'; w.fellBack = true;
        await incusOp('PUT', `${INST}/state`, { action: 'start', stateful: false, timeout: -1 });
      }
      S.vm = 'running'; S.upSince = now();
    });
    wlog('VM is running');

    await phase('code', async () => {
      try { await waitFor(codeHealthy, 180000); } catch { throw new Error('code-server did not come up within 3 minutes'); }
      S.codeOkAt = now();
    });
    wlog('code-server is up');

    let hubUp = false;
    await phase('hub', async () => {
      try { await waitFor(hubStatus, 60000); hubUp = true; } catch { wlog('Agents hub not responding; continuing without it'); return 'skipped'; }
    });

    await phase('sync', async () => {
      if (!hubUp) return 'skipped';
      const r = await httpPost(CFG.hubPort, '/devbox/wake', { now: now(), kind: w.kind });
      if (!r || !r.runId) { wlog('Hub did not start a repo sync'); return 'skipped'; }
      wlog('Syncing repos');
      try {
        await waitFor(async () => {
          const st = await hubStatus();
          if (st) { S.hub = st; S.hubSeenAt = now(); w.sync = st.sync; }
          broadcast();
          return st && st.sync && st.sync.runId === r.runId && st.sync.state === 'done';
        }, CFG.syncWaitSeconds * 1000, 700);
      } catch { wlog('Repo sync is still running; opening anyway (it continues in the background)'); return 'skipped'; }
      const bad = (w.sync?.results || []).filter((x) => x.status === 'conflict' || x.status === 'error');
      wlog(bad.length ? `Repos synced (${bad.length} need attention)` : 'Repos synced');
    });

    w.done = true; w.current = null;
    const total = now() - w.requestedAt;
    wlog(`Ready in ${(total / 1000).toFixed(1)}s`);
    if (w.kind !== 'attach' && !w.fellBack) {
      const phases = {};
      for (const p of PHASES) { const x = w.phases[p.id]; if (x && x.end && !x.skipped) phases[p.id] = x.end - x.start; }
      S.history.push({ kind: w.kind, at: w.requestedAt, total, phases });
      S.history = S.history.slice(-100);
      writeJson(historyFile, S.history);
    }
  } catch (e) {
    w.error = e.message; w.current = null;
    wlog(`Wake failed: ${e.message}`);
  } finally {
    S.phase = 'idle';
    markActivity('wake');
    broadcast();
  }
}

// ---------------------------------------------------------------- sleep (idle stop)
let stopPromise = null;
function sleepVm(reason) {
  if (stopPromise) return stopPromise;
  stopPromise = (async () => {
    S.phase = 'stopping'; broadcast();
    log(`[sleep] stopping VM (${reason}, mode=${CFG.stopMode})`);
    const t0 = now();
    let mode = CFG.stopMode;
    try {
      if (mode === 'stateful') {
        try {
          await incusOp('PUT', `${INST}/state`, { action: 'stop', stateful: true, timeout: 120 });
        } catch (e) {
          log(`[sleep] stateful stop failed (${e.message}); falling back to a clean shutdown`);
          mode = 'shutdown (stateful failed)';
          await incusOp('PUT', `${INST}/state`, { action: 'stop', timeout: 120 });
        }
      } else {
        await incusOp('PUT', `${INST}/state`, { action: 'stop', timeout: 120 });
      }
      S.vm = 'stopped'; S.upSince = null; S.codeOkAt = 0; S.hub = null; S.lastStopError = null;
      S.lastStop = { at: now(), reason, mode, ms: now() - t0 };
      writeJson(path.join(CFG.dataDir, 'last-stop.json'), S.lastStop);
      for (const c of S.conns) c.destroy();
      if (S.wake?.done || S.wake?.error) S.wake = null;
      log(`[sleep] VM stopped in ${now() - t0}ms`);
    } catch (e) {
      S.lastStopError = { at: now(), error: e.message };
      log(`[sleep] stop failed: ${e.message}`);
    } finally {
      S.phase = 'idle'; broadcast();
    }
  })().finally(() => { stopPromise = null; });
  return stopPromise;
}

// ---------------------------------------------------------------- background poll: VM state, health, idle
let polling = false;
async function poll() {
  if (polling) return; polling = true;
  try {
    let info;
    try { info = await vmInfo(); S.incusOkAt = now(); S.incusError = null; } catch (e) { S.incusError = e.message; return; }
    S.vmStatus = info.status; S.stateful = info.stateful;
    const running = info.status === 'Running';
    if (running && S.vm !== 'running') { S.upSince = S.upSince || now(); markActivity('vm-started'); }
    S.vm = running ? 'running' : info.status === 'Stopped' ? 'stopped' : 'error';
    if (!running) { S.codeOkAt = 0; S.hub = null; S.idle = null; return; }
    S.upSince = S.upSince || now();
    if (S.phase !== 'idle') return;

    if (await codeHealthy()) S.codeOkAt = now();
    const hub = await hubStatus();
    if (hub) { S.hub = hub; S.hubSeenAt = now(); }

    const gwIdle = now() - S.lastActivity;
    const hubIdle = hub ? (hub.idle?.busy ? 0 : (hub.idle?.idleSeconds ?? 0) * 1000) : Infinity;
    const idleMs = Math.min(gwIdle, hubIdle);
    const upMs = now() - S.upSince;
    const limit = CFG.idleMinutes * 60000;
    S.idle = {
      idleMs, gatewayIdleMs: gwIdle, hubIdleMs: hub ? hubIdle : null, limitMs: limit,
      hubReasons: hub?.idle?.reasons || [], openConnections: S.conns.size, lastActivityWhy: S.lastActivityWhy,
    };
    if (CFG.stopMode !== 'off' && idleMs >= limit && upMs >= CFG.minUpMinutes * 60000) sleepVm(`idle ${Math.round(idleMs / 60000)}m`);
  } finally { polling = false; }
}
setInterval(poll, CFG.pollSeconds * 1000).unref();

function health() {
  const problems = [];
  const t = now();
  const pollGrace = CFG.pollSeconds * 4000;
  if (t - S.incusOkAt > pollGrace) problems.push(`Incus API unreachable${S.incusError ? `: ${S.incusError}` : ''}`);
  if (S.vm === 'error') problems.push(`VM is in state "${S.vmStatus}"`);
  if (S.wake?.error && t - S.wake.requestedAt < 30 * 60000) problems.push(`last wake failed: ${S.wake.error}`);
  if (S.lastStopError && t - S.lastStopError.at < 60 * 60000) problems.push(`last sleep failed: ${S.lastStopError.error}`);
  if (S.vm === 'running' && S.phase === 'idle' && S.upSince && t - S.upSince > 3 * 60000) {
    if (t - S.codeOkAt > pollGrace) problems.push('VM is up but code-server is not responding');
    if (t - S.hubSeenAt > pollGrace) problems.push('VM is up but the agents hub is not responding');
    if (CFG.stopMode !== 'off' && S.idle && S.idle.idleMs > (CFG.idleMinutes + 10) * 60000) problems.push('VM stayed awake long past the idle limit');
  }
  return { status: problems.length ? 'problem' : 'ok', problems, vm: S.vm, phase: S.phase, checkedAt: t };
}

// ---------------------------------------------------------------- auth for waking
function sign(v) { return crypto.createHmac('sha256', COOKIE_SECRET).update(v).digest('base64url'); }
function makeCookie() { const exp = String(now() + 365 * 864e5); return `${exp}.${sign(exp)}`; }
function cookies(req) {
  const out = {};
  for (const part of String(req.headers.cookie || '').split(';')) { const i = part.indexOf('='); if (i > 0) out[part.slice(0, i).trim()] = decodeURIComponent(part.slice(i + 1).trim()); }
  return out;
}
const authRequired = () => !!(CFG.wakePassword || CFG.wakePasswordHash);
function authed(req) {
  if (!authRequired()) return true;
  const c = cookies(req).devbox_wake; if (!c) return false;
  const [exp, sig] = c.split('.');
  if (!exp || !sig || +exp < now()) return false;
  const a = Buffer.from(sig), b = Buffer.from(sign(exp));
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}
function checkPassword(pw) {
  pw = String(pw || '');
  if (CFG.wakePasswordHash) {
    const [algo, salt, hash] = CFG.wakePasswordHash.split(/[:$]/);
    if (algo !== 'scrypt' || !salt || !hash) return false;
    const got = crypto.scryptSync(pw, Buffer.from(salt, 'hex'), 32);
    const want = Buffer.from(hash, 'hex');
    return got.length === want.length && crypto.timingSafeEqual(got, want);
  }
  const a = crypto.createHash('sha256').update(pw).digest(), b = crypto.createHash('sha256').update(CFG.wakePassword).digest();
  return crypto.timingSafeEqual(a, b);
}
const attempts = new Map();
function rateLimited(ip) {
  const t = now(); const xs = (attempts.get(ip) || []).filter((x) => t - x < 60000);
  xs.push(t); attempts.set(ip, xs);
  return xs.length > 6;
}
const setCookie = (res, name, val, maxAge) => res.setHeader('set-cookie', `${name}=${encodeURIComponent(val)}; Path=/; Max-Age=${maxAge}; HttpOnly; Secure; SameSite=Lax`);

// ---------------------------------------------------------------- HTTP
const bootHtml = fs.readFileSync(path.join(__dirname, 'boot.html'), 'utf8');
function json(res, code, v) { res.writeHead(code, { 'content-type': 'application/json', 'cache-control': 'no-store' }); res.end(JSON.stringify(v)); }
function readBody(req, max = 1e5) {
  return new Promise((resolve) => {
    let b = ''; req.setEncoding('utf8');
    req.on('data', (c) => { b += c; if (b.length > max) req.destroy(); });
    req.on('end', () => resolve(b)); req.on('error', () => resolve(''));
  });
}
function serveBoot(req, res, extra = {}) {
  const host = hostOf(req);
  const boot = { host, app: host === CFG.agentsHost ? 'agents' : 'code', needsAuth: !authed(req), ...extra };
  res.writeHead(extra.status || 200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
  res.end(bootHtml.replace('/*__BOOT__*/null', JSON.stringify(boot).replace(/</g, '\\u003c')));
}
function safeNext(v) { v = String(v || '/'); return v.startsWith('/') && !v.startsWith('//') ? v : '/'; }

async function handleDevbox(req, res, url) {
  const p = url.pathname;
  if (p === '/__devbox/health') { const h = health(); return json(res, h.status === 'ok' ? 200 : 503, h); }
  if (p === '/__devbox/status') return json(res, 200, { ...publicState(), authed: authed(req), health: health() });
  if (p === '/__devbox/events') {
    res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store', connection: 'keep-alive', 'x-accel-buffering': 'no' });
    res.write(`data: ${JSON.stringify(publicState())}\n\n`);
    sse.add(res); req.on('close', () => sse.delete(res));
    return;
  }
  if (p === '/__devbox/login' && req.method === 'POST') {
    const body = await readBody(req);
    const isJson = String(req.headers['content-type'] || '').includes('json');
    let pw, next;
    if (isJson) { try { ({ password: pw, next } = JSON.parse(body)); } catch { /* empty */ } } else { const f = new URLSearchParams(body); pw = f.get('password'); next = f.get('next'); }
    if (rateLimited(clientIp(req))) return isJson ? json(res, 429, { error: 'too many attempts' }) : serveBoot(req, res, { error: 'Too many attempts, wait a minute.', status: 429 });
    if (!checkPassword(pw)) return isJson ? json(res, 401, { error: 'wrong password' }) : serveBoot(req, res, { error: 'Wrong password.', status: 401 });
    setCookie(res, 'devbox_wake', makeCookie(), 365 * 86400);
    requestWake('login');
    if (isJson) return json(res, 202, { ok: true });
    res.writeHead(303, { location: safeNext(next) }); return res.end();
  }
  if (p === '/__devbox/wake' && req.method === 'POST') {
    if (!authed(req)) return json(res, 401, { error: 'login required' });
    requestWake('app');
    return json(res, 202, { ok: true });
  }
  if (p === '/__devbox/sleep' && req.method === 'POST') {
    if (!authed(req)) return json(res, 401, { error: 'login required' });
    if (S.vm !== 'running' || S.phase !== 'idle') return json(res, 409, { error: `VM is ${S.vm}/${S.phase}` });
    sleepVm('requested');
    return json(res, 202, { ok: true });
  }
  if (p === '/__devbox/skip' && req.method === 'POST') {
    if (S.wake) setCookie(res, 'devbox_skip', S.wake.id, 3600);
    const f = new URLSearchParams(await readBody(req));
    res.writeHead(303, { location: safeNext(f.get('next')) }); return res.end();
  }
  if (p === '/__devbox/history') {
    if (!authed(req)) return json(res, 401, { error: 'login required' });
    return json(res, 200, { history: S.history.slice(-30), bytesPerMinute: S.bytesLog, threshold: CFG.activityBytesPerMin });
  }
  json(res, 404, { error: 'not found' });
}

const targetPort = (req) => (hostOf(req) === CFG.agentsHost ? CFG.hubPort : CFG.codePort);
const IGNORE_ACTIVITY = /^\/(healthz|devbox\/|api\/stream|api\/sessions\/[^/]+\/screen|sw\.js|manifest)/;

// Is the target behind this request ready to be proxied?
function upstreamReady(req) {
  if (S.vm !== 'running' || S.phase === 'stopping') return false;
  if (S.phase === 'waking') {
    const w = S.wake; if (!w) return false;
    const need = targetPort(req) === CFG.hubPort ? 'hub' : 'code';
    return !!(w.phases[need]?.end);
  }
  return true;
}
// For page loads during a wake we keep showing the boot screen until everything (incl. sync) is done.
function showBootForPage(req) {
  if (!upstreamReady(req)) return true;
  if (S.phase === 'waking' && S.wake) return cookies(req).devbox_skip !== S.wake.id;
  return false;
}

function proxyHttp(req, res) {
  const url = req.url;
  const pathOnly = url.split('?')[0];
  if (!IGNORE_ACTIVITY.test(pathOnly.slice(0))) markActivity('http');
  const headers = { ...req.headers };
  headers['x-forwarded-host'] = req.headers['x-forwarded-host'] || req.headers.host || '';
  headers['x-forwarded-proto'] = req.headers['x-forwarded-proto'] || 'https';
  headers['x-forwarded-for'] = req.headers['x-forwarded-for'] || req.socket.remoteAddress || '';
  const up = http.request({ host: CFG.vmHost, port: targetPort(req), method: req.method, path: url, headers });
  up.on('response', (ur) => {
    // rebuild headers from rawHeaders so duplicates (set-cookie) and casing survive
    const h = {};
    for (let i = 0; i < ur.rawHeaders.length; i += 2) {
      const k = ur.rawHeaders[i];
      if (h[k] === undefined) h[k] = ur.rawHeaders[i + 1];
      else h[k] = [].concat(h[k], ur.rawHeaders[i + 1]);
    }
    res.writeHead(ur.statusCode, h);
    ur.pipe(res);
    ur.on('error', () => res.destroy());
  });
  up.on('error', () => {
    if (res.headersSent) return res.destroy();
    S.codeOkAt = 0;
    if (req.method === 'GET' && String(req.headers.accept || '').includes('text/html')) return serveBoot(req, res, { upstreamDown: true, status: 502 });
    json(res, 502, { error: 'devbox upstream not responding', devbox: S.vm });
  });
  req.on('data', (c) => countBytes(c.length));
  req.pipe(up);
  res.on('close', () => up.destroy());
}

const server = http.createServer(async (req, res) => {
  let url;
  try { url = new URL(req.url, 'http://gateway'); } catch { return json(res, 400, { error: 'bad url' }); }
  if (url.pathname.startsWith('/__devbox/')) return handleDevbox(req, res, url).catch((e) => json(res, 500, { error: e.message }));
  const isPage = req.method === 'GET' && String(req.headers.accept || '').includes('text/html');
  if (isPage && showBootForPage(req)) {
    if (authed(req)) requestWake('visit'); // no-op when already awake or already waking
    return serveBoot(req, res);
  }
  if (!upstreamReady(req)) {
    if (authed(req) && S.phase === 'idle' && S.vm !== 'running') requestWake('request');
    return json(res, 503, { devbox: S.vm === 'running' ? 'waking' : 'asleep', phase: S.phase, authed: authed(req), wake: publicState().wake });
  }
  proxyHttp(req, res);
});

server.on('upgrade', (req, socket, head) => {
  socket.on('error', () => socket.destroy());
  if (!upstreamReady(req)) { socket.end('HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\n\r\n'); return; }
  markActivity('websocket');
  const up = net.connect(targetPort(req), CFG.vmHost);
  S.conns.add(socket);
  const cleanup = () => { S.conns.delete(socket); socket.destroy(); up.destroy(); };
  up.on('connect', () => {
    let raw = `${req.method} ${req.url} HTTP/1.1\r\n`;
    for (let i = 0; i < req.rawHeaders.length; i += 2) raw += `${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}\r\n`;
    if (!req.headers['x-forwarded-host']) raw += `X-Forwarded-Host: ${req.headers.host || ''}\r\n`;
    if (!req.headers['x-forwarded-proto']) raw += 'X-Forwarded-Proto: https\r\n';
    up.write(`${raw}\r\n`);
    if (head && head.length) up.write(head);
    socket.on('data', (d) => countBytes(d.length));
    socket.pipe(up); up.pipe(socket);
  });
  up.on('error', cleanup); up.on('close', cleanup); socket.on('close', cleanup);
});

// ---------------------------------------------------------------- SSH (TCP) proxy with wake-on-connect
const sshServer = net.createServer(async (client) => {
  client.pause();
  client.on('error', () => client.destroy());
  markActivity('ssh');
  try {
    if (S.vm !== 'running' || S.phase !== 'idle') requestWake('ssh');
    await waitFor(async () => S.vm === 'running' && S.phase !== 'stopping' && (await sshReady()), 180000, 500);
  } catch { client.destroy(); return; }
  const up = net.connect(CFG.sshPort, CFG.vmHost);
  S.conns.add(client);
  const cleanup = () => { S.conns.delete(client); client.destroy(); up.destroy(); };
  up.on('connect', () => { client.on('data', (d) => countBytes(d.length)); client.pipe(up); up.pipe(client); client.resume(); });
  up.on('error', cleanup); up.on('close', cleanup); client.on('close', cleanup);
});

server.listen(CFG.listen, () => log(`devbox-gateway: http :${CFG.listen} -> ${CFG.vmHost} (code :${CFG.codePort}, hub :${CFG.hubPort}); instance=${CFG.instance}; idle=${CFG.idleMinutes}m; stop=${CFG.stopMode}; wake auth=${authRequired() ? 'on' : 'OFF'}`));
if (CFG.sshListen) sshServer.listen(CFG.sshListen, () => log(`devbox-gateway: ssh :${CFG.sshListen} -> ${CFG.vmHost}:${CFG.sshPort}`));
poll();
for (const sig of ['SIGTERM', 'SIGINT']) process.on(sig, () => { server.close(); sshServer.close(); process.exit(0); });
