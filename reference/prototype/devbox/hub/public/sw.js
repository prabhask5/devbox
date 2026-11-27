// Service worker: offline app shell + push notifications.
const CACHE = 'agents-v1';
const SHELL = ['/', '/index.html', '/app.js', '/app.css', '/icon.svg', '/icon-192.png', '/apple-touch-icon.png', '/manifest.webmanifest'];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});
self.addEventListener('activate', (e) => {
  e.waitUntil(caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim()));
});

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== 'GET' || url.origin !== location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/__devbox/')) return; // always live
  if (e.request.mode === 'navigate') {
    // While the devbox is asleep the gateway answers navigations with its boot page (which wakes it
    // and reloads into the app). The cached shell is only the fallback when nothing answers at all.
    e.respondWith(fetch(e.request).then((r) => (r.ok ? r : caches.match('/index.html'))).catch(() => caches.match('/index.html')));
    return;
  }
  e.respondWith(caches.match(e.request).then((hit) => {
    const net = fetch(e.request).then((r) => { if (r.ok) caches.open(CACHE).then((c) => c.put(e.request, r.clone())); return r; }).catch(() => hit);
    return hit || net;
  }));
});

self.addEventListener('push', (e) => {
  let d = {};
  try { d = e.data.json(); } catch { d = { title: 'Devbox', body: e.data ? e.data.text() : '' }; }
  const tasks = [self.registration.showNotification(d.title || 'Devbox', {
    body: d.body || '', tag: d.tag, renotify: !!d.tag, icon: '/icon-192.png', badge: '/icon-192.png',
    data: { url: d.url || '/' },
  })];
  if (typeof d.badge === 'number' && self.navigator.setAppBadge) tasks.push(d.badge ? self.navigator.setAppBadge(d.badge) : self.navigator.clearAppBadge());
  e.waitUntil(Promise.all(tasks).catch(() => {}));
});

self.addEventListener('notificationclick', (e) => {
  e.notification.close();
  const target = new URL(e.notification.data?.url || '/', location.origin).href;
  e.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((wins) => {
    for (const w of wins) {
      if (new URL(w.url).origin === location.origin) { w.postMessage({ navigate: target }); return w.focus(); }
    }
    return self.clients.openWindow(target);
  }));
});
