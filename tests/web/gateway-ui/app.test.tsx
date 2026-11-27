// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/preact";
import { afterEach, expect, test, vi } from "vitest";
import { App, followup } from "@devbox/gateway-ui/App.tsx";
import { fixture } from "./fixtures.ts";

afterEach(cleanup);

function show(name: string, handlers: { onSkip?: () => void; onRetry?: () => void } = {}) {
  const { boot, status, now } = fixture(name);
  const view = render(
    <App
      boot={boot}
      status={status}
      now={now ?? 0}
      next="/workspace?folder=/home/dev/code#top"
      onSkip={handlers.onSkip ?? (() => undefined)}
      onRetry={handlers.onRetry ?? (() => undefined)}
    />,
  );
  return { ...view, then: followup(boot, status) };
}

const text = (selector: string) =>
  [...document.querySelectorAll(selector)].map((el) => el.textContent.trim());
const dot = () => document.querySelector(".dot")?.className;

test("asleep: asks for the wake password and returns to the page asked for", () => {
  const { then } = show("asleep-signin");
  expect(screen.getByRole("heading").textContent).toBe("Devbox is asleep");
  expect(text(".sub")).toEqual([
    "Enter your wake password to start it. This device will be remembered.",
  ]);
  expect(text(".brand")).toEqual(["code.example.com"]);

  const form = document.querySelector("form");
  expect(form?.method).toBe("post");
  expect(form?.getAttribute("action")).toBe("/__devbox/login");
  const password = screen.getByLabelText<HTMLInputElement>("Wake password");
  expect(password.type).toBe("password");
  expect(password.name).toBe("password");
  expect(password.required).toBe(true);
  expect(password.getAttribute("autocomplete")).toBe("current-password");
  expect(document.querySelector<HTMLInputElement>("input[name=next]")?.value).toBe(
    "/workspace?folder=/home/dev/code#top",
  );
  expect(screen.getByRole("button").textContent).toBe("Wake");
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.queryByRole("progressbar")).toBeNull();
  expect(then).toBe("none");
});

test("asleep: shows why signing in failed", () => {
  show("signin-error");
  expect(screen.getByRole("alert").textContent).toBe("Wrong password.");
});

test("waking: title by kind, typical duration, progress and the four phases", () => {
  const { then } = show("waking");
  expect(screen.getByRole("heading").textContent).toBe("Waking your devbox");
  expect(text(".sub")).toEqual(["Usually takes about 3.2s (median of last 7)"]);
  expect(dot()).toBe("dot live");
  expect(text(".meta span")).toEqual(["1.9s", "about 1.3s left"]);
  expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("59");
  expect(text("li .lbl")).toEqual([
    "Starting the VMRestoring saved memory — your sessions pick up where they left off",
    "Waiting for code-server",
    "Waiting for the agents hub",
    "Syncing repos from GitHub",
  ]);
  expect([...document.querySelectorAll("li")].map((li) => li.className)).toEqual([
    "active",
    "pending",
    "pending",
    "pending",
  ]);
  expect(text("li .t")).toEqual(["1.9s / ~2.4s", "~0.3s", "~0.2s", "~0.3s"]);
  expect(screen.queryByText("Open now")).toBeNull();
  expect(text("footer")[0]).toMatch(/^Went to sleep .+ \(idle 15m\)$/);
  expect(text("pre")).toEqual(["Restoring the VM from its saved memory"]);
  expect(then).toBe("none");
});

test("syncing: repo progress, the current repo and Open now", () => {
  const onSkip = vi.fn();
  show("syncing", { onSkip });
  expect(text(".meta span")).toEqual(["6.8s", "about 0.8s left"]);
  expect([...document.querySelectorAll("li")].map((li) => li.className)).toEqual([
    "done",
    "done",
    "done",
    "active",
  ]);
  expect(text("li .t")).toEqual(["2.5s", "2.4s", "1.2s", "0.6s / ~1.4s"]);
  expect(text("li small").at(-1)).toBe("0 of 3 repos · stellar");
  expect(text(".chip")).toEqual(["stellar…"]);
  screen.getByText("Open now").click();
  expect(onSkip).toHaveBeenCalledOnce();
});

