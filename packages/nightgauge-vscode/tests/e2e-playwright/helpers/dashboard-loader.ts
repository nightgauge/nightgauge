/**
 * dashboard-loader.ts — Load dashboard HTML into Playwright with IPC mock injected (Issue #2749).
 *
 * Provides utility functions for loading dashboard HTML into a Playwright page,
 * injecting the IPC mock before page scripts execute, and capturing messages.
 *
 * Pattern:
 *   const mock = createIpcMock({ boardItems: [...] });
 *   await loadDashboard(page, html, mock.initScript);
 *   await page.click('[data-test-id="run-pipeline-btn"]');
 *   const calls = await mock.getCalls(page);
 */

import { type Page } from "@playwright/test";

/**
 * Injects the IPC mock, VSCode API stub, and optional custom init scripts into
 * the page, then loads the provided HTML string. All scripts are injected before
 * page scripts run so the mock is available to page code immediately.
 *
 * @param page - Playwright page
 * @param html - Dashboard HTML string
 * @param ipcInitScript - Serialized IPC mock init script from createIpcMock().initScript
 * @param extraInitScript - Optional additional init script (runs after IPC mock)
 */
export async function loadDashboard(
  page: Page,
  html: string,
  ipcInitScript: string,
  extraInitScript?: string
): Promise<void> {
  // 1. Inject VSCode API stub (must run before page scripts)
  await page.addInitScript(() => {
    (window as any).__vscodeMessages = [];
    (window as any).acquireVsCodeApi = function () {
      return {
        postMessage: function (msg: unknown) {
          (window as any).__vscodeMessages.push(msg);
        },
        setState: function () {},
        getState: function () {
          return {};
        },
      };
    };
  });

  // 2. Inject IPC mock (must run before page scripts)
  await page.addInitScript(ipcInitScript);

  // 3. Inject any extra setup (optional)
  if (extraInitScript) {
    await page.addInitScript(extraInitScript);
  }

  // 4. Load HTML via data URL — ensures addInitScript fires before page scripts
  //    (page.setContent does not reliably trigger addInitScript in all versions)
  await page.goto(`data:text/html,${encodeURIComponent(html)}`, {
    waitUntil: "domcontentloaded",
  });
}

/**
 * Returns all messages posted via vscodeApi.postMessage() since the page loaded.
 */
export async function getPostedMessages(page: Page): Promise<unknown[]> {
  return page.evaluate(() => (window as any).__vscodeMessages ?? []);
}

/**
 * Returns all messages posted via the internal __dashboardMessages array,
 * which captures messages even when acquireVsCodeApi is not available.
 */
export async function getDashboardMessages(page: Page): Promise<unknown[]> {
  return page.evaluate(() => (window as any).__dashboardMessages ?? []);
}

/**
 * Waits for the dashboard's main container to be visible.
 */
export async function waitForDashboard(page: Page, timeout = 10_000): Promise<void> {
  await page.waitForSelector('[data-test-id="dashboard"]', { timeout });
}

/**
 * Waits for a board item with the specified issue number to be visible.
 */
export async function waitForBoardItem(
  page: Page,
  issueNumber: number,
  timeout = 10_000
): Promise<void> {
  await page.waitForSelector(`[data-test-id="board-item-${issueNumber}"]`, { timeout });
}

/**
 * Waits for the pipeline status to reflect the given status value.
 */
export async function waitForPipelineStatus(
  page: Page,
  status: string,
  timeout = 15_000
): Promise<void> {
  await page.waitForFunction(
    (expectedStatus) => {
      const el = document.querySelector('[data-test-id="pipeline-status"]');
      return el?.getAttribute("data-status") === expectedStatus;
    },
    status,
    { timeout }
  );
}

/**
 * Waits for an error banner to appear.
 */
export async function waitForErrorBanner(page: Page, timeout = 10_000): Promise<void> {
  await page.waitForSelector('[data-test-id="error-banner"]', { timeout });
}

/**
 * Returns the current board item count shown in the UI.
 */
export async function getBoardItemCount(page: Page): Promise<number> {
  const text = await page.textContent('[data-test-id="board-item-count"]');
  return parseInt(text ?? "0", 10);
}

/**
 * Forces the dashboard UI into a specific pipeline state by calling
 * `window.__dashboardHelpers.updatePipelineUI()`.
 *
 * Throws if `__dashboardHelpers` is not registered — making test setup failures
 * loud rather than silently no-op (avoids false passes when helpers are absent).
 */
export async function setDashboardPipelineState(
  page: Page,
  state: { status: string; stage?: string }
): Promise<void> {
  const ok = await page.evaluate((s) => {
    const helpers = (window as any).__dashboardHelpers;
    if (!helpers || typeof helpers.updatePipelineUI !== "function") return false;
    helpers.updatePipelineUI(s);
    return true;
  }, state);

  if (!ok) {
    throw new Error(
      "__dashboardHelpers.updatePipelineUI is not registered on the page. " +
        "Ensure the dashboard HTML was loaded via loadDashboard() with the IPC mock init script."
    );
  }
}
