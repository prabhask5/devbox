import { defineConfig, devices } from "@playwright/test";

const ci = process.env.CI !== undefined;

// One stack (tests/e2e/harness) serves every test and tests steer it, so they run one at a time.
export default defineConfig({
  testDir: ".",
  outputDir: "test-results",
  fullyParallel: false,
  workers: 1,
  forbidOnly: ci,
  retries: 0,
  reporter: ci ? [["list"], ["html", { open: "never" }]] : "list",
  snapshotPathTemplate: "{testDir}/visual/{arg}-{projectName}-{platform}{ext}",
  expect: {
    toHaveScreenshot: { maxDiffPixelRatio: 0.02, animations: "disabled", scale: "device" },
  },
  use: {
    ignoreHTTPSErrors: true, // the harness's TLS front has a self-signed certificate
    locale: "en-US",
    timezoneId: "UTC",
    trace: "retain-on-failure",
  },
  projects: [
    // The primary device: iPhone Safari. The stack-level scenarios need no browser, so they run once.
    {
      name: "webkit-iphone",
      use: { ...devices["iPhone 14"] },
      testIgnore: /(health|idle)\.spec\.ts/,
    },
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        // Chromium registers a service worker only from an origin whose certificate it trusts.
        launchOptions: { args: ["--ignore-certificate-errors"] },
      },
    },
  ],
  webServer: {
    command: "go run ./tests/e2e/harness",
    cwd: "../..",
    url: "http://127.0.0.1:47100/ready",
    reuseExistingServer: !ci,
    timeout: 180_000,
    stdout: "ignore",
    stderr: "pipe",
  },
});
