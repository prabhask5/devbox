import { useEffect, useRef } from "preact/hooks";
import type { GatewayStatus, HubState } from "@devbox/shared/proto/types";
import { ago, base64UrlToBytes, deviceName, duration } from "../format.ts";
import { api, report, signedOut, toast } from "../store.ts";

async function enablePush(vapidPublicKey: string) {
  const permission = await Notification.requestPermission();
  if (permission !== "granted") {
    toast("Notifications were not allowed");
    return;
  }
  const registration = await navigator.serviceWorker.ready;
  const subscription =
    (await registration.pushManager.getSubscription()) ??
    (await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: base64UrlToBytes(vapidPublicKey),
    }));
  await api("/api/push/subscribe", {
    method: "POST",
    body: { subscription: subscription.toJSON(), device: deviceName(navigator.userAgent) },
  });
  toast("This device will get notifications");
}

function Notifications({ state }: { state: HubState }) {
  const threshold = useRef<HTMLInputElement>(null);
  useEffect(() => {
    api<{ finishedNotifyAfterSec: number }>("/api/settings")
      .then((settings) => {
        const field = threshold.current;
        if (field && document.activeElement !== field) {
          field.value = String(settings.finishedNotifyAfterSec);
        }
      })
      .catch(() => undefined);
  }, []);
  const standalone =
    matchMedia("(display-mode: standalone)").matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true;
  const supported = "PushManager" in window && "serviceWorker" in navigator;
  const granted = typeof Notification !== "undefined" && Notification.permission === "granted";
  // On iPhone and iPad, only an app on the Home Screen may ask for notifications.
  const needsHomeScreen = !supported || (!standalone && /iPhone|iPad/.test(navigator.userAgent));
  return (
    <div class="card">
      {needsHomeScreen ? (
        <>
          <h3>Add to Home Screen first</h3>
          <p>
            On iPhone, notifications only work for apps added to the Home Screen: tap Share → Add to
            Home Screen, then open Agents from there.
          </p>
        </>
      ) : (
        <>
          <h3>{granted ? "Notifications are on" : "Get notified on this device"}</h3>
          <p>
            When an agent needs permission, asks you something, errors, or finishes a longer task.
          </p>
          <div class="row" style="margin-top:12px">
            <button
              class="btn"
              onClick={() => {
                enablePush(state.vapidPublicKey ?? "").catch(report);
              }}
            >
              {granted ? "Re-register this device" : "Enable notifications"}
            </button>
            <button
              class="btn ghost"
              onClick={() => {
                api<{ devices: number }>("/api/push/test", { method: "POST" })
                  .then((sent) => {
                    toast(`Sent to ${String(sent.devices)} device(s)`);
                  })
                  .catch(report);
              }}
            >
              Send test
            </button>
          </div>
          <p style="margin-top:10px;font-size:13px">
            {state.pushDevices} device{state.pushDevices === 1 ? "" : "s"} registered.
          </p>
        </>
      )}
      <label class="label" for="threshold">
        Notify when a turn finishes after (seconds)
      </label>
      <input
        ref={threshold}
        id="threshold"
        class="field"
        type="number"
        inputmode="numeric"
        min="0"
        onChange={(event) => {
          api("/api/settings", {
            method: "POST",
            body: { finishedNotifyAfterSec: Number(event.currentTarget.value) },
          })
            .then(() => {
              toast("Saved");
            })
            .catch(report);
        }}
      />
    </div>
  );
}

/** The settings tab: notifications, the devbox's status, repo sync, the agents, signing out. */
export function Settings({
  state,
  gateway,
}: {
  state: HubState;
  gateway: GatewayStatus | undefined;
}) {
  const { idle, sync, uptime, version, deploy } = state.devbox;
  const typical = gateway?.typical.restore;
  const phases = typical?.samples ? typical.phases : undefined;
  const typicalMs = phases ? phases.start + phases.code + phases.hub + phases.sync : 0;
  let synced = "never";
  if (sync.finishedAt) synced = `${ago(sync.finishedAt, Date.now())} ago`;
  else if (sync.state === "running") synced = "running";
  return (
    <>
      <div class="section">
        <span>Notifications</span>
      </div>
      <Notifications state={state} />

      <div class="section">
        <span>Devbox</span>
      </div>
      <div class="card">
        <div class="kv">
          <span>Status</span>
          <span>
            {idle.busy
              ? `Busy — ${idle.reasons.join(", ")}`
              : `Idle for ${duration(idle.idleSeconds)}`}
          </span>
        </div>
        {gateway?.idle?.limitMs ? (
          <div class="kv">
            <span>Sleeps after</span>
            <span>{Math.round(gateway.idle.limitMs / 60000)} min idle</span>
          </div>
        ) : null}
        <div class="kv">
          <span>Up for</span>
          <span>{duration(uptime)}</span>
        </div>
        {typicalMs > 0 && (
          <div class="kv">
            <span>Typical wake</span>
            <span>{(typicalMs / 1000).toFixed(1)}s</span>
          </div>
        )}
        {gateway?.lastStop && (
          <div class="kv">
            <span>Last slept</span>
            <span>{new Date(gateway.lastStop.at).toLocaleString()}</span>
          </div>
        )}
      </div>

      <div class="section">
        <span>Repo sync</span>
        <span>{synced}</span>
      </div>
      <div class="card">
        {sync.results.length === 0 && <p>No sync has run yet.</p>}
        {sync.results.map((result) => {
          const failed = result.status === "conflict" || result.status === "error";
          const tone = failed ? "error" : result.status === "updated" ? "idle" : "";
          return (
            <>
              <div class="kv">
                <span>{result.name}</span>
                <span class={`chip ${tone}`} title={result.detail}>
                  {result.status}
                </span>
              </div>
              {failed && result.detail && <p style="font-size:12.5px">{result.detail}</p>}
            </>
          );
        })}
        <div style="height:10px" />
        <button
          class="btn ghost block"
          disabled={sync.state === "running"}
          onClick={() => {
            api("/api/sync", { method: "POST" })
              .then(() => {
                toast("Syncing…");
              })
              .catch(report);
          }}
        >
          {sync.state === "running"
            ? `Syncing ${String(sync.done)}/${sync.total ? String(sync.total) : "…"}`
            : "Sync now"}
        </button>
      </div>

      <div class="section">
        <span>Agents</span>
      </div>
      <div class="card">
        {state.adapters.map((adapter) => (
          <div class="kv" key={adapter.id}>
            <span>{adapter.label}</span>
            <span>
              {adapter.available ? (
                "installed"
              ) : (
                <span style="color:var(--faint)">not installed</span>
              )}
            </span>
          </div>
        ))}
      </div>

      <div style="height:16px" />
      <button
        class="btn danger block"
        onClick={() => {
          api("/api/logout", { method: "POST" }).then(signedOut).catch(report);
        }}
      >
        Sign out
      </button>
      <p class="about">
        devbox {version}
        {deploy?.at ? ` · deployed ${ago(deploy.at, Date.now())} ago` : ""}
      </p>
    </>
  );
}
