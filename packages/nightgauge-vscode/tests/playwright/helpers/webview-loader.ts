import { type Page } from "@playwright/test";

/**
 * Injects the acquireVsCodeApi() mock before page scripts run.
 * Captures all postMessage calls in window.__vscodeMessages[].
 */
async function injectVsCodeApiMock(page: Page): Promise<void> {
  await page.addInitScript(() => {
    (window as any).__vscodeMessages = [];
    (window as any).acquireVsCodeApi = () => ({
      postMessage: (msg: unknown) => {
        (window as any).__vscodeMessages.push(msg);
      },
      setState: () => {},
      getState: () => ({}),
    });
  });
}

/**
 * Loads an HTML string into a Playwright page with the VSCode API mock injected.
 * The mock is available before any page scripts execute.
 */
export async function loadWebview(page: Page, html: string): Promise<void> {
  await injectVsCodeApiMock(page);
  // Use goto with data URL to ensure addInitScript fires before page scripts.
  // page.setContent does not reliably trigger addInitScript across all Playwright versions.
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
