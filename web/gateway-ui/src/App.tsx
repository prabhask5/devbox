import type { GatewayStatus, SyncState, Wake } from "@devbox/shared/proto/types";
import { PHASES, progress, seconds } from "@devbox/shared";

/** What the gateway tells the page about the request it answered with the boot screen. */
export interface Boot {
  host: string;
  app: "code" | "agents";
  needsAuth: boolean;
  error?: string;
  /** After a failed sign-in: the page the form should still return to. */
  next?: string;
  upstreamDown?: boolean;
  unconfigured?: boolean;
}

export interface AppProps {
  boot: Boot;
  /** The latest gateway status; absent until the event stream delivers one. */
  status?: GatewayStatus | undefined;
  /** The gateway's clock, in ms. */
  now: number;
  /** The page to return to after signing in. */
  next: string;
  onSkip: () => void;
  onRetry: () => void;
}

type Dot = "" | "live" | "ok" | "bad";

interface Headline {
  dot: Dot;
  title: string;
  sub: string;
}

/** What the page should do by itself once it shows a state. */
export type Followup = "none" | "open" | "reconnect";

function service(boot: Boot): string {
  return boot.app === "agents" ? "the agents hub" : "code-server";
}

function headline(boot: Boot, status: GatewayStatus | undefined): Headline {
  if (boot.unconfigured) {
    return {
      dot: "",
      title: "devbox is not set up yet",
      sub: "Run devbox up on the host to finish setting it up.",
    };
  }
  if (boot.needsAuth) {
    return {
      dot: "",
      title: "Devbox is asleep",
      sub: "Enter your wake password to start it. This device will be remembered.",
    };
  }
  if (!status) return { dot: "", title: "Connecting…", sub: "" };
  const wake = status.wake;
  if (boot.upstreamDown && (!wake || wake.done)) {
    return {
      dot: "live",
      title: "Reconnecting…",
      sub: `The devbox is up but ${service(boot)} isn't answering yet.`,
    };
  }
  if (status.phase === "stopping") {
    return { dot: "live", title: "Going to sleep…", sub: "It will wake right back up after." };
  }
  if (!wake) {
    return {
      dot: "",
      title: status.vm === "running" ? "Almost there…" : "Waking your devbox",
      sub: "",
    };
  }
  if (wake.error !== undefined) {
    return { dot: "bad", title: "Couldn't wake the devbox", sub: wake.error };
  }
  if (wake.done) {
    return {
      dot: "ok",
      title: "Ready",
      sub: `Opening ${boot.app === "agents" ? "Agents" : "VS Code"}…`,
    };
  }
  const titles = {
    restore: "Waking your devbox",
    cold: "Booting your devbox",
    attach: "Getting things ready",
  };
  const samples = wake.kind === "attach" ? 0 : status.typical[wake.kind].samples;
  const total = PHASES.reduce((sum, { id }) => sum + wake.eta.phases[id], 0);
  return {
    dot: "live",
    title: titles[wake.kind],
    sub:
      samples > 0
        ? `Usually takes about ${seconds(total)} (median of last ${String(samples)})`
        : "First wake — learning how long this takes",
  };
}

/** What the page does next in the state it shows: reload into the app, retry, or wait. */
export function followup(boot: Boot, status: GatewayStatus | undefined): Followup {
  if (boot.unconfigured || boot.needsAuth || !status) return "none";
  const wake = status.wake;
  if (boot.upstreamDown && (!wake || wake.done)) return "reconnect";
  if (status.phase !== "stopping" && wake?.done) return "open";
  return "none";
}

function Check() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
      <path
        d="M3 8.5l3.2 3L13 4.5"
        fill="none"
        stroke="var(--ok)"
        stroke-width="2.2"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
  );
}

function syncLine(sync: SyncState | undefined): string {
  if (!sync || sync.total === 0) return "";
  const current = sync.current === undefined ? "" : ` · ${sync.current}`;
  return `${String(sync.done)} of ${String(sync.total)} repos${current}`;
}

