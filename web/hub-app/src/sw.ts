/// <reference lib="webworker" />
// The service worker: the app shell for when nothing answers, and push notifications.
import type { PushPayload } from "@devbox/shared/proto/types";

declare const self: ServiceWorkerGlobalScope;
// Both are filled in by the build (vite.config.ts): a hash of everything the app is made of, so
// a new version of the hub is a new cache, and the files of the shell.
declare const __BUILD__: string;
declare const __SHELL__: string[];

const CACHE = `agents-${__BUILD__}`;

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) => cache.addAll(__SHELL__))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  const url = new URL(request.url);
  if (request.method !== "GET" || url.origin !== location.origin) return;
  if (url.pathname.startsWith("/api/") || url.pathname.startsWith("/__devbox/")) return; // always live
  const cached = async (path: RequestInfo) => (await caches.match(path)) ?? Response.error();
  if (request.mode === "navigate") {
    // While the devbox is asleep the gateway answers navigations with its boot screen, which
    // wakes it and reloads into the app. The cached shell is for when nothing answers at all.
    event.respondWith(
      fetch(request).then(
        (response) => (response.ok ? response : cached("/")),
        () => cached("/"),
      ),
    );
    return;
  }
  event.respondWith(caches.match(request).then((hit) => hit ?? fetch(request)));
});

self.addEventListener("push", (event) => {
  let payload: Partial<PushPayload>;
  try {
    payload = event.data?.json() as Partial<PushPayload>;
  } catch {
    payload = { title: "Devbox", body: event.data?.text() ?? "" };
  }
  // iOS wants a visible notification for every push.
  const tasks: Promise<unknown>[] = [
    self.registration.showNotification(payload.title ?? "Devbox", {
      body: payload.body ?? "",
      ...(payload.tag ? { tag: payload.tag, renotify: true } : {}),
      icon: "/icon-192.png",
      badge: "/icon-192.png",
      data: { url: payload.url ?? "/" },
    }),
  ];
  if (typeof payload.badge === "number" && "setAppBadge" in self.navigator) {
    tasks.push(
      payload.badge ? self.navigator.setAppBadge(payload.badge) : self.navigator.clearAppBadge(),
    );
  }
  event.waitUntil(Promise.all(tasks).catch(() => undefined));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const data = event.notification.data as { url?: string } | null;
  const target = new URL(data?.url ?? "/", location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((windows) => {
      for (const open of windows) {
        if (new URL(open.url).origin === location.origin) {
          open.postMessage({ navigate: target });
          return open.focus();
        }
      }
      return self.clients.openWindow(target);
    }),
  );
});
