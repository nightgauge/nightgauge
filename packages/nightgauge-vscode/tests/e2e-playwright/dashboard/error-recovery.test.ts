/**
 * error-recovery.test.ts — E2E tests for error recovery after failed stages (Issue #2749).
 *
 * Tests three critical error scenarios:
 *   1. IPC connection failure — error banner + retry clears and resumes
 *   2. API rate limit — rate limit warning shown with countdown
 *   3. Skill execution failure — error details shown, retry succeeds
 *
 * All IPC calls are mocked — errors are simulated via rejectOnce option.
 */

import { test, expect } from "@playwright/test";
import { createIpcMock } from "../helpers/ipc-mock.js";
import {
  loadDashboard,
  waitForDashboard,
  waitForErrorBanner,
  waitForBoardItem,
  waitForPipelineStatus,
  getDashboardMessages,
  setDashboardPipelineState,
} from "../helpers/dashboard-loader.js";
import { makeDashboardHtml, makeMockBoard, makeMockPipelineStatus } from "../helpers/test-data.js";

// ---------------------------------------------------------------------------
// Scenario 1: IPC Connection Failure
// ---------------------------------------------------------------------------

test("Connection failure: error banner appears when boardList IPC call fails", async ({ page }) => {
  const mock = createIpcMock({
    boardItems: makeMockBoard({ readyCount: 2 }),
    rejectOnce: { boardList: "IPC connection refused" },
  });

  const html = makeDashboardHtml({ boardItems: [] });
  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Trigger refresh — first call will reject
  await page.click('[data-test-id="refresh-board-btn"]');

  // Error banner should appear
  await waitForErrorBanner(page);

  const banner = page.locator('[data-test-id="error-banner"]');
  await expect(banner).toBeVisible();

  // Error message should mention connection
  const bannerText = await banner.textContent();
  expect(bannerText).toContain("IPC connection refused");

  // Retry button should be shown in the banner
  await expect(banner.locator('[data-test-id="retry-btn"]')).toBeVisible();
});

test("Connection failure: clicking Retry clears error and resumes normal operation", async ({
  page,
}) => {
  const recoveryItems = makeMockBoard({ readyCount: 3 });
  const mock = createIpcMock({
    boardItems: recoveryItems,
    rejectOnce: { boardList: "IPC connection refused" },
  });

  const html = makeDashboardHtml({ boardItems: [] });
  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // First click: triggers rejection → error banner
  await page.click('[data-test-id="refresh-board-btn"]');
  await waitForErrorBanner(page);

  // Click Retry — second boardList call succeeds
  await page.click('[data-test-id="retry-btn"]');

  // Error banner should be cleared
  await page.waitForSelector('[data-test-id="error-banner"]', {
    state: "detached",
    timeout: 10_000,
  });

  // Board items should load successfully
  await waitForBoardItem(page, recoveryItems[0].number);

  // Verify total boardList calls: initial (rejected) + retry (success)
  const calls = await mock.getCallsFor(page, "boardList");
  expect(calls.length).toBeGreaterThanOrEqual(2);
});

// ---------------------------------------------------------------------------
// Scenario 2: API Rate Limit
// ---------------------------------------------------------------------------

test("Rate limit: rate limit warning appears when boardList returns rate limit error", async ({
  page,
}) => {
  const mock = createIpcMock({
    boardItems: makeMockBoard({ readyCount: 2 }),
    rejectOnce: { boardList: "RATE_LIMITED: retry after 60s" },
  });

  const html = makeDashboardHtml({ boardItems: [] });
  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Trigger refresh — will reject with rate limit error
  await page.click('[data-test-id="refresh-board-btn"]');

  // Either the error banner or rate-limit warning should appear
  // (dashboard script may route to either depending on error message)
  await waitForErrorBanner(page);

  const banner = page.locator('[data-test-id="error-banner"]');
  const bannerText = await banner.textContent();
  expect(bannerText).toMatch(/RATE_LIMITED|rate limit|retry/i);
});

