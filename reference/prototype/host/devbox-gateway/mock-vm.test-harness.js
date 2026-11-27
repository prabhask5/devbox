// Mock Incus REST API (unix socket) + mock code-server (:18080) + mock hub (:18090) + mock sshd (:18022)
const http = require('http'), net = require('net'), fs = require('fs');
const SOCK = '/tmp/mock-incus.sock'; try { fs.unlinkSync(SOCK); } catch {}
let status = 'Stopped', stateful = true, ops = {}, n = 0, failStateful = process.env.FAIL_STATEFUL === '1';
const log = (...a) => console.log('[mock]', ...a);
http.createServer((req, res) => {
  let b = ''; req.on('data', c => b += c); req.on('end', () => {
    const send = (o, code = 200) => { res.writeHead(code, {'content-type': 'application/json'}); res.end(JSON.stringify(o)); };
    if (req.url === '/1.0/instances/devbox' ) return send({ type: 'sync', metadata: { stateful } });
    if (req.url === '/1.0/instances/devbox/state' && req.method === 'GET') return send({ type: 'sync', metadata: { status } });
    if (req.url === '/1.0/instances/devbox/state' && req.method === 'PUT') {
      const body = JSON.parse(b); const id = 'op' + (++n); log('PUT state', JSON.stringify(body));
      ops[id] = new Promise(r => setTimeout(() => {
        if (body.action === 'start') { status = 'Running'; stateful = false; bootAt = Date.now(); r(null); }
        else if (body.action === 'stop') { if (body.stateful && failStateful) return r('stateful not enabled'); status = 'Stopped'; stateful = !!body.stateful; r(null); }
      }, body.action === 'start' ? +(process.env.START_MS||600) : 300));
      return send({ type: 'async', operation: '/1.0/operations/' + id });
    }
    const m = req.url.match(/^\/1\.0\/operations\/(op\d+)\/wait/);
    if (m) return ops[m[1]].then(err => send({ type: 'sync', metadata: err ? { status_code: 400, err } : { status_code: 200 } }));
    send({ type: 'error', error: 'not found ' + req.url }, 404);
  });
}).listen(SOCK, () => log('incus on', SOCK));
let bootAt = 0;
const SLOW = +(process.env.SLOW||1); const up = (delay) => status === 'Running' && Date.now() - bootAt > delay*SLOW;
http.createServer((req, res) => {
  if (!up(800)) { req.socket.destroy(); return; }
  if (req.url === '/healthz') { res.end('{"status":"alive"}'); return; }
  res.writeHead(200, {'content-type': 'text/html', 'set-cookie': ['a=1', 'b=2']}); res.end('<h1>code-server ' + req.headers['x-forwarded-host'] + '</h1>');
}).on('upgrade', (req, sock) => {
  if (!up(800)) return sock.destroy();
  sock.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n');
  sock.on('data', d => sock.write('echo:' + d));
}).listen(18080);
let sync = { runId: null, state: 'idle', results: [] }, busy = process.env.HUB_BUSY === '1';
http.createServer((req, res) => {
  if (!up(1200)) { req.socket.destroy(); return; }
  const send = o => { res.writeHead(200, {'content-type': 'application/json'}); res.end(JSON.stringify(o)); };
  if (req.url === '/devbox/status') return send({ idle: { busy, idleSeconds: busy ? 0 : 99999, reasons: busy ? ['claude working'] : [] }, sync });
  if (req.url === '/devbox/wake') {
    const runId = 'r' + Date.now(); const names = ['stellar', 'shortless-yt', 'compiler-ui'];
    sync = { runId, state: 'running', total: 3, done: 0, results: [], current: names[0] };
    names.forEach((nm, i) => setTimeout(() => { sync.done = i + 1; sync.results.push({ name: nm, status: i === 1 ? 'updated' : 'current' }); sync.current = names[i + 1] || null; if (i === 2) sync.state = 'done'; }, 400 * SLOW * (i + 1)));
    return send({ runId });
  }
  if (req.url === '/') { res.writeHead(200, {'content-type': 'text/html'}); return res.end('<h1>hub</h1>'); }
  send({});
}).listen(18090);
net.createServer(s => { if (!up(500)) return s.destroy(); s.write('SSH-2.0-mock\r\n'); s.on('data', d => s.write('ssh-echo:' + d)); }).listen(18022);
