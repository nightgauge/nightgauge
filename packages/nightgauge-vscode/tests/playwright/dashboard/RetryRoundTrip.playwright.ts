/**
 * Round-trip tests for retry buttons: click → outbound message posted →
 * simulated extension response → asserted DOM change (Issue #751). Retry
 * buttons are the priority case named in the issue — "a retry that renders
 * nothing observable is the defect that started this" — and #752, #775, and
 * #785 added or correctly ordered loading renders so every platform-tab retry
 * has a visible middle step before transport completion.
 *
 * Every platform-backed tab's refresh is a FULL webview re-render, not a
 * postMessage-driven DOM patch: Dashboard.ts renders loading immediately and
 * schedules final states through `updatePanel(trigger)`; both paths reassign
 * `panel.webview.html` — a real navigation, not a live DOM mutation.
 * `vscode.getState()`/`setState()` are the only thing that survives it (that
 * survival is exactly why DashboardHtml.ts's tab-restoration script exists).
 * So the "simulated response" step here is a second `loadTabFixture()` call
 * for the SAME tab's `--loading` fixture — a second real page load, faithful
 * to what a real webview.html reassignment actually does — rather than an
 * inbound `message` dispatch on the same page (that mechanism is exercised
 * separately in InboundRendering.playwright.ts, for the messages that really
 * are postMessage-driven: incrementalUpdate, the audit SSE feed, etc.).
 *
 * Prerequisite: `npx tsx scripts/generate-dashboard-html.ts` (or
 * `npm run test:e2e`) must have run first.
 */

import { test, expect } from "@playwright/test";
import { getPostedMessages } from "../helpers/webview-loader.js";
import { loadTabFixture } from "../helpers/dashboard-fixtures.js";

test.describe("Runs tab retry round trip", () => {
  test("click retry → runsRefresh posted → loading state renders", async ({ page }) => {
    await loadTabFixture(page, "runs--failure-server_error");
    await page.click("#runsRetryBtn");
    expect(await getPostedMessages(page)).toContainEqual({ type: "runsRefresh" });

    // Simulated response: Dashboard.ts's refreshRunsData() pushes an explicit
    // isLoading:true render before the fetch even starts.
    await loadTabFixture(page, "runs--loading");
    await expect(page.locator(".runs-loading")).toContainText("Loading pipeline runs");
  });
});

test.describe("Compliance tab retry round trip", () => {
  test("click retry → complianceRefresh posted → loading state renders", async ({ page }) => {
    await loadTabFixture(page, "compliance--failure-server_error");
    await page.click("#complianceRetryBtn");
    expect(await getPostedMessages(page)).toContainEqual({ type: "complianceRefresh" });

    await loadTabFixture(page, "compliance--loading");
    await expect(page.locator(".compliance-loading")).toContainText("Loading compliance reports");
  });
});

test.describe("Trends tab retry round trip", () => {
  test("click retry → trendsRefresh posted → loading state renders", async ({ page }) => {
    await loadTabFixture(page, "trends--failure-server_error");
    await page.click("#trendsRetryBtn");
    expect(await getPostedMessages(page)).toContainEqual({ type: "trendsRefresh" });

    await loadTabFixture(page, "trends--loading");
    await expect(page.locator(".trends-empty-title")).toContainText("Loading trends");
  });
});

test.describe("Audit tab local-fallback retry round trip", () => {
  test("click retry → auditRetry posted → loading state renders", async ({ page }) => {
    await loadTabFixture(page, "audit--local-fallback");
    await page.click("#auditRetryBtn");
    expect(await getPostedMessages(page)).toContainEqual({ type: "auditRetry" });

    // Dashboard.ts's "auditRetry" case sets isLoading:true synchronously
    // before re-fetching — same loading branch as the initial-load case.
    await loadTabFixture(page, "audit--loading");
    await expect(page.locator(".audit-loading")).toContainText("Loading audit events");
  });
});

test.describe("Cost tab retry round trip", () => {
  test("click retry → costDateRangeChange posted → an observable state change renders", async ({
    page,
  }) => {
    // Cost's fixture matrix covers unauthorized/offline (not server_error) —
    // offline is the one of the two with showRetry: true (unauthorized shows
    // a sign-in CTA instead), so it is the one with a #costRetryBtn to click.
    await loadTabFixture(page, "cost--failure-offline");
    // CostTabHtml.ts's getPlatformCostFailureHtml wires its retry button to
    // costDateRangeChange (re-requesting the currently-selected range) rather
    // than a dedicated costRefresh message — the fixture's active range is
    // the default "7d".
    await page.click("#costRetryBtn");
    expect(await getPostedMessages(page)).toContainEqual({
      type: "costDateRangeChange",
      range: "7d",
    });

    await loadTabFixture(page, "cost--loading");
    await expect(page.locator(".platform-cost-loading")).toContainText("Loading cost data");
    await expect(page.locator(".platform-cost-failure")).toHaveCount(0);
  });
});

// ---------------------------------------------------------------------------
// These two tabs are why this suite exists. Building it surfaced that neither
// Health nor Dependencies rendered anything when Retry was clicked: their
// refresh paths pushed no loading state before the first await, so the DOM sat
// on the stale failure until the fetch resolved. Fixed in #775 (merged as
// #778); these assertions are the regression guard for the rendering half.
// The Dashboard.ts half — that the loading state is actually pushed before the
// await — is asserted by the data-arrival tier (#746), which drives the real
// class instead of a fixture.
// ---------------------------------------------------------------------------

test.describe("Health tab retry round trip", () => {
  test("click retry posts healthRefresh (outbound half works)", async ({ page }) => {
    await loadTabFixture(page, "health--failure-server_error");
    await page.click("#healthRefreshBtn");
    expect(await getPostedMessages(page)).toContainEqual({ type: "healthRefresh" });
  });

  test("click retry renders an observable loading state", async ({ page }) => {
    await loadTabFixture(page, "health--failure-server_error");
    await page.click("#healthRefreshBtn");
    await loadTabFixture(page, "health--loading");
    await expect(page.locator('.health-empty-state[role="status"]')).toContainText(
      "Loading health data"
    );
  });
});

test.describe("Dependencies (Dependabot) tab retry round trip", () => {
  test("click retry posts dependabotRefresh (outbound half works)", async ({ page }) => {
    await loadTabFixture(page, "dependencies--fetch-error");
    await page.click("#dependabotRetryBtn");
    expect(await getPostedMessages(page)).toContainEqual({ type: "dependabotRefresh" });
  });

  test("click retry renders an observable loading state", async ({ page }) => {
    await loadTabFixture(page, "dependencies--fetch-error");
    await page.click("#dependabotRetryBtn");
    await loadTabFixture(page, "dependencies--loading");
    await expect(page.locator(".dependabot-loading")).toBeVisible();
  });
});