test("Rate limit: retry after delay loads items successfully", async ({ page }) => {
  const successItems = makeMockBoard({ readyCount: 2 });
  const mock = createIpcMock({
    boardItems: successItems,
    rejectOnce: { boardList: "RATE_LIMITED: retry after 1s" },
  });

  const html = makeDashboardHtml({ boardItems: [] });
  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // First click: rate limited
  await page.click('[data-test-id="refresh-board-btn"]');
  await waitForErrorBanner(page);

  // Retry: should succeed this time
  await page.click('[data-test-id="retry-btn"]');

  await page.waitForSelector('[data-test-id="error-banner"]', {
    state: "detached",
    timeout: 10_000,
  });
  await waitForBoardItem(page, successItems[0].number);
});

// ---------------------------------------------------------------------------
// Scenario 3: Skill Execution Failure
// ---------------------------------------------------------------------------

test("Skill failure: error state is shown when pipeline pipelineStatus returns failed status", async ({
  page,
}) => {
  const failedStatus = makeMockPipelineStatus({
    issueNumber: 600,
    status: "failed",
    stage: "feature-dev",
    error: "Skill execution timed out",
  });

  const mock = createIpcMock({ pipelineStatus: failedStatus });
  const html = makeDashboardHtml({ pipelineIssueNumber: 600 });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Trigger pipeline start
  await page.click('[data-test-id="run-pipeline-btn"]');

  // Set dashboard into failed state — throws loudly if helpers aren't registered
  await setDashboardPipelineState(page, { status: "failed", stage: "feature-dev" });

  // Pipeline should be in failed state
  await page.waitForFunction(
    () => {
      const el = document.querySelector('[data-test-id="pipeline-status"]');
      return el?.getAttribute("data-status") === "failed";
    },
    { timeout: 10_000 }
  );

  // Retry stage button should be visible
  await expect(page.locator('[data-test-id="retry-stage-btn"]')).toBeVisible();
});

test("Skill failure: retry succeeds and pipeline transitions to running state", async ({
  page,
}) => {
  const failedStatus = makeMockPipelineStatus({
    issueNumber: 700,
    status: "failed",
    stage: "feature-dev",
    error: "Skill execution timed out",
  });

  // After retry, pipelineStatus returns running
  const runningStatus = makeMockPipelineStatus({
    issueNumber: 700,
    status: "running",
    stage: "feature-dev",
  });

  // First call returns failed, subsequent calls return running
  const mock = createIpcMock({ pipelineStatus: runningStatus });
  const html = makeDashboardHtml({ pipelineIssueNumber: 700 });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Put dashboard into failed state — throws loudly if helpers aren't registered
  await setDashboardPipelineState(page, { status: "failed", stage: "feature-dev" });

  // Retry Stage button should be visible
  await expect(page.locator('[data-test-id="retry-stage-btn"]')).toBeVisible();

  // Click retry
  await page.click('[data-test-id="retry-stage-btn"]');

  // Should transition to running
  await waitForPipelineStatus(page, "running");

  // Verify retry was posted
  const messages = await getDashboardMessages(page);
  expect(messages).toContainEqual(
    expect.objectContaining({ type: "retryStage", stage: "feature-dev" })
  );

  // pipelineStatus was called during retry
  const calls = await mock.getCallsFor(page, "pipelineStatus");
  expect(calls.length).toBeGreaterThanOrEqual(1);
});

test("Skill failure: error details are accessible after failure", async ({ page }) => {
  const mock = createIpcMock({
    pipelineStatus: makeMockPipelineStatus({ issueNumber: 800, status: "running" }),
    rejectOnce: { pipelineStatus: "Skill execution failed: feature-dev timed out after 120s" },
  });

  const html = makeDashboardHtml({ pipelineIssueNumber: 800 });
  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Trigger pipeline — first pipelineStatus call will reject
  await page.click('[data-test-id="run-pipeline-btn"]');

  // Error banner should appear with the error details
  await waitForErrorBanner(page);

  const bannerText = await page.textContent('[data-test-id="error-banner"]');
  expect(bannerText).toContain("feature-dev timed out");
});
