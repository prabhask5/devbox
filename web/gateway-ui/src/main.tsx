import { render } from "preact";
import type { GatewayStatus } from "@devbox/shared/proto/types";
import { App, followup } from "./App.tsx";
import type { Boot } from "./App.tsx";
import "./styles.css";

const boot = JSON.parse(document.getElementById("devbox-boot")?.textContent ?? "{}") as Boot;
const root = document.getElementById("app");
const next = location.pathname + location.search + location.hash;
document.title = `${boot.app === "agents" ? "Agents" : "Code"} · devbox`;

let status: GatewayStatus | undefined;
let clockOffset = 0; // the gateway's clock minus this device's
let leaving = false;

function reloadIn(ms: number) {
  if (leaving) return;
  leaving = true;
  setTimeout(() => {
    location.reload();
  }, ms);
}

function skip() {
  void fetch("/__devbox/skip", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  }).then(() => {
    location.reload();
  });
}

function draw() {
  if (!root) return;
  render(
    <App
      boot={boot}
      status={status}
      now={Date.now() + clockOffset}
      next={next}
      onSkip={skip}
      onRetry={() => {
        location.reload();
      }}
    />,
    root,
  );
  const then = followup(boot, status);
  if (then === "open") reloadIn(350);
  if (then === "reconnect") reloadIn(3000);
}

function connect() {
  const events = new EventSource("/__devbox/events");
  events.onmessage = (event: MessageEvent<string>) => {
    status = JSON.parse(event.data) as GatewayStatus;
    clockOffset = status.serverNow - Date.now();
    draw();
  };
  events.onerror = () => {
    events.close();
    setTimeout(connect, 2000);
  };
}

draw();
if (!boot.needsAuth && !boot.unconfigured) {
  connect();
  setInterval(draw, 250);
}
