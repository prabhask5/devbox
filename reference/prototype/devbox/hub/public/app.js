'use strict';
// Devbox Agents — mobile PWA. No framework, no build step.
const $app = document.getElementById('app');
const S = { snap: null, mode: 'loading', gw: null, repos: null, stream: null, session: null, toastT: null };
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const LS = { get: (k, d) => { try { return JSON.parse(localStorage.getItem(k)) ?? d; } catch { return d; } }, set: (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* private mode */ } } };

async function api(path, { method = 'GET', body } = {}) {
  const r = await fetch(path, { method, credentials: 'same-origin', headers: body ? { 'content-type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw Object.assign(new Error(data.error || `HTTP ${r.status}`), { status: r.status, data });
  return data;
}
function toast(msg) {
  document.querySelector('.toast')?.remove();
  const t = document.createElement('div'); t.className = 'toast'; t.textContent = msg; document.body.appendChild(t);
  clearTimeout(S.toastT); S.toastT = setTimeout(() => t.remove(), 2600);
}
const ago = (t) => {
  if (!t) return ''; const s = Math.max(0, (Date.now() - t) / 1000);
  return s < 45 ? 'now' : s < 3600 ? `${Math.round(s / 60)}m` : s < 86400 ? `${Math.round(s / 3600)}h` : `${Math.round(s / 86400)}d`;
};
const dur = (sec) => (sec < 60 ? `${Math.round(sec)}s` : sec < 3600 ? `${Math.round(sec / 60)}m` : `${(sec / 3600).toFixed(1)}h`);
const ICON = {
  back: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 5l-7 7 7 7"/></svg>',
  more: '<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="2"/><circle cx="12" cy="12" r="2"/><circle cx="19" cy="12" r="2"/></svg>',
  send: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5 12l7-7 7 7"/></svg>',
  plus: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>',
  list: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="3" y="4" width="18" height="16" rx="3"/><path d="M7 9l3 3-3 3M12 15h5"/></svg>',
  spark: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/><path d="M19 16l.8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8z"/></svg>',
  gear: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 00.3 1.8l.1.1a2 2 0 11-2.8 2.8l-.1-.1a1.7 1.7 0 00-1.8-.3 1.7 1.7 0 00-1 1.5V21a2 2 0 11-4 0v-.1a1.7 1.7 0 00-1.1-1.5 1.7 1.7 0 00-1.8.3l-.1.1a2 2 0 11-2.8-2.8l.1-.1a1.7 1.7 0 00.3-1.8 1.7 1.7 0 00-1.5-1H3a2 2 0 110-4h.1a1.7 1.7 0 001.5-1.1 1.7 1.7 0 00-.3-1.8l-.1-.1a2 2 0 112.8-2.8l.1.1a1.7 1.7 0 001.8.3H9a1.7 1.7 0 001-1.5V3a2 2 0 114 0v.1a1.7 1.7 0 001 1.5 1.7 1.7 0 001.8-.3l.1-.1a2 2 0 112.8 2.8l-.1.1a1.7 1.7 0 00-.3 1.8V9a1.7 1.7 0 001.5 1H21a2 2 0 110 4h-.1a1.7 1.7 0 00-1.5 1z"/></svg>',
};
const LETTER = { claude: 'C', codex: 'X', opencode: 'O', shell: '$' };
const STATE_LABEL = { working: 'Working', needs_input: 'Needs you', idle: 'Idle', starting: 'Starting', exited: 'Exited', gone: 'Gone', ended: 'Ended', error: 'Error' };

// ------------------------------------------------------------------ ANSI -> HTML (tmux capture-pane -e output)
const PAL = ['#2b2f36', '#ff6b6b', '#5fd38d', '#f2c14e', '#6cb6ff', '#c58cff', '#5fd3d3', '#c9ced6', '#6b7380', '#ff8787', '#7ee2a8', '#ffd479', '#8cc8ff', '#d8a8ff', '#7fe6e6', '#f2f4f7'];
function c256(n) {
  if (n < 16) return PAL[n];
  if (n < 232) { n -= 16; const v = (x) => (x ? x * 40 + 55 : 0); return `rgb(${v(Math.floor(n / 36))},${v(Math.floor(n / 6) % 6)},${v(n % 6)})`; }
  const l = 8 + (n - 232) * 10; return `rgb(${l},${l},${l})`;
}
function ansiToHtml(text) {
  text = text.replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '').replace(/\x1b[()][A-Za-z0-9]/g, '').replace(/\x1b[=>]/g, '');
  const lines = text.split('\n');
  while (lines.length && !lines[lines.length - 1].replace(/\x1b\[[0-9;:]*m/g, '').trim()) lines.pop();
  text = lines.join('\n');
  let out = ''; let st = {}; let open = false; let last = 0; let m;
  const re = /\x1b\[([0-9;:]*)([A-Za-z])/g;
  const css = () => {
    let fg = st.fg, bg = st.bg; if (st.inv) { [fg, bg] = [bg || '#090b0d', fg || '#d7dae0']; }
    const c = []; if (fg) c.push(`color:${fg}`); if (bg) c.push(`background:${bg}`); if (st.b) c.push('font-weight:700'); if (st.d) c.push('opacity:.55');
    if (st.i) c.push('font-style:italic'); if (st.u || st.s) c.push(`text-decoration:${st.u ? 'underline' : ''} ${st.s ? 'line-through' : ''}`); return c.join(';');
  };
  while ((m = re.exec(text))) {
    out += esc(text.slice(last, m.index)); last = re.lastIndex;
    if (m[2] !== 'm') continue;
    const p = m[1] === '' ? [0] : m[1].split(/[;:]/).map((x) => +x || 0);
    for (let i = 0; i < p.length; i++) {
      const n = p[i];
      if (n === 0) st = {}; else if (n === 1) st.b = 1; else if (n === 2) st.d = 1; else if (n === 3) st.i = 1; else if (n === 4) st.u = 1; else if (n === 7) st.inv = 1; else if (n === 9) st.s = 1;
      else if (n === 22) st.b = st.d = 0; else if (n === 23) st.i = 0; else if (n === 24) st.u = 0; else if (n === 27) st.inv = 0; else if (n === 29) st.s = 0;
      else if (n >= 30 && n <= 37) st.fg = PAL[n - 30]; else if (n === 39) st.fg = null; else if (n >= 40 && n <= 47) st.bg = PAL[n - 40]; else if (n === 49) st.bg = null;
      else if (n >= 90 && n <= 97) st.fg = PAL[n - 82]; else if (n >= 100 && n <= 107) st.bg = PAL[n - 92];
      else if (n === 38 || n === 48) {
        let col = null;
        if (p[i + 1] === 5) { col = c256(p[i + 2] || 0); i += 2; } else if (p[i + 1] === 2) { col = `rgb(${p[i + 2] || 0},${p[i + 3] || 0},${p[i + 4] || 0})`; i += 4; }
        if (n === 38) st.fg = col; else st.bg = col;
      }
    }
    if (open) { out += '</span>'; open = false; }
    const c = css(); if (c) { out += `<span style="${c}">`; open = true; }
  }
  out += esc(text.slice(last)); if (open) out += '</span>';
  return out;
}

// ------------------------------------------------------------------ boot / connection
async function start() {
  if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
  navigator.serviceWorker?.addEventListener('message', (e) => { if (e.data?.navigate) { const u = new URL(e.data.navigate); location.hash = u.hash || '#/'; } });
  window.addEventListener('hashchange', render);
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') connect(); });
  setInterval(() => { if (S.mode === 'app' && !S.session) renderMain(); }, 30000);
  setInterval(pollGateway, 20000); pollGateway();
  connect();
}
async function pollGateway() {
  if (document.visibilityState !== 'visible') return;
  try { const r = await fetch('/__devbox/status', { cache: 'no-store' }); if (r.ok) { S.gw = await r.json(); if (S.mode === 'app' && !S.session) renderTopPill(); } } catch { /* not behind the gateway */ }
}
async function connect() {
  try {
    const snap = await api('/api/state');
    S.snap = snap; S.mode = 'app';
    openStream();
  } catch (e) {
    S.stream?.close(); S.stream = null;
    if (e.status === 401) S.mode = 'login';
    else if (e.status === 503 || e.status === 502) { S.mode = 'asleep'; S.gwInfo = e.data; }
    else S.mode = 'offline';
  }
  render();
}
function openStream() {
  if (S.stream && S.stream.readyState !== 2) return;
  const es = new EventSource('/api/stream');
  S.stream = es;
  es.onmessage = (e) => {
    S.snap = JSON.parse(e.data);
    updateBadge();
    if (S.session) S.session.update(); else renderMain();
  };
  es.onerror = () => { es.close(); if (S.stream === es) S.stream = null; setTimeout(() => { if (document.visibilityState === 'visible') connect(); }, 2500); };
}
function updateBadge() {
  const n = (S.snap?.sessions || []).filter((s) => s.state === 'needs_input' || s.unread).length;
  try { if (n) navigator.setAppBadge?.(n); else navigator.clearAppBadge?.(); } catch { /* unsupported */ }
}

// ------------------------------------------------------------------ routing
function route() {
  const h = location.hash.replace(/^#/, '') || '/';
  const m = h.match(/^\/s\/([a-f0-9]+)/);
  if (m) return { name: 'session', id: m[1] };
  if (h.startsWith('/assistant')) return { name: 'assistant' };
  if (h.startsWith('/settings')) return { name: 'settings' };
  return { name: 'sessions' };
}
function render() {
  if (S.mode === 'loading') { $app.innerHTML = '<div class="center"><p>Connecting…</p></div>'; return; }
  if (S.mode === 'login') return renderLogin();
  if (S.mode === 'asleep') return renderAsleep();
  if (S.mode === 'offline') { $app.innerHTML = `<div class="center"><h2>Can't reach the devbox</h2><p>Check your connection.</p><button class="btn" id="retry">Try again</button></div>`; $app.querySelector('#retry').onclick = connect; return; }
  const r = route();
  if (r.name === 'session') {
    if (!S.session || S.session.id !== r.id) { S.session?.close(); S.session = new SessionView(r.id); }
    return;
  }
  if (S.session) { S.session.close(); S.session = null; }
  renderMain(true);
}

// ------------------------------------------------------------------ login / asleep
function renderLogin() {
  $app.innerHTML = `<div class="center">
    <h2>Agents</h2><p>Sign in to your devbox.</p>
    <form id="f"><input class="field" type="password" name="pw" placeholder="Password" autocomplete="current-password" required>
    <div style="height:10px"></div><button class="btn block">Sign in</button><div class="err" id="err"></div></form></div>`;
  const f = $app.querySelector('#f');
  f.onsubmit = async (e) => {
    e.preventDefault();
    try { await api('/api/login', { method: 'POST', body: { password: f.pw.value } }); connect(); } catch (err) { $app.querySelector('#err').textContent = err.message; }
  };
  setTimeout(() => f.pw.focus(), 50);
}
function renderAsleep() {
  const info = S.gwInfo || {};
  const needsAuth = info.authed === false;
  $app.innerHTML = `<div class="center">
    <h2 id="t">${info.devbox === 'waking' ? 'Waking up…' : 'Devbox is asleep'}</h2>
    <p id="p">${needsAuth ? 'Enter your wake password to start it.' : 'It went to sleep to free up the server. Your sessions are saved.'}</p>
    ${needsAuth ? '<form id="f"><input class="field" type="password" name="pw" placeholder="Wake password" required><div style="height:10px"></div><button class="btn block">Wake devbox</button></form>' : '<button class="btn block" id="wake">Wake devbox</button>'}
    <div id="prog" class="hidden"><div class="bar"><i id="fill"></i></div><div class="row" style="justify-content:space-between;color:var(--dim);font-size:13px"><span id="ph"></span><span id="eta"></span></div></div>
    <div class="err" id="err"></div></div>`;
  const begin = () => { $app.querySelector('#prog').classList.remove('hidden'); $app.querySelector('#f, #wake')?.classList.add('hidden'); followWake(); };
  const f = $app.querySelector('#f');
  if (f) f.onsubmit = async (e) => { e.preventDefault(); try { await api('/__devbox/login', { method: 'POST', body: { password: f.pw.value } }); begin(); } catch (err) { $app.querySelector('#err').textContent = err.message; } };
  const b = $app.querySelector('#wake');
  if (b) b.onclick = async () => { try { await api('/__devbox/wake', { method: 'POST' }); begin(); } catch (err) { $app.querySelector('#err').textContent = err.message; } };
  if (info.devbox === 'waking') begin();
}
function followWake() {
  const es = new EventSource('/__devbox/events');
  es.onmessage = (e) => {
    const st = JSON.parse(e.data); const w = st.wake; if (!w) return;
    const off = st.serverNow - Date.now(); const t = Date.now() + off;
    const defs = st.phaseDefs || []; let remaining = 0;
    for (const d of defs) { const p = w.phases[d.id]; const eta = w.eta?.phases[d.id] || 0; if (p && (p.end || p.skipped)) continue; remaining += p ? Math.max(0, eta - (t - p.start)) : eta; }
    const el = t - w.requestedAt;
    const q = (id) => $app.querySelector(id);
    if (!q('#fill')) { es.close(); return; }
    q('#fill').style.width = `${w.done ? 100 : Math.min(97, (100 * el) / Math.max(1, el + remaining))}%`;
    q('#ph').textContent = w.error ? '' : (defs.find((d) => d.id === w.current)?.label || (w.done ? 'Ready' : 'Starting…'));
    q('#eta').textContent = w.done ? '' : remaining > 500 ? `~${Math.ceil(remaining / 1000)}s left` : '';
    q('#t').textContent = w.error ? "Couldn't wake the devbox" : w.done ? 'Ready' : 'Waking up…';
    if (w.error) { q('#err').textContent = w.error; es.close(); }
    if (w.done) { es.close(); setTimeout(() => location.reload(), 400); }
  };
  es.onerror = () => { es.close(); setTimeout(followWake, 2000); };
}

// ------------------------------------------------------------------ main (tabs)
function renderMain(force) {
  const r = route();
  if (!force && document.activeElement && $app.contains(document.activeElement) && /INPUT|TEXTAREA/.test(document.activeElement.tagName)) return;
  const title = { sessions: 'Agents', assistant: 'Assistant', settings: 'Settings' }[r.name];
  let body = '';
  if (r.name === 'sessions') body = sessionsHtml();
  if (r.name === 'assistant') body = assistantHtml();
  if (r.name === 'settings') body = settingsHtml();
  const scrollY = $app.querySelector('.scroll')?.scrollTop || 0;
  $app.innerHTML = `<div class="top"><h1>${title}</h1><span id="pill"></span></div>
    <div class="scroll">${body}</div>
    ${r.name === 'sessions' ? `<button class="fab" id="new" aria-label="New session">${ICON.plus}</button>` : ''}
    <nav class="tabs">
      <button data-go="#/" class="${r.name === 'sessions' ? 'on' : ''}">${ICON.list}Sessions</button>
      <button data-go="#/assistant" class="${r.name === 'assistant' ? 'on' : ''}">${ICON.spark}Assistant</button>
      <button data-go="#/settings" class="${r.name === 'settings' ? 'on' : ''}">${ICON.gear}Settings</button>
    </nav>`;
  if (!force) $app.querySelector('.scroll').scrollTop = scrollY;
  $app.querySelectorAll('[data-go]').forEach((b) => { b.onclick = () => { location.hash = b.dataset.go; }; });
  $app.querySelectorAll('[data-open]').forEach((b) => { b.onclick = () => { location.hash = `#/s/${b.dataset.open}`; }; });
  $app.querySelector('#new')?.addEventListener('click', openNewSheet);
  if (r.name === 'settings') bindSettings();
  renderTopPill();
}
function renderTopPill() {
  const el = $app.querySelector('#pill'); if (!el || !S.snap) return;
  const d = S.snap.devbox; const g = S.gw;
  let dot = 'ok', text = 'Awake';
  if (d.sync?.state === 'running') { dot = 'accent pulse'; text = `Syncing ${d.sync.done}/${d.sync.total || '…'}`; }
  else if (d.idle?.busy) { dot = 'warn pulse'; text = 'Busy'; }
  else if (g?.idle?.limitMs) { const left = Math.max(0, g.idle.limitMs - g.idle.idleMs); text = left < 60000 ? 'Sleeping soon' : `Sleeps in ${Math.ceil(left / 60000)}m`; }
  else if (d.idle) text = `Idle ${dur(d.idle.idleSeconds)}`;
  el.innerHTML = `<span class="pill"><span class="dot ${dot}"></span>${esc(text)}</span>`;
}

function sessionSub(s) {
  if (s.state === 'needs_input') return s.waitingFor || 'Waiting for you';
  if (s.state === 'error') return s.waitingFor || 'Error';
  if (s.state === 'working') return s.lastTool ? `⚙︎ ${s.lastTool}` : s.lastPrompt ? `“${s.lastPrompt}”` : 'Working…';
  if (s.state === 'starting') return 'Starting…';
  if (s.state === 'exited') return 'Agent exited — open to restart';
  if (s.state === 'gone') return 'tmux session ended';
  if (s.state === 'ended') return 'Session ended';
  return s.lastReply || s.title || (s.agent === 'shell' ? 'Shell' : 'Ready for a prompt');
}
function sessionItem(s) {
  const spin = s.state === 'working' || s.state === 'starting' ? '<span class="spin"></span>' : '';
  return `<button class="item ${s.state === 'needs_input' ? 'needs' : ''}" data-open="${s.id}">
    <span class="badge ${esc(s.agent)}">${LETTER[s.agent] || '?'}${spin}</span>
    <span class="main"><span class="row1"><span class="name">${esc(s.name)}</span>${s.unread ? '<span class="unread"></span>' : ''}<span class="when">${ago(s.stateSince)}</span></span>
    <span class="sub">${esc(sessionSub(s))}</span>
    <span class="meta"><span class="chip ${s.state}">${STATE_LABEL[s.state] || s.state}</span>${s.repo ? `<span class="chip">${esc(s.repo)}</span>` : ''}${s.external ? '<span class="chip">outside tmux</span>' : ''}${s.attached ? '<span class="chip">attached</span>' : ''}</span></span>
  </button>`;
}
function sessionsHtml() {
  const list = S.snap?.sessions || [];
  if (!list.length) return `<div class="empty"><b>No sessions yet</b>Start Claude Code, Codex or opencode in one of your repos with the + button. Sessions keep running when you close the app.</div>`;
  const groups = [
    ['Needs you', list.filter((s) => s.state === 'needs_input' || s.state === 'error')],
    ['Working', list.filter((s) => s.state === 'working' || s.state === 'starting')],
    ['Idle', list.filter((s) => s.state === 'idle')],
    ['Finished', list.filter((s) => ['exited', 'gone', 'ended'].includes(s.state))],
  ];
  return groups.filter(([, xs]) => xs.length).map(([name, xs]) => `<div class="section"><span>${name}</span><span>${xs.length}</span></div><div class="list">${xs.map(sessionItem).join('')}</div>`).join('');
}
function assistantHtml() {
  return `<div class="soon"><div class="orb"></div><h2>Your assistant lives here</h2>
    <p>This tab is reserved for a personal assistant running on your own model — a chat you control fully from your phone, alongside your coding agents.</p>
    <p style="color:var(--faint);font-size:13px">The hub already supports “chat” agents next to terminal ones; plugging a local model in is a new adapter, not a new app.</p></div>`;
}

// ------------------------------------------------------------------ new session sheet
async function openNewSheet() {
  const adapters = Object.entries(S.snap.adapters).filter(([, a]) => a.kind === 'tmux');
  let agent = LS.get('lastAgent', 'claude'); if (!S.snap.adapters[agent]?.available) agent = adapters.find(([, a]) => a.available)?.[0] || 'shell';
  let repo = null; let filter = '';
  const bg = document.createElement('div'); bg.className = 'sheet-bg';
  const sh = document.createElement('div'); sh.className = 'sheet';
  const close = () => { bg.remove(); sh.remove(); };
  bg.onclick = close;
  document.body.append(bg, sh);
  const draw = () => {
    const repos = (S.repos || []).filter((r) => !filter || r.name.toLowerCase().includes(filter.toLowerCase()));
    sh.innerHTML = `<div class="grab"></div><h2>New session</h2>
      <span class="label">Agent</span>
      <div class="seg">${adapters.map(([k, a]) => `<button data-agent="${k}" class="${k === agent ? 'on' : ''}" ${a.available ? '' : 'disabled'}>${esc(a.short || a.label)}</button>`).join('')}</div>
      <span class="label">Where</span>
      ${(S.repos || []).length > 7 ? `<input class="field" id="filter" placeholder="Filter repos" value="${esc(filter)}" style="margin-bottom:8px">` : ''}
      <div class="repos">${S.repos === null ? '<div class="repo">Loading…</div>' : ''}
        ${repos.map((r) => `<button class="repo ${repo === r.name ? 'on' : ''}" data-repo="${esc(r.name)}"><span>${esc(r.name)}</span><small>${r.git ? `${esc(r.branch)}${r.dirty ? ` · ${r.dirty}✎` : ''}${r.ahead ? ` · ↑${r.ahead}` : ''}` : ''}</small></button>`).join('')}
        <button class="repo ${repo === '' ? 'on' : ''}" data-repo=""><span>Home (~)</span><small></small></button></div>
      <span class="label">First prompt (optional)</span>
      <textarea class="field" id="prompt" placeholder="What should it do?" enterkeyhint="go"></textarea>
      <div style="height:14px"></div><button class="btn block" id="go">Start</button><div class="err" id="err"></div>`;
    sh.querySelectorAll('[data-agent]').forEach((b) => { b.onclick = () => { agent = b.dataset.agent; keep(); draw(); }; });
    sh.querySelectorAll('[data-repo]').forEach((b) => { b.onclick = () => { repo = b.dataset.repo; keep(); draw(); }; });
    const fi = sh.querySelector('#filter'); if (fi) fi.oninput = () => { filter = fi.value; keep(); draw(); const n = sh.querySelector('#filter'); n.focus(); n.setSelectionRange(filter.length, filter.length); };
    sh.querySelector('#prompt').value = S.draftPrompt || '';
    sh.querySelector('#go').onclick = async () => {
      const btn = sh.querySelector('#go'); btn.disabled = true;
      try {
        const s = await api('/api/sessions', { method: 'POST', body: { agent, cwd: repo || undefined, prompt: sh.querySelector('#prompt').value.trim() || undefined } });
        LS.set('lastAgent', agent); S.draftPrompt = ''; close(); location.hash = `#/s/${s.id}`;
      } catch (e) { sh.querySelector('#err').textContent = e.message; btn.disabled = false; }
    };
  };
  const keep = () => { const p = sh.querySelector('#prompt'); if (p) S.draftPrompt = p.value; };
  draw();
  try { S.repos = (await api('/api/repos')).repos; if (repo === null) repo = S.repos[0]?.name ?? ''; } catch { S.repos = []; repo = ''; }
  keep(); draw();
}

// ------------------------------------------------------------------ session view
class SessionView {
  constructor(id) {
    this.id = id; this.es = null; this.atBottom = true; this.first = true;
    this.fs = LS.get('fs', 12);
    const el = document.createElement('div'); el.className = 'sess'; this.el = el;
    el.innerHTML = `<div class="sess-top">
        <button class="icon-btn" id="back">${ICON.back}</button>
        <div class="sess-title"><b id="nm"></b><span id="st"></span></div>
        <button class="icon-btn" id="more">${ICON.more}</button></div>
      <div id="ask"></div>
      <div class="screen" id="screen"><pre id="pre"></pre></div>
      <div class="keys" id="keys"></div>
      <div class="composer" id="comp"><textarea id="ta" rows="1" placeholder="Message" enterkeyhint="send" autocapitalize="sentences"></textarea><button class="send" id="send">${ICON.send}</button></div>`;
    document.body.appendChild(el);
    $app.innerHTML = '';
    el.style.setProperty('--fs', `${this.fs}px`);
    this.$ = (s) => el.querySelector(s);
    this.$('#back').onclick = () => { location.hash = '#/'; };
    this.$('#more').onclick = (e) => { e.stopPropagation(); this.menu(); };
    const sc = this.$('#screen');
    sc.addEventListener('scroll', () => { this.atBottom = sc.scrollTop + sc.clientHeight >= sc.scrollHeight - 40; });
    const ta = this.$('#ta');
    ta.addEventListener('input', () => { ta.style.height = 'auto'; ta.style.height = `${Math.min(140, ta.scrollHeight)}px`; });
    ta.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); this.sendText(); } });
    this.$('#send').onclick = () => this.sendText();
    this.onResize = () => { clearTimeout(this.rt); this.rt = setTimeout(() => this.connect(), 400); };
    window.addEventListener('resize', this.onResize);
    this.update(); this.connect();
  }
  get s() { return (S.snap?.sessions || []).find((x) => x.id === this.id); }
  update() {
    const s = this.s;
    if (!s) { this.$('#nm').textContent = 'Session not found'; this.$('#st').textContent = ''; return; }
    this.$('#nm').textContent = s.name;
    this.$('#st').textContent = `${STATE_LABEL[s.state] || s.state}${s.repo ? ` · ${s.repo}` : ''}${s.attached ? ' · attached elsewhere' : ''}`;
    const ad = S.snap.adapters[s.agent] || { answerKeys: [] };
    let ask = '';
    if (s.state === 'needs_input') {
      ask = `<div class="ask"><p>🔐 ${esc(s.waitingFor || 'Waiting for your input')}</p><div class="row">${ad.answerKeys.map((k, i) => `<button class="${k.tone || ''}" data-ans="${i}">${esc(k.label)}</button>`).join('')}</div></div>`;
    } else if (s.state === 'exited') {
      ask = `<div class="ask" style="background:#ffffff08;border-color:var(--line2)"><p style="color:var(--dim)">The agent exited; the pane is a shell now.</p><div class="row"><button class="ok" id="restart">Restart ${esc(ad.label || s.agent)}</button></div></div>`;
    }
    if (this.lastAsk !== ask) {
      this.lastAsk = ask; this.$('#ask').innerHTML = ask;
      this.$('#ask').querySelectorAll('[data-ans]').forEach((b) => { b.onclick = () => this.keys(ad.answerKeys[+b.dataset.ans].keys); });
      const r = this.$('#restart'); if (r) r.onclick = () => api(`/api/sessions/${this.id}/restart`, { method: 'POST' }).catch((e) => toast(e.message));
    }
    const ext = s.kind !== 'tmux' || s.state === 'gone';
    this.$('#comp').classList.toggle('hidden', ext); this.$('#keys').classList.toggle('hidden', ext);
    if (!this.keysDrawn && !ext) {
      this.keysDrawn = true;
      const keys = S.snap.commonKeys || [];
      this.$('#keys').innerHTML = keys.map((k, i) => `<button data-k="${i}">${esc(k.label)}</button>`).join('');
      this.$('#keys').querySelectorAll('[data-k]').forEach((b) => { b.onclick = () => this.keys(keys[+b.dataset.k].keys); });
    }
  }
  measure() {
    const probe = document.createElement('span');
    probe.style.cssText = `position:absolute;visibility:hidden;font-family:var(--mono);font-size:${this.fs}px;white-space:pre`;
    probe.textContent = 'M'.repeat(40); this.el.appendChild(probe);
    const cw = probe.getBoundingClientRect().width / 40; probe.remove();
    const sc = this.$('#screen');
    return { cols: Math.max(30, Math.floor((sc.clientWidth - 20) / cw)), rows: Math.max(12, Math.floor((sc.clientHeight - 16) / (this.fs * 1.32))) };
  }
  connect() {
    this.es?.close();
    const { cols, rows } = this.measure();
    const es = new EventSource(`/api/sessions/${this.id}/screen?cols=${cols}&rows=${rows}`);
    this.es = es;
    es.onmessage = (e) => {
      const d = JSON.parse(e.data);
      const pre = this.$('#pre');
      if (d.text === null) {
        const s = this.s;
        pre.innerHTML = `<div class="msg">${d.reason === 'external'
          ? `This ${esc(S.snap.adapters[s?.agent]?.label || 'agent')} session runs outside tmux (e.g. a VS Code terminal). You still get its notifications, but answer it where it runs.<br><br>Tip: start agents from this app or with <code>agent new</code> to control them from anywhere.`
          : 'This tmux session has ended.'}</div>`;
        return;
      }
      pre.innerHTML = ansiToHtml(d.text);
      const sc = this.$('#screen');
      if (this.atBottom || this.first) { sc.scrollTop = sc.scrollHeight; this.first = false; }
    };
    es.onerror = () => { es.close(); if (this.es === es) setTimeout(() => { if (S.session === this) this.connect(); }, 2000); };
  }
  async keys(keys) {
    try { await api(`/api/sessions/${this.id}/input`, { method: 'POST', body: { keys } }); this.atBottom = true; } catch (e) { toast(e.message); }
  }
  async sendText() {
    const ta = this.$('#ta'); const text = ta.value;
    if (!text.trim()) return this.keys(['Enter']);
    ta.value = ''; ta.style.height = 'auto';
    try { await api(`/api/sessions/${this.id}/input`, { method: 'POST', body: { text } }); this.atBottom = true; } catch (e) { ta.value = text; toast(e.message); }
  }
  menu() {
    this.el.querySelector('.menu')?.remove();
    const s = this.s; if (!s) return;
    const m = document.createElement('div'); m.className = 'menu';
    const items = [
      ['Rename', async () => { const n = prompt('Name', s.name); if (n) await api(`/api/sessions/${s.id}/rename`, { method: 'POST', body: { name: n } }); }],
      ['Bigger text', () => this.setFs(this.fs + 1)],
      ['Smaller text', () => this.setFs(this.fs - 1)],
      ...(s.cwd ? [['Open folder in VS Code', () => { const host = location.hostname.replace(/^agents\./, 'code.'); window.open(`https://${host}/?folder=${encodeURIComponent(s.cwd)}`, '_blank'); }]] : []),
      ...(s.kind === 'tmux' ? [['Copy attach command', () => { navigator.clipboard?.writeText(`tmux attach -t ${s.tmux}`); toast('Copied'); }]] : []),
      [s.kind === 'tmux' && s.state !== 'gone' ? 'Kill session' : 'Remove', async () => {
        if (!confirm(s.kind === 'tmux' && s.state !== 'gone' ? `Kill "${s.name}"? The agent and its tmux session will be stopped.` : `Remove "${s.name}" from the list?`)) return;
        await api(`/api/sessions/${s.id}`, { method: 'DELETE' }); location.hash = '#/';
      }, 'bad'],
    ];
    m.innerHTML = items.map(([l, , c], i) => `<button data-i="${i}" class="${c || ''}">${esc(l)}</button>`).join('');
    m.querySelectorAll('[data-i]').forEach((b) => { b.onclick = async () => { m.remove(); try { await items[+b.dataset.i][1](); } catch (e) { toast(e.message); } }; });
    this.el.appendChild(m);
    setTimeout(() => document.addEventListener('click', () => m.remove(), { once: true }), 0);
  }
  setFs(n) { this.fs = Math.max(8, Math.min(20, n)); LS.set('fs', this.fs); this.el.style.setProperty('--fs', `${this.fs}px`); this.connect(); }
  close() { this.es?.close(); window.removeEventListener('resize', this.onResize); this.el.remove(); }
}

// ------------------------------------------------------------------ settings
function settingsHtml() {
  const d = S.snap.devbox; const g = S.gw;
  const standalone = matchMedia('(display-mode: standalone)').matches || navigator.standalone;
  const pushOk = 'PushManager' in window && 'serviceWorker' in navigator;
  const perm = typeof Notification !== 'undefined' ? Notification.permission : 'unsupported';
  const sync = d.sync || {};
  const adapters = Object.entries(S.snap.adapters);
  const typ = g?.typical?.restore?.samples ? Object.values(g.typical.restore.phases).reduce((a, b) => a + b, 0) / 1000 : null;
  return `
  <div class="section"><span>Notifications</span></div>
  <div class="card">
    ${!pushOk || (!standalone && /iPhone|iPad/.test(navigator.userAgent))
      ? '<h3>Add to Home Screen first</h3><p>On iPhone, notifications only work for apps added to the Home Screen: tap Share → Add to Home Screen, then open Agents from there.</p>'
      : `<h3>${perm === 'granted' ? 'Notifications are on' : 'Get notified on this device'}</h3>
         <p>When an agent needs permission, asks you something, errors, or finishes a longer task.</p>
         <div class="row" style="margin-top:12px"><button class="btn" id="pushOn">${perm === 'granted' ? 'Re-register this device' : 'Enable notifications'}</button><button class="btn ghost" id="pushTest">Send test</button></div>
         <p style="margin-top:10px;font-size:13px">${S.snap.pushDevices} device${S.snap.pushDevices === 1 ? '' : 's'} registered.</p>`}
    <span class="label">Notify when a turn finishes after (seconds)</span>
    <input class="field" id="thr" type="number" inputmode="numeric" min="0" value="">
  </div>

  <div class="section"><span>Devbox</span></div>
  <div class="card">
    <div class="kv"><span>Status</span><span>${d.idle?.busy ? `Busy — ${esc(d.idle.reasons.join(', '))}` : `Idle for ${dur(d.idle?.idleSeconds || 0)}`}</span></div>
    ${g?.idle?.limitMs ? `<div class="kv"><span>Sleeps after</span><span>${Math.round(g.idle.limitMs / 60000)} min idle</span></div>` : ''}
    <div class="kv"><span>Up for</span><span>${dur(d.uptime)}</span></div>
    ${typ ? `<div class="kv"><span>Typical wake</span><span>${typ.toFixed(1)}s</span></div>` : ''}
    ${g?.lastStop ? `<div class="kv"><span>Last slept</span><span>${new Date(g.lastStop.at).toLocaleString()}</span></div>` : ''}
  </div>

  <div class="section"><span>Repo sync</span><span>${sync.finishedAt ? ago(sync.finishedAt) + ' ago' : sync.state === 'running' ? 'running' : 'never'}</span></div>
  <div class="card">
    ${(sync.results || []).map((r) => `<div class="kv"><span>${esc(r.name)}</span><span class="chip ${r.status === 'conflict' || r.status === 'error' ? 'error' : r.status === 'updated' ? 'idle' : ''}" title="${esc(r.detail)}">${esc(r.status)}</span></div>${r.detail && (r.status === 'conflict' || r.status === 'error') ? `<p style="font-size:12.5px">${esc(r.detail)}</p>` : ''}`).join('') || '<p>No sync has run yet.</p>'}
    <div style="height:10px"></div><button class="btn ghost block" id="syncNow" ${sync.state === 'running' ? 'disabled' : ''}>${sync.state === 'running' ? `Syncing ${sync.done}/${sync.total || '…'}` : 'Sync now'}</button>
  </div>

  <div class="section"><span>Agents</span></div>
  <div class="card">${adapters.map(([, a]) => `<div class="kv"><span>${esc(a.label)}</span><span>${a.available ? 'installed' : '<span style="color:var(--faint)">not installed</span>'}</span></div>`).join('')}</div>

  <div style="height:16px"></div><button class="btn danger block" id="logout">Sign out</button>`;
}
function urlB64ToU8(b64) { const pad = '='.repeat((4 - (b64.length % 4)) % 4); const raw = atob((b64 + pad).replace(/-/g, '+').replace(/_/g, '/')); return Uint8Array.from(raw, (c) => c.charCodeAt(0)); }
function deviceName() { const ua = navigator.userAgent; return /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android' : /Mac/.test(ua) ? 'Mac' : 'Browser'; }
function bindSettings() {
  const q = (s) => $app.querySelector(s);
  api('/api/settings').then((r) => { const t = q('#thr'); if (t && document.activeElement !== t) t.value = r.finishedNotifyAfterSec; }).catch(() => {});
  const thr = q('#thr'); if (thr) thr.onchange = () => api('/api/settings', { method: 'POST', body: { finishedNotifyAfterSec: +thr.value } }).then(() => toast('Saved')).catch((e) => toast(e.message));
  const on = q('#pushOn');
  if (on) on.onclick = async () => {
    try {
      const perm = await Notification.requestPermission();
      if (perm !== 'granted') return toast('Notifications were not allowed');
      const reg = await navigator.serviceWorker.ready;
      let sub = await reg.pushManager.getSubscription();
      if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlB64ToU8(S.snap.vapidPublicKey) });
      await api('/api/push/subscribe', { method: 'POST', body: { subscription: sub.toJSON(), device: deviceName() } });
      toast('This device will get notifications');
    } catch (e) { toast(e.message); }
  };
  const test = q('#pushTest'); if (test) test.onclick = () => api('/api/push/test', { method: 'POST' }).then((r) => toast(`Sent to ${r.devices} device(s)`)).catch((e) => toast(e.message));
  q('#syncNow').onclick = () => api('/api/sync', { method: 'POST' }).then(() => toast('Syncing…')).catch((e) => toast(e.message));
  q('#logout').onclick = async () => { await api('/api/logout', { method: 'POST' }); S.stream?.close(); S.mode = 'login'; render(); };
}

start();
