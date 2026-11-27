import { useEffect, useRef, useState } from "preact/hooks";
import { PHASES, progress } from "@devbox/shared";
import type { GatewayStatus } from "@devbox/shared/proto/types";
import { api, connect } from "../store.ts";

/** Signing in to the app. */
export function Login() {
  const [error, setError] = useState("");
  const password = useRef<HTMLInputElement>(null);
  useEffect(() => {
    password.current?.focus();
  }, []);
  return (
    <div class="center">
      <h2>Agents</h2>
      <p>Sign in to your devbox.</p>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          api("/api/login", { method: "POST", body: { password: password.current?.value ?? "" } })
            .then(connect)
            .catch((failure: unknown) => {
              setError(failure instanceof Error ? failure.message : String(failure));
            });
        }}
      >
        <input
          ref={password}
          class="field"
          type="password"
          name="pw"
          placeholder="Password"
          aria-label="Password"
          autocomplete="current-password"
          required
        />
        <div style="height:10px" />
        <button class="btn block">Sign in</button>
        <div class="err" role="alert">
          {error}
        </div>
      </form>
    </div>
  );
}

/** Nothing answered at all. */
export function Offline() {
  return (
    <div class="center">
      <h2>Can't reach the devbox</h2>
      <p>Check your connection.</p>
      <button class="btn" onClick={() => void connect()}>
        Try again
      </button>
    </div>
  );
}

/**
 * The devbox is asleep: the gateway answered instead of the hub. Wakes it — after the wake
 * password, when this device has not given it yet — and follows the wake to its end.
 */
export function Asleep({ info }: { info: GatewayStatus | undefined }) {
  const needsAuth = info?.authed === false;
  const [waking, setWaking] = useState(info?.phase === "waking");
  const [status, setStatus] = useState<GatewayStatus>();
  const [error, setError] = useState("");
  const password = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!waking) return undefined;
    let events: EventSource | undefined;
    let retry: ReturnType<typeof setTimeout> | undefined;
    const follow = () => {
      events = new EventSource("/__devbox/events");
      events.onmessage = (event: MessageEvent<string>) => {
        const latest = JSON.parse(event.data) as GatewayStatus;
        setStatus(latest);
        if (latest.wake?.error !== undefined) events?.close();
        if (latest.wake?.done) {
          events?.close();
          setTimeout(() => {
            location.reload();
          }, 400);
        }
      };
      events.onerror = () => {
        events?.close();
        retry = setTimeout(follow, 2000);
      };
    };
    follow();
    return () => {
      events?.close();
      clearTimeout(retry);
    };
  }, [waking]);

  const wake = (path: string, body: unknown) => {
    api(path, { method: "POST", body })
      .then(() => {
        setWaking(true);
      })
      .catch((failure: unknown) => {
        setError(failure instanceof Error ? failure.message : String(failure));
      });
  };

  const w = status?.wake;
  const at = status ? progress(status.wake ?? emptyWake, status.serverNow) : undefined;
  let title = waking ? "Waking up…" : "Devbox is asleep";
  if (w?.error !== undefined) title = "Couldn't wake the devbox";
  else if (w?.done) title = "Ready";
  const phase =
    w?.error !== undefined
      ? ""
      : (PHASES.find((p) => p.id === w?.current)?.label ?? (w?.done ? "Ready" : "Starting…"));
  const left =
    at && !w?.done && at.remainingMs > 500
      ? `~${String(Math.ceil(at.remainingMs / 1000))}s left`
      : "";

  return (
    <div class="center">
      <h2>{title}</h2>
      <p>
        {needsAuth
          ? "Enter your wake password to start it."
          : "It went to sleep to free up the server. Your sessions are saved."}
      </p>
      {!waking && needsAuth && (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            wake("/__devbox/login", { password: password.current?.value ?? "" });
          }}
        >
          <input
            ref={password}
            class="field"
            type="password"
            name="pw"
            placeholder="Wake password"
            aria-label="Wake password"
            required
          />
          <div style="height:10px" />
          <button class="btn block">Wake devbox</button>
        </form>
      )}
      {!waking && !needsAuth && (
        <button
          class="btn block"
          onClick={() => {
            wake("/__devbox/wake", {});
          }}
        >
          Wake devbox
        </button>
      )}
      {waking && (
        <div>
          <div
            class="bar"
            role="progressbar"
            aria-label="Wake progress"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(at?.percent ?? 0)}
          >
            <i style={{ width: `${String(w ? (at?.percent ?? 0) : 0)}%` }} />
          </div>
          <div class="row" style="justify-content:space-between;color:var(--dim);font-size:13px">
            <span>{w ? phase : ""}</span>
            <span>{w ? left : ""}</span>
          </div>
        </div>
      )}
      <div class="err" role="alert">
        {w?.error ?? error}
      </div>
    </div>
  );
}

const emptyWake = {
  id: "",
  kind: "attach" as const,
  reason: "",
  requestedAt: 0,
  phases: {},
  done: false,
  eta: { phases: { start: 0, code: 0, hub: 0, sync: 0 }, samples: 0 },
  log: [],
};
