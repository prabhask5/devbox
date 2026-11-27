import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { Page } from "@playwright/test";
import { expect } from "@playwright/test";

const root = join(import.meta.dirname, "../../..");

/** The data every app screenshot is made with (tests/fixtures/app/app.json). */
interface AppFixture {
  now: number;
  state: Record<string, unknown>;
  gateway: Record<string, unknown>;
  asleep: Record<string, unknown>;
  repos: unknown[];
  settings: unknown;
  frames: Record<string, Record<string, unknown>>;
}

const FIXTURE = JSON.parse(
  readFileSync(join(root, "tests/fixtures/app/app.json"), "utf8"),
) as AppFixture;

/** One screen with a screenshot: where it is, what the hub answers, and how to get it on screen. */
export interface Screen {
  name: string;
  hash: string;
  /** What GET /api/state answers: signed in, signed out, or the gateway for a sleeping devbox. */
  api: "state" | "login" | "asleep";
  ready: (page: Page) => Promise<void>;
}

export const SCREENS: readonly Screen[] = [
  {
    name: "signin",
    hash: "#/",
    api: "login",
    ready: async (page) => {
      await expect(page.getByRole("heading")).toHaveText("Agents");
      await expect(page.getByPlaceholder("Password")).toBeFocused();
    },
  },
  {
    name: "asleep",
    hash: "#/",
    api: "asleep",
    ready: async (page) => {
      await expect(page.getByRole("heading")).toHaveText("Devbox is asleep");
    },
  },
  {
    name: "sessions",
    hash: "#/",
    api: "state",
    ready: async (page) => {
      await expect(page.getByText("claude-commits")).toBeVisible();
      await expect(page.locator(".pill")).toHaveText("Sleeps in 10m");
    },
  },
  {
    name: "new-session",
    hash: "#/",
    api: "state",
    ready: async (page) => {
      await page.getByRole("button", { name: "New session" }).click();
      await expect(page.locator(".sheet h2")).toHaveText("New session");
      await expect(page.locator(".repo.on")).toHaveText(/detached/);
    },
  },
  {
    name: "needs-input",
    hash: "#/s/aaaa1111",
    api: "state",
    ready: async (page) => {
      await expect(page.locator(".screen pre")).toContainText("Do you want to proceed?");
      await expect(page.locator(".ask button")).toHaveCount(3);
    },
  },
  {
    name: "session-screen",
    hash: "#/s/bbbb2222",
    api: "state",
    ready: async (page) => {
      await expect(page.locator(".screen pre")).toContainText("bold magenta");
    },
  },
  {
    name: "session-external",
    hash: "#/s/eeee5555",
    api: "state",
    ready: async (page) => {
      await expect(page.locator(".screen .msg")).toContainText("runs outside tmux");
    },
  },
  {
    name: "settings",
    hash: "#/settings",
    api: "state",
    ready: async (page) => {
      await expect(page.locator("input[type=number]")).toHaveValue("30");
      await expect(page.locator(".pill")).toHaveText("Sleeps in 10m");
    },
  },
  {
    name: "assistant",
    hash: "#/assistant",
    api: "state",
    ready: async (page) => {
      await expect(page.getByRole("heading", { level: 2 })).toHaveText("Your assistant lives here");
    },
  },
];

/** How one implementation of the app is served and what shape of data it expects. */
export interface Served {
  /** The file at a path, or undefined for a 404. */
  file: (path: string) => { body: string | Buffer; contentType: string } | undefined;
  state: (state: Record<string, unknown>) => unknown;
  frame: (frame: Record<string, unknown>) => unknown;
  asleep: (status: Record<string, unknown>) => unknown;
}

/**
 * Shows a screen of the app at a made-up origin: its files from `served`, the hub's and the
 * gateway's answers from the fixture, the clock stopped, so the same screen is the same pixels.
 */
export async function show(page: Page, served: Served, screen: Screen) {
  await page.clock.setFixedTime(new Date(FIXTURE.now));
  const stream = (value: unknown) => ({
    contentType: "text/event-stream",
    body: `data: ${JSON.stringify(value)}\n\n`,
  });
  const json = (value: unknown, status = 200) => ({
    status,
    contentType: "application/json",
    body: JSON.stringify(value),
  });
  await page.route("https://devbox.test/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const session = /^\/api\/sessions\/([a-f0-9]+)\/screen$/.exec(path);
    if (path === "/api/state") {
      if (screen.api === "login") await route.fulfill(json({ error: "login required" }, 401));
      else if (screen.api === "asleep")
        await route.fulfill(json(served.asleep(FIXTURE.asleep), 503));
      else await route.fulfill(json(served.state(FIXTURE.state)));
    } else if (path === "/api/stream") {
      await route.fulfill(stream(served.state(FIXTURE.state)));
    } else if (session) {
      await route.fulfill(stream(served.frame(FIXTURE.frames[session[1] ?? ""] ?? {})));
    } else if (path === "/api/repos") {
      await route.fulfill(json({ codeDir: "/home/dev/code", repos: FIXTURE.repos }));
    } else if (path === "/api/settings") {
      await route.fulfill(json(FIXTURE.settings));
    } else if (path === "/__devbox/status") {
      await route.fulfill(json(FIXTURE.gateway));
    } else {
      const file = served.file(path);
      if (file) await route.fulfill(file);
      else await route.fulfill({ status: 404, body: "" });
    }
  });
  await page.goto(`https://devbox.test/${screen.hash}`);
  await screen.ready(page);
}

export function readFile(path: string): Buffer {
  return readFileSync(join(root, path));
}
