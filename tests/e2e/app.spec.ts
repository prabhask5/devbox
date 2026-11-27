import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import {
  AGENTS,
  WAKE_PASSWORD,
  incus,
  pushDevice,
  pushes,
  reset,
  sessions,
  set,
  typeInto,
} from "./stack.ts";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

/** Wakes the devbox through the boot screen and signs in to the app (the harness uses one password for both). */
async function open(page: Page) {
  await page.goto(`${AGENTS}/`);
  await page.getByLabel("Wake password").fill(WAKE_PASSWORD);
  await page.getByRole("button", { name: "Wake" }).click();
  await expect(page.getByText("Sign in to your devbox.")).toBeVisible({ timeout: 20_000 });
  await expect(page.getByLabel("Password")).toBeFocused();
  await page.getByLabel("Password").fill("FAKE-wrong");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toHaveText("Wrong password.");
  await page.getByLabel("Password").fill(WAKE_PASSWORD);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Agents");
}

// Scenario 2 (docs/spec/15-testing.md): sign in, start a session, the agent asks for permission →
// a push arrives → its link opens the session → Yes → the agent goes on → it stops → a second push.
test("an agent asks, the phone is told, one tap answers, and the end of the turn is announced", async ({
  page,
  request,
}) => {
  await open(page);
  await expect(page.getByText("No sessions yet")).toBeVisible();

  // Announce every finished turn that took a second, and register this "phone" for pushes. A
  // browser under test cannot subscribe at a real push service, so the subscription comes from
  // the fake one and goes to the hub the way the app sends it.
  await page.getByRole("button", { name: "Settings" }).click();
  const threshold = page.getByLabel("Notify when a turn finishes after (seconds)");
  await expect(threshold).toHaveValue("30");
  await threshold.fill("1");
  await threshold.blur();
  await expect(page.getByRole("status")).toHaveText("Saved");
  const subscription = await pushDevice(request, "iPhone");
  const subscribed = await page.evaluate(
    async (body) => {
      const response = await fetch("/api/push/subscribe", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      return response.status;
    },
    { subscription, device: "iPhone" },
  );
  expect(subscribed).toBe(200);

  // A new Claude session in a repo, with a first prompt.
  await page.getByRole("button", { name: "Sessions" }).click();
  await page.getByRole("button", { name: "New session" }).click();
  const sheet = page.getByRole("dialog", { name: "New session" });
  await expect(sheet.getByRole("button", { name: "Claude Code" })).toHaveClass("on");
  await sheet.getByRole("button", { name: "devbox" }).click();
  await sheet.getByLabel("First prompt").fill("Fix the flaky sync test");
  await sheet.getByRole("button", { name: "Start" }).click();
  await expect(page.locator(".sess-title b")).toHaveText("claude-devbox");
  await expect(page.locator(".sess-title span")).toHaveText("Working · devbox");
  await expect(page.locator(".screen pre")).toContainText("fake claude");
  const [session] = await sessions(request);
  if (!session) throw new Error("the hub has no session");
  expect(page.url()).toBe(`${AGENTS}/#/s/${session.id}`);

  // Back at the list, the agent asks for permission: the list shows it live, and after the
  // delay the phone gets a push.
  await page.getByRole("button", { name: "Back" }).click();
  await expect(page.locator(".item .chip").first()).toHaveText("Working");
  await expect.poll(async () => (await sessions(request))[0]?.viewers).toBe(0);
  await typeInto(request, session.id, "ask");
  await expect(page.locator(".section").first()).toHaveText("Needs you1");
  await expect(page.locator(".item.needs .sub")).not.toBeEmpty();
  await expect.poll(async () => (await pushes(request)).deliveries.length).toBe(1);
  const asked = (await pushes(request)).deliveries[0];
  expect(asked?.urgency).toBe("high");
  expect(asked?.payload.title).toBe("🔐 claude-devbox — Fix the flaky sync test");
  expect(asked?.payload.url).toBe(`/#/s/${session.id}`);
  expect(asked?.payload.badge).toBe(1);

  // A tap on the notification opens the session, where the answer is one tap away.
  await page.goto(`${AGENTS}${asked?.payload.url ?? ""}`);
  const banner = page.locator(".ask");
  await expect(banner.locator("p")).toContainText("🔐");
  await expect(banner.getByRole("button")).toHaveText(["Yes", "Always", "No"]);
  await banner.getByRole("button", { name: "Yes" }).click();
  await expect(page.locator(".screen pre")).toContainText("allowed");
  await expect(banner).toHaveCount(0);
  await expect(page.locator(".sess-title span")).toHaveText("Working · devbox");

  // The turn ends while nobody watches: the second push.
  await page.getByRole("button", { name: "Back" }).click();
  await expect.poll(async () => (await sessions(request))[0]?.viewers).toBe(0);
  await typeInto(request, session.id, "stop");
  await expect(page.locator(".section").first()).toHaveText("Idle1");
  await expect.poll(async () => (await pushes(request)).deliveries.length).toBe(2);
  const finished = (await pushes(request)).deliveries[1];
  expect(finished?.payload.title).toBe("✅ claude-devbox — Fix the flaky sync test");
  expect(finished?.urgency).toBe("normal");
  expect((await pushes(request)).rejected ?? []).toEqual([]);
  await expect(page.locator(".unread")).toBeVisible();
});

test("typing, quick keys, renaming and killing a session", async ({ page, request }) => {
  await open(page);
  await page.getByRole("button", { name: "New session" }).click();
  const sheet = page.getByRole("dialog", { name: "New session" });
  // Agents that are not installed cannot be chosen.
  await expect(sheet.getByRole("button", { name: "Assistant" })).toHaveCount(0);
  await sheet.getByRole("button", { name: "Shell" }).click();
  await sheet.getByRole("button", { name: "Home (~)" }).click();
  await sheet.getByRole("button", { name: "Start" }).click();
  await expect(page.locator(".sess-title b")).toHaveText("shell-home");

  // The composer sends a line on Enter; Shift+Enter is a line break in the message.
  const composer = page.getByLabel("Message");
  await composer.fill("echo typed-$((6*7))");
  await composer.press("Enter");
  await expect(page.locator(".screen pre")).toContainText("typed-42");
  await expect(composer).toHaveValue("");
  await composer.fill("printf '\\033[1;32mgreen\\033[0m\\n'");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.locator(".screen pre span", { hasText: /^green$/ })).toHaveCSS(
    "font-weight",
    "700",
  );
  // A quick key reaches the terminal: Ctrl-C abandons the line being typed.
  await composer.fill("sleep 600");
  await composer.press("Enter");
  await page.locator(".keys").getByRole("button", { name: "^C" }).click();
  await composer.fill("echo after-$((1+1))");
  await composer.press("Enter");
  await expect(page.locator(".screen pre")).toContainText("after-2");

  page.once("dialog", (dialog) => void dialog.accept("scratchpad"));
  await page.getByRole("button", { name: "More" }).click();
  await page.getByRole("menu").getByRole("button", { name: "Rename" }).click();
  await expect(page.locator(".sess-title b")).toHaveText("scratchpad");
  expect((await sessions(request))[0]?.name).toBe("scratchpad");

  page.once("dialog", (dialog) => {
    expect(dialog.message()).toContain('Kill "scratchpad"?');
    void dialog.accept();
  });
  await page.getByRole("button", { name: "More" }).click();
  await page.getByRole("menu").getByRole("button", { name: "Kill session" }).click();
  await expect(page.getByText("No sessions yet")).toBeVisible();
  expect(await sessions(request)).toEqual([]);
});

