import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { AGENTS, CODE, WAKE_PASSWORD, incus, reset, set } from "./stack.ts";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function signIn(page: Page, password: string) {
  await page.getByLabel("Wake password").fill(password);
  await page.getByRole("button", { name: "Wake" }).click();
}

// Scenario 1 (docs/spec/15-testing.md): asleep → visit the editor → password → phases → the page.
test("an asleep devbox wakes on sign-in and opens the page that was asked for", async ({
  page,
  request,
}) => {
  // The boot screen talks to its own origin's /__devbox/ and to nothing else.
  const elsewhere: string[] = [];
  let booting = true;
  page.on("request", (sent) => {
    const url = new URL(sent.url());
    const own =
      url.origin === CODE && (url.pathname.startsWith("/__devbox/") || sent.isNavigationRequest());
    if (booting && !own) elsewhere.push(sent.url());
  });

  await page.goto(`${CODE}/workspace?folder=/home/dev/code`);
  await expect(page.getByRole("heading")).toHaveText("Devbox is asleep");
  await expect(page).toHaveTitle("Code · devbox");
  await expect(page.getByLabel("Wake password")).toBeFocused();
  expect((await incus(request)).stateChanges).toBeNull();

  // A wrong password says so and wakes nothing; the page to return to is not forgotten.
  await page.getByLabel("Wake password").fill("FAKE-wrong");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toHaveText("Wrong password.");
  expect((await incus(request)).stateChanges).toBeNull();

  await signIn(page, WAKE_PASSWORD);
  await expect(page.getByRole("heading")).toHaveText("Waking your devbox");
  await expect(page.getByRole("progressbar")).toBeVisible();
  await expect(page.locator("li.active .lbl")).toContainText("Starting the VM");
  await expect(page.locator("li.active small")).toHaveText(
    "Restoring saved memory — your sessions pick up where they left off",
  );
  await expect(page.locator("li")).toHaveCount(4);
  // The phases advance, and the page reloads into the editor when the wake is done.
  await expect(page.locator("li.done").first()).toContainText("Starting the VM");
  await expect(page.getByRole("heading")).toHaveText("code-server", { timeout: 20_000 });
  booting = false;
  await expect(page.locator("#url")).toHaveText("/workspace?folder=/home/dev/code");
  await expect(page.locator("#host")).toHaveText("code.example.com");

  const state = await incus(request);
  expect(state.status).toBe("Running");
  expect(state.stateChanges).toEqual([{ action: "start", timeout: -1 }]);
  expect(elsewhere).toEqual([]);

  // Signed in and awake: the next visit goes straight through.
  await page.goto(`${CODE}/another`);
  await expect(page.locator("#url")).toHaveText("/another");
});

test("Open now during the repo sync opens the editor at once", async ({ page, request }) => {
  await set(request, { sync: "hold" });
  await page.goto(`${CODE}/`);
  await signIn(page, WAKE_PASSWORD);

  await expect(page.locator("li.active .lbl")).toContainText("Syncing repos from GitHub", {
    timeout: 20_000,
  });
  await expect(page.locator("li.active small")).toHaveText("2 of 3 repos · devbox");
  await expect(page.locator(".chip")).toHaveText(["dotfiles", "notes ↓", "devbox…"]);
  await expect(page.locator("li.done")).toHaveCount(3);

  await page.getByRole("button", { name: "Open now" }).click();
  await expect(page.getByRole("heading")).toHaveText("code-server");
  // The wake is still running: only this browser skipped the wait.
  const status = (await (await request.get(`${CODE}/__devbox/status`)).json()) as { phase: string };
  expect(status.phase).toBe("waking");

  await set(request, { sync: "release" });
  await expect
    .poll(
      async () =>
        ((await (await request.get(`${CODE}/__devbox/status`)).json()) as { phase: string }).phase,
    )
    .toBe("idle");
});

test("the agents host gets its own boot screen and its own app", async ({ page }) => {
  await page.goto(`${AGENTS}/`);
  await expect(page).toHaveTitle("Agents · devbox");
  await expect(page.locator(".brand")).toHaveText("agents.example.com");
  await signIn(page, WAKE_PASSWORD);
  await expect(page.getByRole("heading")).toHaveText("agents app", { timeout: 20_000 });
});

test("a failed wake says why and Try again retries", async ({ page, request }) => {
  await set(request, { start: "fail" });
  await page.goto(`${CODE}/`);
  await signIn(page, WAKE_PASSWORD);
  await expect(page.getByRole("heading")).toHaveText("Couldn't wake the devbox", {
    timeout: 20_000,
  });
  await expect(page.locator(".sub")).toHaveText('incus: Failed to start device "eth0"');

  await set(request, { start: "ok" });
  await page.getByRole("button", { name: "Try again" }).click();
  await expect(page.getByRole("heading")).toHaveText("code-server", { timeout: 20_000 });
});

test("the sign-in form works from the keyboard alone", async ({ page }) => {
  await page.goto(`${CODE}/`);
  await page.keyboard.type(WAKE_PASSWORD); // the password field has the focus
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading")).toHaveText("Waking your devbox");
  const live = page.locator("[aria-live=polite]");
  await expect(live.getByRole("heading")).toBeVisible(); // state changes are announced
});