function Phases({ wake, now }: { wake: Wake; now: number }) {
  return (
    <ol>
      {PHASES.map(({ id, label }) => {
        const timing = wake.phases[id];
        const eta = wake.eta.phases[id];
        let state = "pending";
        let icon = <span class="ring" />;
        let time = eta > 0 ? `~${seconds(eta)}` : "";
        if (timing?.skipped) {
          state = "skipped";
          icon = <span class="dash">–</span>;
          time = "skipped";
        } else if (timing?.end !== undefined) {
          state = "done";
          icon = <Check />;
          time = seconds(timing.end - timing.start);
        } else if (timing && wake.current === id) {
          state = "active";
          icon = <span class="spin" />;
          time = seconds(now - timing.start) + (eta > 0 ? ` / ~${seconds(eta)}` : "");
        }
        let detail = "";
        if (id === "start" && wake.kind === "restore") {
          detail = "Restoring saved memory — your sessions pick up where they left off";
        } else if (id === "start" && wake.kind === "cold") {
          detail = "Fresh boot";
        } else if (id === "sync") {
          detail = syncLine(wake.sync);
        }
        return (
          <li key={id} class={state}>
            <span class="ic">{icon}</span>
            <span class="lbl">
              {label}
              {detail && <small>{detail}</small>}
            </span>
            <span class="t">{time}</span>
          </li>
        );
      })}
    </ol>
  );
}

const MARKS: Partial<Record<string, string>> = { updated: " ↓", conflict: " ⚠", error: " ✕" };

function Repos({ sync }: { sync: SyncState | undefined }) {
  return (
    <div class="repos">
      {sync?.results.map((repo) => (
        <span key={repo.name} class={`chip ${repo.status}`} title={repo.detail}>
          {repo.name}
          {MARKS[repo.status] ?? ""}
        </span>
      ))}
      {sync?.current !== undefined && <span class="chip syncing">{sync.current}…</span>}
    </div>
  );
}

function WakeProgress({ wake, now, onSkip, onRetry }: { wake: Wake } & AppProps) {
  const { percent, elapsedMs, remainingMs, overrun } = progress(wake, now);
  let left = "";
  if (wake.done) left = "done";
  else if (wake.error === undefined && remainingMs > 500)
    left = `about ${seconds(remainingMs)} left`;
  else if (overrun) left = "taking a bit longer…";
  return (
    <section>
      <div
        class="bar"
        role="progressbar"
        aria-label="Wake progress"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(percent)}
      >
        <i style={{ width: `${String(percent)}%` }} />
      </div>
      <div class="meta">
        <span>{seconds(elapsedMs)}</span>
        <span>{left}</span>
      </div>
      <Phases wake={wake} now={now} />
      <Repos sync={wake.sync} />
      <div class="row">
        {wake.current === "sync" && !wake.done && (
          <button type="button" class="ghost" onClick={onSkip}>
            Open now
          </button>
        )}
        {wake.error !== undefined && (
          <button type="button" onClick={onRetry}>
            Try again
          </button>
        )}
      </div>
      <details>
        <summary>Details</summary>
        <pre>{wake.log.join("\n")}</pre>
      </details>
    </section>
  );
}

/** The boot screen: one page for every state between "asleep" and "ready". */
export function App(props: AppProps) {
  const { boot, status, next } = props;
  const { dot, title, sub } = headline(boot, status);
  const wake = boot.unconfigured || boot.needsAuth ? undefined : status?.wake;
  const lastStop = status?.lastStop;
  return (
    <>
      <div class="brand">
        <span class={`dot ${dot}`} />
        <span>{boot.host || "devbox"}</span>
      </div>
      <div aria-live="polite">
        <h1>{title}</h1>
        <p class="sub">{sub}</p>
      </div>
      {boot.needsAuth && !boot.unconfigured && (
        <section>
          <form class="auth" method="post" action="/__devbox/login">
            <input
              type="password"
              name="password"
              placeholder="Wake password"
              aria-label="Wake password"
              autocomplete="current-password"
              autofocus
              required
            />
            <input type="hidden" name="next" value={next} />
            <button type="submit">Wake</button>
          </form>
          {boot.error && (
            <p class="err" role="alert">
              {boot.error}
            </p>
          )}
        </section>
      )}
      {wake && <WakeProgress {...props} wake={wake} />}
      <footer>
        {lastStop &&
          !boot.needsAuth &&
          `Went to sleep ${new Date(lastStop.at).toLocaleString()} (${lastStop.reason})`}
      </footer>
    </>
  );
}