test("the open app notices the devbox going to sleep and wakes it", async ({ page, request }) => {
  await open(page);
  await set(request, { idle: "1s" });
  await expect(page.getByRole("heading")).toHaveText("Devbox is asleep", { timeout: 30_000 });
  expect((await incus(request)).status).toBe("Stopped");
  await expect(page.getByText("Your sessions are saved.")).toBeVisible();

  await set(request, { idle: "15m" });
  await page.getByRole("button", { name: "Wake devbox" }).click();
  await expect(page.getByRole("progressbar")).toBeVisible();
  // The wake ends in a reload, into the app, still signed in.
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Agents", { timeout: 30_000 });
  expect((await incus(request)).status).toBe("Running");
});

test("the app's shell opens when nothing answers", async ({ page, context }) => {
  await open(page);
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  await context.setOffline(true);
  await page.reload();
  await expect(page.getByRole("heading")).toHaveText("Can't reach the devbox");
  await context.setOffline(false);
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Agents");
});

test("a new version of the app says so once it has taken over", async ({ page, request }) => {
  await open(page);
  await page.evaluate(async () => {
    await navigator.serviceWorker.ready;
  });
  // Installing the app for the first time is not an update.
  await expect(page.getByText("Updated — tap to reload")).toHaveCount(0);

  // The next deploy of the hub: the browser finds another service worker.
  await set(request, { app: "next" });
  await page.evaluate(async () => {
    await (await navigator.serviceWorker.getRegistration())?.update();
  });
  const prompt = page.getByRole("button", { name: "Updated — tap to reload" });
  await expect(prompt).toBeVisible();
  await prompt.click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Agents");
  await expect(prompt).toHaveCount(0);
});
