/**
 * Playwright tests for Dashboard webview behaviors (Issue #1244)
 *
 * Targets embedded JS behaviors that Vitest cannot reach:
 *   - Scroll position preservation after incremental DOM updates
 *   - Debounced re-renders (rapid messages coalesce into a single DOM update)
 *   - vscode.postMessage ↔ acquireVsCodeApi() message roundtrip for all interactive controls
 *
 * Uses the webview-loader helper from Issue #1243 to inject the acquireVsCodeApi() mock
 * before page scripts execute, capturing all postMessage calls in window.__vscodeMessages[].
 */

import { test, expect } from "@playwright/test";
import { loadWebview, getPostedMessages } from "../helpers/webview-loader.js";

/**
 * Minimal dashboard HTML fixture containing the interactive elements and
 * script behaviors under test. Mirrors the script logic from DashboardHtml.ts
 * getScript() / getFirewallScript() so tests validate the behavior contract.
 */
function createDashboardHtml(): string {
  return `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"></head>
<body style="height:200px; overflow:auto;">

  <!-- Incremental update target -->
  <div id="section-summary-cards"><p>Initial content</p></div>

  <!-- Header -->
  <span id="lastUpdated">Last updated: now</span>
  <button id="refreshBtn"><span id="refreshIcon">&#8635;</span> Refresh</button>

  <!-- Export -->
  <button id="exportJson">Export JSON</button>
  <button id="exportCsv">Export CSV</button>

  <!-- Scope toggles -->
  <button class="toggle-btn" data-scope="session">Session</button>
  <button class="toggle-btn" data-scope="all">All Time</button>

  <!-- History load-more -->
  <button id="loadMoreBtn" onclick="loadMoreHistory()">Load more</button>

  <!-- Firewall filter (category) -->
  <select id="firewallCategoryFilter">
    <option value="">All</option>
    <option value="pii">PII</option>
  </select>

  <script>
    // -----------------------------------------------------------------------
    // Dashboard script (mirrors DashboardHtml.ts getScript())
    // -----------------------------------------------------------------------

    // Mirrors the outer scope in DashboardHtml.ts: const vscode = acquireVsCodeApi();
    const vscode = acquireVsCodeApi();

    const VALID_SECTIONS = new Set(['pipeline-progress', 'summary-cards', 'analytics', 'tool-calls']);

    // Debounce accumulator for incremental updates (Issue #1244)
    var _pendingIncrementalUpdates = {};
    var _incrementalUpdateTimer = null;
    function _flushIncrementalUpdates() {
      Object.keys(_pendingIncrementalUpdates).forEach(function(section) {
        var el = document.getElementById('section-' + section);
        if (el) {
          el.innerHTML = _pendingIncrementalUpdates[section];
        }
      });
      _pendingIncrementalUpdates = {};
    }

    (function() {
      // Refresh button
      document.getElementById('refreshBtn')?.addEventListener('click', function() {
        vscode.postMessage({ type: 'refresh' });
      });

      // Export buttons
      document.getElementById('exportJson')?.addEventListener('click', function() {
        vscode.postMessage({ type: 'export', format: 'json', target: 'current' });
      });
      document.getElementById('exportCsv')?.addEventListener('click', function() {
        vscode.postMessage({ type: 'export', format: 'csv', target: 'current' });
      });

      // Scope toggles
      document.querySelectorAll('.toggle-btn').forEach(function(btn) {
        btn.addEventListener('click', function(e) {
          var scope = e.target.dataset.scope;
          vscode.postMessage({ type: 'setScope', scope: scope });
        });
      });

      // Message handler (incremental updates + scroll helpers)
      window.addEventListener('message', function(event) {
        var message = event.data;
        if (message.type === 'incrementalUpdate' && VALID_SECTIONS.has(message.section) && typeof message.html === 'string') {
          // Debounce: accumulate rapid updates, flush after 50 ms of silence
          _pendingIncrementalUpdates[message.section] = message.html;
          clearTimeout(_incrementalUpdateTimer);
          _incrementalUpdateTimer = setTimeout(_flushIncrementalUpdates, 50);
        } else if (message.type === 'restoreScrollPosition' && typeof message.scrollY === 'number') {
          window.scrollTo(0, message.scrollY);
        } else if (message.type === 'requestScrollPosition') {
          vscode.postMessage({ type: 'scrollPosition', scrollY: window.scrollY });
        }
      });
    })();

    // Load more history
    function loadMoreHistory() {
      vscode.postMessage({ type: 'loadMoreHistory' });
    }
  </script>

  <script>
    // -----------------------------------------------------------------------
    // Firewall script (mirrors FirewallTabHtml.ts getFirewallScript())
    // -----------------------------------------------------------------------
    (function() {
      document.getElementById('firewallCategoryFilter')?.addEventListener('change', function(e) {
        var value = e.target.value;
        vscode.postMessage({
          type: 'firewallFilter',
          filter: 'category',
          value: value ? [value] : []
        });
      });
    })();
  </script>
</body>
</html>`;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe("Dashboard webview", () => {
  test('Refresh button posts { type: "refresh" }', async ({ page }) => {
    await loadWebview(page, createDashboardHtml());
    await page.click("#refreshBtn");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "refresh" });
  });

  test('Export JSON button posts { type: "export", format: "json" }', async ({ page }) => {
    await loadWebview(page, createDashboardHtml());
    await page.click("#exportJson");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "export",
      format: "json",
      target: "current",
    });
  });

  test('Export CSV button posts { type: "export", format: "csv" }', async ({ page }) => {
    await loadWebview(page, createDashboardHtml());
    await page.click("#exportCsv");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "export",
      format: "csv",
      target: "current",
    });
  });

  test('Scope "Session" toggle posts { type: "setScope", scope: "session" }', async ({ page }) => {
    await loadWebview(page, createDashboardHtml());
    await page.click('.toggle-btn[data-scope="session"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "setScope", scope: "session" });
  });

  test('Scope "All Time" toggle posts { type: "setScope", scope: "all" }', async ({ page }) => {
    await loadWebview(page, createDashboardHtml());
    await page.click('.toggle-btn[data-scope="all"]');
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "setScope", scope: "all" });
  });

  test('"Load more" button posts { type: "loadMoreHistory" }', async ({ page }) => {
    await loadWebview(page, createDashboardHtml());
    await page.click("#loadMoreBtn");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "loadMoreHistory" });
  });

  test('Firewall category filter change posts { type: "firewallFilter" } with correct value', async ({
    page,
  }) => {
    await loadWebview(page, createDashboardHtml());
    await page.selectOption("#firewallCategoryFilter", "pii");
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({
      type: "firewallFilter",
      filter: "category",
      value: ["pii"],
    });
  });

  test("Scroll position is preserved after incremental DOM update", async ({ page }) => {
    await loadWebview(page, createDashboardHtml());

    // Make the page scrollable and set a known scroll position
    await page.evaluate(() => {
      document.body.style.height = "5000px";
      window.scrollTo(0, 800);
    });
    const scrollBefore = await page.evaluate(() => window.scrollY);
    expect(scrollBefore).toBe(800);

    // Dispatch an incremental update (replaces section innerHTML, does NOT reload)
    await page.evaluate(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: {
            type: "incrementalUpdate",
            section: "summary-cards",
            html: "<p>Updated</p>",
          },
        })
      );
    });

    // Wait for the 50 ms debounce to flush
    await page.waitForTimeout(150);

    // Section content updated
    const content = await page.textContent("#section-summary-cards");
    expect(content).toContain("Updated");

    // Scroll position unchanged (no page reload, no scroll-to-top)
    const scrollAfter = await page.evaluate(() => window.scrollY);
    expect(scrollAfter).toBe(800);
  });

  test("Debounce: 10 rapid incremental updates coalesce into a single DOM mutation", async ({
    page,
  }) => {
    await loadWebview(page, createDashboardHtml());

    // Attach a MutationObserver to count innerHTML replacements on the target section
    await page.evaluate(() => {
      (window as any).__mutationCount = 0;
      const el = document.getElementById("section-summary-cards");
      if (el) {
        const observer = new MutationObserver(() => {
          (window as any).__mutationCount++;
        });
        observer.observe(el, {
          childList: true,
          subtree: true,
          characterData: true,
        });
      }
    });

    // Dispatch 10 rapid incremental update messages without waiting between them
    for (let i = 0; i < 10; i++) {
      await page.evaluate((idx) => {
        window.dispatchEvent(
          new MessageEvent("message", {
            data: {
              type: "incrementalUpdate",
              section: "summary-cards",
              html: `<p>Update ${idx}</p>`,
            },
          })
        );
      }, i);
    }

    // Wait for debounce timer to fire (50 ms) plus margin
    await page.waitForTimeout(200);

    // Only 1 DOM mutation should have occurred (the debounced flush)
    const mutationCount = await page.evaluate(() => (window as any).__mutationCount);
    expect(mutationCount).toBe(1);

    // Final content reflects the last update (index 9)
    const content = await page.textContent("#section-summary-cards");
    expect(content).toContain("Update 9");
  });
});
