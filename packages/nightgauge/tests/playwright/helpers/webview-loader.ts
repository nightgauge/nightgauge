import { type Page } from "@playwright/test";

/**
 * Injects the acquireVsCodeApi() mock before page scripts run.
 * Captures all postMessage calls in window.__vscodeMessages[].
 *
 * setState()/getState() are a REAL in-memory store, not the no-op stubs the
 * outbound-only tests got away with — vscode.getState() persists whatever
 * was last set via vscode.setState() (real VS Code persists this across a
 * full webview reload; here it persists for the lifetime of the page, which
 * is what a single-page restoration test needs). `initialState` seeds the
 * store before the page's own scripts run, so a test can simulate "the
 * webview reopened with previously-saved state" (Issue #751 — tab-selection
 * restoration) without needing a real cross-navigation store.
 */
async function injectVsCodeApiMock(page: Page, initialState: unknown = null): Promise<void> {
  await page.addInitScript((seed) => {
    (window as any).__vscodeMessages = [];
    let _state = seed;
    (window as any).acquireVsCodeApi = () => ({
      postMessage: (msg: unknown) => {
        (window as any).__vscodeMessages.push(msg);
      },
      setState: (s: unknown) => {
        _state = s;
        return s;
      },
      getState: () => _state,
    });
  }, initialState);
}

/**
 * Loads an HTML string into a Playwright page with the VSCode API mock injected.
 * The mock is available before any page scripts execute.
 * `initialState` seeds vscode.getState() — see injectVsCodeApiMock().
 */
export async function loadWebview(page: Page, html: string, initialState?: unknown): Promise<void> {
  await injectVsCodeApiMock(page, initialState ?? null);
  // Use goto with data URL to ensure addInitScript fires before page scripts.
  // page.setContent does not reliably trigger addInitScript across all Playwright versions.
  await page.goto(`data:text/html,${encodeURIComponent(html)}`, {
    waitUntil: "domcontentloaded",
  });
}

/**
 * Loads a real HTML file (e.g. one written by scripts/generate-dashboard-html.ts)
 * into a Playwright page, with the same acquireVsCodeApi() mock as loadWebview().
 * Use this instead of loadWebview() when the fixture is real rendered dashboard
 * HTML on disk rather than an inline HTML string (Issue #751).
 *
 * Fixtures under /tmp/dashboard-fixtures/ (the matrix generated for this
 * issue) deliberately do NOT embed their own acquireVsCodeApi mock, so this
 * loader's mock — with real setState()/getState() — is the only one that
 * runs. `/tmp/dashboard-test.html` (the pre-existing single fixture) still
 * embeds its own basic mock for backward compatibility with
 * DashboardInteractions.playwright.ts; loading it through this helper still
 * works, but that page's own embedded mock wins since it executes after
 * this addInitScript (see generate-dashboard-html.ts).
 */
export async function loadWebviewFromFile(
  page: Page,
  filePath: string,
  initialState?: unknown
): Promise<void> {
  await injectVsCodeApiMock(page, initialState ?? null);
  await page.goto(`file://${filePath}`, { waitUntil: "domcontentloaded" });
}

/**
 * Returns the current value of vscode.getState() from inside the page —
 * i.e. whatever the page's own script last passed to vscode.setState().
 * Pairs with the `initialState` seed on loadWebview()/loadWebviewFromFile()
 * to assert both halves of state restoration: seeded-in and read-back.
 */
export async function getVsCodeState(page: Page): Promise<unknown> {
  return page.evaluate(() => {
    const api = (window as any).acquireVsCodeApi?.();
    return api ? api.getState() : undefined;
  });
}

/**
 * Returns all messages posted via vscodeApi.postMessage() since the page loaded.
 */
export async function getPostedMessages(page: Page): Promise<unknown[]> {
  return page.evaluate(() => (window as any).__vscodeMessages ?? []);
}

/**
 * Clears the recorded postMessage() history without reloading the page.
 * Useful between the "outbound" and "simulated response" halves of a
 * round-trip test so assertions only see messages posted after a given point.
 */
export async function clearPostedMessages(page: Page): Promise<void> {
  await page.evaluate(() => {
    (window as any).__vscodeMessages = [];
  });
}

// ---------------------------------------------------------------------------
// Inbound driver (Issue #751)
// ---------------------------------------------------------------------------
//
// Every test above this line drives the OUTBOUND half of the webview
// contract: click a control, assert a vscode.postMessage() call. Nothing
// drives a message INTO the webview the way the real extension host does —
// `panel.webview.postMessage(data)` on the extension side fires a `message`
// DOM event on the webview's `window`, with `event.data` set to the payload.
// `window.addEventListener("message", ...)` handlers (DashboardHtml.ts's
// top-level listener, AuditTabHtml.ts's SSE listener, etc.) only run in
// response to that event — nothing else triggers them in a live webview.
//
// dispatchInboundMessage() reproduces exactly that DOM event so those
// handlers execute under test and their DOM effects can be asserted.

/**
 * Dispatches a `message` event at `window`, the same event a real extension
 * host produces via `panel.webview.postMessage(data)`. Any
 * `window.addEventListener("message", ...)` handler registered by the page's
 * own scripts runs synchronously as a result — this does not require the
 * page to have been loaded via loadWebview()/loadWebviewFromFile(), only that
 * it has already executed its own script (i.e. wait for the handler-installing
 * script to run before calling this).
 */
export async function dispatchInboundMessage(page: Page, data: unknown): Promise<void> {
  await page.evaluate((payload) => {
    window.dispatchEvent(new MessageEvent("message", { data: payload }));
  }, data);
}

/**
 * Dispatches several inbound messages in order, awaiting each in turn.
 * Convenience for tests simulating a short sequence of extension-host
 * pushes (e.g. two rapid `incrementalUpdate` messages).
 */
export async function dispatchInboundMessages(page: Page, messages: unknown[]): Promise<void> {
  for (const message of messages) {
    await dispatchInboundMessage(page, message);
  }
}
