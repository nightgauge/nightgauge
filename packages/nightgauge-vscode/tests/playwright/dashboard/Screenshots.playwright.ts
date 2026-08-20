/**
 * Screenshot capture per tab per state, published as CI artifacts (Issue #751
 * acceptance criterion). Iterates every fixture scripts/generate-dashboard-html.ts
 * wrote to /tmp/dashboard-fixtures/ — the full tab/state matrix (populated,
 * empty, loading, and each PlatformFailureKind) plus the 13 tab-activation
 * fixtures — so a visual regression across any of them is reviewable without
 * re-running the suite locally.
 *
 * Screenshots land in test-results/dashboard-screenshots/, inside
 * playwright.config.ts's outputDir; .github/workflows/ci.yml's playwright job
 * uploads that whole directory unconditionally (not only on failure, unlike
 * the existing failure-only screenshot/trace/video upload — this is a
 * separate, deliberate capture, not Playwright's own failure artifacts).
 *
 * Prerequisite: `npx tsx scripts/generate-dashboard-html.ts` (or
 * `npm run test:e2e`) must have run first.
 */

import { existsSync, mkdirSync, readdirSync } from "fs";
import { join } from "path";
import { test, expect } from "@playwright/test";
import { loadTabFixture, FIXTURES_DIR } from "../helpers/dashboard-fixtures.js";

const SCREENSHOT_DIR = join("test-results", "dashboard-screenshots");
mkdirSync(SCREENSHOT_DIR, { recursive: true });

const fixtureNames = existsSync(FIXTURES_DIR)
  ? readdirSync(FIXTURES_DIR)
      .filter((f) => f.endsWith(".html"))
      .map((f) => f.replace(/\.html$/, ""))
      .sort()
  : [];

test.describe("Dashboard fixture screenshots", () => {
  test("fixture matrix exists (run scripts/generate-dashboard-html.ts first)", () => {
    expect(
      fixtureNames.length,
      `No fixtures found in ${FIXTURES_DIR} — run ` +
        "'npx tsx scripts/generate-dashboard-html.ts' (or 'npm run test:e2e', " +
        "which chains it) before this suite."
    ).toBeGreaterThan(0);
  });

  for (const name of fixtureNames) {
    test(`screenshot: ${name}`, async ({ page }) => {
      await loadTabFixture(page, name);
      await page.screenshot({ path: join(SCREENSHOT_DIR, `${name}.png`), fullPage: true });
    });
  }
});
