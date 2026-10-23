/**
 * pipeline-execution.test.ts — E2E tests for pipeline execution from dashboard (Issue #2749).
 *
 * Tests three critical pipeline workflow scenarios:
 *   1. Start pipeline and monitor progress
 *   2. Cancel a running pipeline
 *   3. Retry a failed stage
 *
 * All IPC calls are mocked — no real Go binary or GitHub API required.
 */

import { test, expect } from "@playwright/test";
import { createIpcMock } from "../helpers/ipc-mock.js";
import {
  loadDashboard,
  waitForDashboard,
  waitForPipelineStatus,
  getDashboardMessages,
  setDashboardPipelineState,
} from "../helpers/dashboard-loader.js";
import {
  makeDashboardHtml,
  makeMockPipelineStatus,
  makeMockFailedPipelineStatus,
} from "../helpers/test-data.js";

// ---------------------------------------------------------------------------
// Scenario 1: Start Pipeline and Monitor Progress
// ---------------------------------------------------------------------------

test("Start pipeline: clicking Run Pipeline triggers pipelineStatus IPC call and transitions UI to running", async ({
  page,
}) => {
  const runningStatus = makeMockPipelineStatus({
    issueNumber: 100,
    status: "running",
    stage: "issue-pickup",
    progress: 0.1,
  });

  const mock = createIpcMock({ pipelineStatus: runningStatus });
  const html = makeDashboardHtml({ pipelineIssueNumber: 100 });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Initial state: run button visible, stop button hidden
  await expect(page.locator('[data-test-id="run-pipeline-btn"]')).toBeVisible();
  await expect(page.locator('[data-test-id="stop-pipeline-btn"]')).not.toBeVisible();

  // Click Run Pipeline
  await page.click('[data-test-id="run-pipeline-btn"]');

  // UI should transition to running state
  await waitForPipelineStatus(page, "running");

  // Stop button should appear, run button hidden
  await expect(page.locator('[data-test-id="stop-pipeline-btn"]')).toBeVisible();
  await expect(page.locator('[data-test-id="run-pipeline-btn"]')).not.toBeVisible();

  // Verify IPC call was made
  const calls = await mock.getCallsFor(page, "pipelineStatus");
  expect(calls.length).toBeGreaterThanOrEqual(1);

  // Verify message was posted
  const messages = await getDashboardMessages(page);
  expect(messages).toContainEqual(
    expect.objectContaining({ type: "runPipeline", issueNumber: 100 })
  );
});

test("Start pipeline: stage name is displayed in the UI after pipeline starts", async ({
  page,
}) => {
  const runningStatus = makeMockPipelineStatus({
    issueNumber: 100,
    status: "running",
    stage: "feature-dev",
    progress: 0.6,
  });

  const mock = createIpcMock({ pipelineStatus: runningStatus });
  const html = makeDashboardHtml({ pipelineIssueNumber: 100 });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Trigger UI update to simulate a pipeline progress event
  await setDashboardPipelineState(page, { status: "running", stage: "feature-dev" });

  // Stage name should appear in the pipeline-stage element
  const stageText = await page.textContent('[data-test-id="pipeline-stage"]');
  expect(stageText).toContain("feature-dev");
});

// ---------------------------------------------------------------------------
// Scenario 2: Cancel Running Pipeline
// ---------------------------------------------------------------------------

test("Cancel pipeline: clicking Stop Pipeline triggers pipelineStop IPC call and transitions UI to idle", async ({
  page,
}) => {
  const runningStatus = makeMockPipelineStatus({ issueNumber: 100, status: "running" });
  const mock = createIpcMock({ pipelineStatus: runningStatus });

  // Start with running state pre-rendered
  const html = makeDashboardHtml({ pipelineIssueNumber: 100 });
  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // First start the pipeline to get into running state
  await page.click('[data-test-id="run-pipeline-btn"]');
  await waitForPipelineStatus(page, "running");

  // Now stop it
  await page.click('[data-test-id="stop-pipeline-btn"]');

  // UI should return to idle
  await waitForPipelineStatus(page, "idle");

  // Run button should be visible again
  await expect(page.locator('[data-test-id="run-pipeline-btn"]')).toBeVisible();
  await expect(page.locator('[data-test-id="stop-pipeline-btn"]')).not.toBeVisible();

  // Verify pipelineStop was called
  const stopCalls = await mock.getCallsFor(page, "pipelineStop");
  expect(stopCalls.length).toBeGreaterThanOrEqual(1);

  // Verify stop message posted
  const messages = await getDashboardMessages(page);
  expect(messages).toContainEqual(expect.objectContaining({ type: "stopPipeline" }));
});

// ---------------------------------------------------------------------------
// Scenario 3: Retry Failed Stage
// ---------------------------------------------------------------------------

test("Retry stage: clicking Retry Stage on a failed pipeline triggers pipelineStatus call for the failed stage", async ({
  page,
}) => {
  const failedStatus = makeMockFailedPipelineStatus({
    issueNumber: 200,
    stage: "feature-dev",
    error: "Skill execution timed out",
  });

  // Mock: first pipelineStatus returns failed, second returns running (simulating retry)
  const mock = createIpcMock({ pipelineStatus: failedStatus });
  const html = makeDashboardHtml({ pipelineIssueNumber: 200 });

  await loadDashboard(page, html, mock.initScript);
  await waitForDashboard(page);

  // Set dashboard into failed state — throws loudly if helpers aren't registered
  await setDashboardPipelineState(page, { status: "failed", stage: "feature-dev" });

  // Retry Stage button should be visible
  await expect(page.locator('[data-test-id="retry-stage-btn"]')).toBeVisible();

  // Click Retry Stage
  await page.click('[data-test-id="retry-stage-btn"]');

  // UI should transition to running
  await waitForPipelineStatus(page, "running");

  // Verify IPC call was made
  const pipelineCalls = await mock.getCallsFor(page, "pipelineStatus");
  expect(pipelineCalls.length).toBeGreaterThanOrEqual(1);

  // Verify retry message posted with stage name
  const messages = await getDashboardMessages(page);
  expect(messages).toContainEqual(
    expect.objectContaining({ type: "retryStage", stage: "feature-dev" })
  );
});
