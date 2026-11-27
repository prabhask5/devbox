import { useEffect, useState } from "preact/hooks";
import type { GatewayStatus, HubState, Session } from "@devbox/shared/proto/types";
import { badgeCount } from "./format.ts";

/** A directory of the code directory, as GET /api/repos lists it. */
export interface Repo {
  name: string;
  git: boolean;
  branch?: string;
  dirty?: number;
  ahead?: number;
  behind?: number;
}

export type Route =
  { name: "sessions" | "assistant" | "settings" } | { name: "session"; id: string };

export interface AppState {
  /** loading: nothing known yet. login: the hub wants the app password. asleep: the gateway
   * answered for a sleeping devbox. offline: nothing answered. app: signed in and awake. */
  mode: "loading" | "login" | "asleep" | "offline" | "app";
  state?: HubState;
  /** The gateway's status: when the devbox will sleep, typical wake time. */
  gateway?: GatewayStatus;
  /** What the gateway said when it answered instead of the sleeping hub. */
  asleep?: GatewayStatus;
  route: Route;
  toast?: string;
  /** A new version of the app is installed and waits for a reload. */
  updated: boolean;
}

class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly data: unknown,
  ) {
    super(message);
  }
}

/** Calls the hub (or the gateway, on the same origin) and returns the JSON it answers. */
export async function api<T = unknown>(
  path: string,
  options: { method?: "GET" | "POST" | "DELETE"; body?: unknown } = {},
): Promise<T> {
  const method = options.method ?? "GET";
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: method === "GET" ? {} : { "Content-Type": "application/json" },
    ...(method === "GET" ? {} : { body: JSON.stringify(options.body ?? {}) }),
  });
  const data = (await response.json().catch(() => ({}))) as { error?: string };
  if (!response.ok) {
    throw new ApiError(data.error ?? `HTTP ${String(response.status)}`, response.status, data);
  }
  return data as T;
}

function parseRoute(hash: string): Route {
  const path = hash.replace(/^#/, "") || "/";
  const session = /^\/s\/([a-f0-9]+)/.exec(path);
  if (session?.[1]) return { name: "session", id: session[1] };
  if (path.startsWith("/assistant")) return { name: "assistant" };
  if (path.startsWith("/settings")) return { name: "settings" };
  return { name: "sessions" };
}

let current: AppState = { mode: "loading", route: parseRoute(location.hash), updated: false };
const listeners = new Set<() => void>();

function set(change: Partial<AppState>) {
  current = { ...current, ...change };
  for (const listener of listeners) listener();
}

/** The app's state; the component re-renders when it changes. */
export function useAppState(): AppState {
  const [, redraw] = useState(0);
  const rendered = current;
  useEffect(() => {
    const listener = () => {
      redraw((n) => n + 1);
    };
    listeners.add(listener);
    // Effects run after the first paint; the state may have moved on in between.
    if (current !== rendered) listener();
    return () => {
      listeners.delete(listener);
    };
  }, []);
  return current;
}

export function session(state: AppState, id: string): Session | undefined {
  return state.state?.sessions.find((s) => s.id === id);
}

/** Shows a session the hub just created without waiting for the stream to bring it. */
export function created(s: Session) {
  const state = current.state;
  if (state && !state.sessions.some((known) => known.id === s.id)) {
    set({ state: { ...state, sessions: [s, ...state.sessions] } });
  }
}

let toastTimer: ReturnType<typeof setTimeout> | undefined;

/** Shows a short message at the bottom of the screen. */
export function toast(message: string) {
  clearTimeout(toastTimer);
  set({ toast: message });
  toastTimer = setTimeout(() => {
    const rest = { ...current };
    delete rest.toast;
    current = rest;
    for (const listener of listeners) listener();
  }, 2600);
}

/** Shows an action's failure as a toast. */
export function report(error: unknown) {
  toast(error instanceof Error ? error.message : String(error));
}

function setBadge(state: HubState) {
  if (!("setAppBadge" in navigator)) return;
  const count = badgeCount(state.sessions);
  void (count ? navigator.setAppBadge(count) : navigator.clearAppBadge()).catch(() => undefined);
}

let stream: EventSource | undefined;

function openStream() {
  if (stream && stream.readyState !== EventSource.CLOSED) return;
  const source = new EventSource("/api/stream");
  stream = source;
  source.onmessage = (event: MessageEvent<string>) => {
    const state = JSON.parse(event.data) as HubState;
    setBadge(state);
    set({ state });
  };
  source.onerror = () => {
    source.close();
    if (stream === source) stream = undefined;
    setTimeout(() => {
      if (document.visibilityState === "visible") void connect();
    }, 2500);
  };
}

/** Finds out where things stand: signed in, signed out, asleep or unreachable. */
export async function connect(): Promise<void> {
  try {
    const state = await api<HubState>("/api/state");
    set({ state, mode: "app" });
    openStream();
  } catch (error) {
    stream?.close();
    stream = undefined;
    const status = error instanceof ApiError ? error.status : 0;
    if (status === 401) set({ mode: "login" });
    else if (status === 503 || status === 502) {
      set({ mode: "asleep", asleep: (error as ApiError).data as GatewayStatus });
    } else set({ mode: "offline" });
  }
}

/** Leaves the signed-in state after signing out. */
export function signedOut() {
  stream?.close();
  stream = undefined;
  set({ mode: "login" });
}

async function pollGateway() {
  if (document.visibilityState !== "visible") return;
  try {
    const response = await fetch("/__devbox/status", { cache: "no-store" });
    if (response.ok) set({ gateway: (await response.json()) as GatewayStatus });
  } catch {
    // Not behind the gateway (development), or offline: the pill falls back to the hub's idle time.
  }
}

/** Starts everything that runs for the life of the page. */
export function start() {
  window.addEventListener("hashchange", () => {
    set({ route: parseRoute(location.hash) });
  });
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") void connect();
  });
  setInterval(() => void pollGateway(), 20000);
  void pollGateway();
  void connect();

  if ("serviceWorker" in navigator) {
    // A tap on a notification asks the open app to show the session it is about.
    navigator.serviceWorker.addEventListener(
      "message",
      (event: MessageEvent<{ navigate?: string }>) => {
        if (event.data.navigate) location.hash = new URL(event.data.navigate).hash || "#/";
      },
    );
    void navigator.serviceWorker
      .register("/sw.js")
      .then((registration) => {
        registration.addEventListener("updatefound", () => {
          // The first worker of a fresh install replaces nothing; a later one replaces the
          // worker this page was loaded with, and the page still runs the old code.
          const replacing = navigator.serviceWorker.controller !== null;
          const installing = registration.installing;
          installing?.addEventListener("statechange", () => {
            if (installing.state === "activated" && replacing) set({ updated: true });
          });
        });
      })
      .catch(() => undefined);
  }
}
