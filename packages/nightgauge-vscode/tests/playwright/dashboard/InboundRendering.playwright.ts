/**
 * Playwright tests for the INBOUND half of the webview contract (Issue #751).
 *
 * Every pre-existing dashboard Playwright test drives the OUTBOUND half only:
 * click a control, assert a vscode.postMessage() call was recorded. Nothing
 * drove a message *into* the webview the way the real extension host does —
 * `panel.webview.postMessage(data)` fires a `message` DOM event on the
 * webview's `window`, and only that event runs a page's
 * `window.addEventListener("message", ...)` handlers.
 *
 * dispatchInboundMessage() (tests/playwright/helpers/webview-loader.ts)
 * reproduces exactly that event against REAL rendered dashboard HTML (from
 * scripts/generate-dashboard-html.ts), so these tests exercise the same
 * handlers the extension host drives in production:
 *   - DashboardHtml.ts's top-level listener: incrementalUpdate,
 *     metricsRefreshing, restoreScrollPosition, runDetailLiveUpdate.
 *   - AuditTabHtml.ts's SSE listener: streamStatusChanged, auditLiveEvent.
 *
 * Prerequisite: `npx tsx scripts/generate-dashboard-html.ts` (or
 * `npm run test:e2e`, which chains it) must have run first.
 */

import { test, expect } from "@playwright/test";
import { loadWebviewFromFile, dispatchInboundMessage } from "../helpers/webview-loader.js";
import { loadTabFixture } from "../helpers/dashboard-fixtures.js";

const DEFAULT_HTML_PATH = "/tmp/dashboard-test.html";

test.describe("Inbound driver — DashboardHtml.ts top-level message listener", () => {
  test("incrementalUpdate replaces a valid section's innerHTML", async ({ page }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH);

    await expect(page.locator("#section-summary-cards")).not.toContainText("Inbound driver marker");

    await dispatchInboundMessage(page, {
      type: "incrementalUpdate",
      section: "summary-cards",
      html: "<p>Inbound driver marker</p>",
    });

    // Debounced 50ms flush (DashboardHtml.ts _flushIncrementalUpdates)
    await expect(page.locator("#section-summary-cards")).toContainText("Inbound driver marker", {
      timeout: 2000,
    });
  });

  test("incrementalUpdate ignores an unlisted section (VALID_SECTIONS allowlist)", async ({
    page,
  }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH);
    const before = await page.locator("#section-summary-cards").innerHTML();

    await dispatchInboundMessage(page, {
      type: "incrementalUpdate",
      section: "not-a-real-section",
      html: "<p>Should never render</p>",
    });
    await page.waitForTimeout(200);

    const after = await page.locator("#section-summary-cards").innerHTML();
    expect(after).toBe(before);
    await expect(page.locator("body")).not.toContainText("Should never render");
  });

  test("metricsRefreshing toggles the .refreshing class on widget containers", async ({ page }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH);
    const widget = page.locator(".health-widget").first();
    await expect(widget).not.toHaveClass(/refreshing/);

    await dispatchInboundMessage(page, { type: "metricsRefreshing", active: true });
    await expect(widget).toHaveClass(/refreshing/);

    await dispatchInboundMessage(page, { type: "metricsRefreshing", active: false });
    await expect(widget).not.toHaveClass(/refreshing/);
  });

  test("restoreScrollPosition scrolls the window to the given offset", async ({ page }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH);
    await page.evaluate(() => {
      document.body.style.height = "5000px";
    });

    await dispatchInboundMessage(page, { type: "restoreScrollPosition", scrollY: 640 });
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBe(640);
  });

  test("runDetailLiveUpdate patches a specific stage row's status and duration", async ({
    page,
  }) => {
    // runs--populated seeds entry #701 with a real feature-dev stage row
    // (data-stage-name), specifically so this test has a live DOM target —
    // see scripts/generate-dashboard-html.ts's runs701Stages.
    await loadTabFixture(page, "runs--populated");

    const stageRow = page.locator('[data-run-detail-issue="701"] [data-stage-name="feature-dev"]');
    await expect(stageRow).toHaveAttribute("data-stage-status", "completed");

    await dispatchInboundMessage(page, {
      type: "runDetailLiveUpdate",
      issueNumber: 701,
      update: { stage: "feature-dev", status: "running", durationMs: 1234 },
    });

    await expect(stageRow).toHaveAttribute("data-stage-status", "running");
    await expect(stageRow.locator("[data-stage-duration]")).toHaveText("1.2s");
  });

  test("runDetailLiveUpdate with allComplete flips every running row to completed", async ({
    page,
  }) => {
    await loadTabFixture(page, "runs--populated");
    const stageRow = page.locator('[data-run-detail-issue="701"] [data-stage-name="feature-dev"]');

    await dispatchInboundMessage(page, {
      type: "runDetailLiveUpdate",
      issueNumber: 701,
      update: { stage: "feature-dev", status: "running" },
    });
    await expect(stageRow).toHaveAttribute("data-stage-status", "running");

    await dispatchInboundMessage(page, {
      type: "runDetailLiveUpdate",
      issueNumber: 701,
      update: { allComplete: true },
    });
    await expect(stageRow).toHaveAttribute("data-stage-status", "completed");
  });

  test("runDetailLiveUpdate for an unknown issue number is a silent no-op", async ({ page }) => {
    await loadTabFixture(page, "runs--populated");
    const stageRow = page.locator('[data-run-detail-issue="701"] [data-stage-name="feature-dev"]');

    await dispatchInboundMessage(page, {
      type: "runDetailLiveUpdate",
      issueNumber: 999999,
      update: { stage: "feature-dev", status: "running" },
    });
    await page.waitForTimeout(100);

    // Nothing threw, and the real row is untouched.
    await expect(stageRow).toHaveAttribute("data-stage-status", "completed");
  });
});