test("syncing: every result is a chip marked by its outcome", () => {
  show("syncing-results");
  expect(text(".chip")).toEqual([
    "dotfiles",
    "devbox ↓",
    "website",
    "moi ⚠",
    "old-api ✕",
    "notes…",
  ]);
  expect([...document.querySelectorAll(".chip")].map((chip) => chip.className)).toEqual([
    "chip current",
    "chip updated",
    "chip cloned",
    "chip conflict",
    "chip error",
    "chip syncing",
  ]);
  expect(document.querySelector(".chip.conflict")?.getAttribute("title")).toContain(
    "left exactly as it was",
  );
  expect(text(".meta span")[1]).toBe("taking a bit longer…");
});

test("cold boot without history: says it is learning, for the agents app", () => {
  show("cold-first");
  expect(screen.getByRole("heading").textContent).toBe("Booting your devbox");
  expect(text(".sub")).toEqual(["First wake — learning how long this takes"]);
  expect(text(".brand")).toEqual(["agents.example.com"]);
  expect(text("li small")).toEqual(["Fresh boot"]);
  expect(document.querySelector("footer")).toBeNull();
});

test("a skipped phase is struck through", () => {
  show("hub-skipped");
  expect([...document.querySelectorAll("li")].map((li) => li.className)).toEqual([
    "done",
    "done",
    "skipped",
    "active",
  ]);
  expect(text("li .t")[2]).toBe("skipped");
});

test("ready: says what opens and reloads into it", () => {
  const { then } = show("ready");
  expect(screen.getByRole("heading").textContent).toBe("Ready");
  expect(text(".sub")).toEqual(["Opening VS Code…"]);
  expect(dot()).toBe("dot ok");
  expect(text(".meta span")).toEqual(["3.5s", "done"]);
  expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("100");
  expect(then).toBe("open");
});

test("error: the reason and Try again", () => {
  const onRetry = vi.fn();
  const { then } = show("error", { onRetry });
  expect(screen.getByRole("heading").textContent).toBe("Couldn't wake the devbox");
  expect(text(".sub")).toEqual(["code-server did not come up within 3m0s"]);
  expect(dot()).toBe("dot bad");
  expect(text(".meta span")).toEqual(["183s", ""]);
  screen.getByText("Try again").click();
  expect(onRetry).toHaveBeenCalledOnce();
  expect(then).toBe("none");
});

test("stopping: it will wake right back up", () => {
  const { then } = show("stopping");
  expect(screen.getByRole("heading").textContent).toBe("Going to sleep…");
  expect(text(".sub")).toEqual(["It will wake right back up after."]);
  expect(screen.queryByRole("progressbar")).toBeNull();
  expect(then).toBe("none");
});

test("reconnecting: names the service that is not answering and retries", () => {
  const { then } = show("reconnecting");
  expect(screen.getByRole("heading").textContent).toBe("Reconnecting…");
  expect(text(".sub")).toEqual(["The devbox is up but code-server isn't answering yet."]);
  expect(then).toBe("reconnect");
});

test("unconfigured: how to set it up, and nothing to sign in to", () => {
  const { then } = show("unconfigured");
  expect(screen.getByRole("heading").textContent).toBe("devbox is not set up yet");
  expect(text(".sub")).toEqual(["Run devbox up on the host to finish setting it up."]);
  expect(document.querySelector("form")).toBeNull();
  expect(then).toBe("none");
});

test("status changes are announced to screen readers", () => {
  show("waking");
  const live = document.querySelector("[aria-live=polite]");
  expect(live?.querySelector("h1")?.textContent).toBe("Waking your devbox");
  expect(live?.querySelector(".sub")).not.toBeNull();
});

test("before the first event it says it is connecting", () => {
  const { boot } = fixture("waking");
  render(
    <App
      boot={boot}
      status={undefined}
      now={0}
      next="/"
      onSkip={() => undefined}
      onRetry={() => undefined}
    />,
  );
  expect(screen.getByRole("heading").textContent).toBe("Connecting…");
});
