import { defineConfig, devices } from "@playwright/test";

const isCI = !!process.env.CI;

export default defineConfig({
  // Include both legacy tests/playwright and new tests/e2e-playwright
  testDir: "./tests",
  testMatch: ["**/*.playwright.ts"],

  // Retry once on CI to absorb transient flakes; no retry locally for fast feedback
  retries: isCI ? 1 : 0,

  // Capture screenshots and traces on failure for debugging
  use: {
    browserName: "chromium",
    headless: true,
    viewport: { width: 1280, height: 720 },
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    trace: "retain-on-failure",
    // Per-action timeout (individual locator operations)
    actionTimeout: 10_000,
    // Navigation timeout
    navigationTimeout: 15_000,
  },

  // Per-test timeout: allow up to 60s for E2E tests with IPC interactions
  timeout: 60_000,

  // Expect timeout for assertions
  expect: {
    timeout: 10_000,
  },

  // Output directory for artifacts (screenshots, videos, traces)
  outputDir: "test-results",

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], headless: true },
    },
  ],

  // On CI: parallel workers; locally: default (half of CPU cores)
  workers: isCI ? 2 : undefined,
});