test.describe("Inbound driver — AuditTabHtml.ts real-time SSE listener (#3321)", () => {
  test("streamStatusChanged updates the connection badge class and label", async ({ page }) => {
    await loadTabFixture(page, "audit--populated");
    const badge = page.locator("#stream-status-badge");
    await expect(badge).toHaveClass(/stream-badge--disconnected/);

    await dispatchInboundMessage(page, {
      type: "streamStatusChanged",
      status: "connected",
      label: "● live",
    });
    await expect(badge).toHaveClass(/stream-badge--connected/);
    await expect(badge).toHaveText("● live");

    await dispatchInboundMessage(page, { type: "streamStatusChanged", status: "reconnecting" });
    await expect(badge).toHaveClass(/stream-badge--reconnecting/);
    await expect(badge).toHaveText("↻ reconnecting");
  });

  test("auditLiveEvent prepends a live row to the audit entries list", async ({ page }) => {
    await loadTabFixture(page, "audit--populated");
    const list = page.locator("#audit-live-entries-list");
    await expect(list.locator(".audit-live-row")).toHaveCount(0);

    await dispatchInboundMessage(page, {
      type: "auditLiveEvent",
      entry: {
        timestamp: new Date().toISOString(),
        action: "pipeline.run",
        userEmail: "operator@example.com",
        status: "success",
        resourceType: "issue",
        resourceId: "701",
      },
    });

    await expect(list.locator(".audit-live-row")).toHaveCount(1);
    await expect(list.locator(".audit-live-row").first()).toContainText("pipeline.run");
    await expect(list.locator(".audit-live-row").first()).toContainText("operator@example.com");
  });

  test("auditLiveEvent caps live rows at 50 (trims the oldest)", async ({ page }) => {
    await loadTabFixture(page, "audit--populated");

    for (let i = 0; i < 52; i++) {
      await dispatchInboundMessage(page, {
        type: "auditLiveEvent",
        entry: {
          timestamp: new Date().toISOString(),
          action: `pipeline.run.${i}`,
          userEmail: "operator@example.com",
          status: "success",
        },
      });
    }

    await expect(page.locator("#audit-live-entries-list .audit-live-row")).toHaveCount(50);
    // Most recent (last dispatched) is prepended to the top.
    await expect(page.locator("#audit-live-entries-list .audit-live-row").first()).toContainText(
      "pipeline.run.51"
    );
  });
});
