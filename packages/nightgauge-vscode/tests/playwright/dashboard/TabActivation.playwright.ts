/**
 * Playwright tests for dashboard tab-activation machinery (Issue #751):
 * activating each of the 13 tabs, the `selectTab` lazy-load message that
 * populates Audit/Discovery/Cost/Health/Runs/Trends/Compliance/Dependencies
 * on first activation, and state restoration via vscode.setState()/getState().
 *
 * DashboardHtml.ts's tab script (getTabScript()) is the code under test:
 *
 *   var savedTab = (vscode.getState() && vscode.getState().activeTab) || 'overview';
 *   activateTab(savedTab);
 *   vscode.postMessage({ type: 'selectTab', tab: savedTab });
 *   ...
 *   btn.addEventListener('click', function() {
 *     activateTab(tabId);
 *     vscode.setState(Object.assign({}, vscode.getState() || {}, { activeTab: tabId }));
 *     vscode.postMessage({ type: 'selectTab', tab: tabId });
 *   });
 *
 * Prerequisite: `npx tsx scripts/generate-dashboard-html.ts` (or
 * `npm run test:e2e`) must have run first.
 */

import { test, expect } from "@playwright/test";
import {
  loadWebviewFromFile,
  getPostedMessages,
  getVsCodeState,
} from "../helpers/webview-loader.js";
import { fixturePath } from "../helpers/dashboard-fixtures.js";

// /tmp/dashboard-test.html (the pre-#751 single fixture) embeds its own basic
// acquireVsCodeApi mock for DashboardInteractions.playwright.ts's backward
// compatibility — that embedded <script> runs AFTER this file's addInitScript
// mock and would silently overwrite it, undoing the real setState()/getState()
// these tests depend on (see generate-dashboard-html.ts's postProcess() and
// webview-loader.ts's loadWebviewFromFile() doc comments). Use a fixture from
// the #751 matrix instead, which has no such embedded mock.
const DEFAULT_HTML_PATH = fixturePath("tab-activation--overview");

const ALL_TAB_IDS = [
  "overview",
  "pipeline",
  "analytics",
  "history",
  "epics",
  "audit",
  "discovery",
  "cost",
  "health",
  "runs",
  "trends",
  "compliance",
  "dependencies",
];

// Every tab posts selectTab on activation (DashboardHtml.ts's tab script does
// not distinguish "lazy" tabs from static ones), but only these 8 actually
// have an extension-host handler that fetches data on first activation
// (Dashboard.ts's "selectTab" case — see the fixture matrix in
// scripts/generate-dashboard-html.ts, which mirrors this same set).
const LAZY_LOAD_TAB_IDS = [
  "audit",
  "discovery",
  "cost",
  "health",
  "runs",
  "trends",
  "compliance",
  "dependencies",
];

test.describe("Tab activation — click-through from a fresh load", () => {
  test("every tab button activates its panel and posts selectTab", async ({ page }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH);

    // Bootstrap posts selectTab for the default tab ('overview': no seeded
    // state on this load) before any click.
    let messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "selectTab", tab: "overview" });
    await expect(page.locator("#tab-panel-overview")).toHaveClass(/active/);

    for (const tabId of ALL_TAB_IDS) {
      await page.click(`[data-tab="${tabId}"]`);

      await expect(page.locator(`#tab-panel-${tabId}`)).toHaveClass(/active/);
      // Every OTHER panel lost the active class (only one panel visible at a time).
      for (const other of ALL_TAB_IDS) {
        if (other === tabId) continue;
        await expect(page.locator(`#tab-panel-${other}`)).not.toHaveClass(/active/);
      }

      messages = await getPostedMessages(page);
      expect(messages).toContainEqual({ type: "selectTab", tab: tabId });

      // vscode.setState() persisted the click so a later reload restores it.
      expect(await getVsCodeState(page)).toEqual({ activeTab: tabId });
    }
  });

  for (const tabId of LAZY_LOAD_TAB_IDS) {
    test(`activating "${tabId}" posts selectTab (the lazy-load trigger Dashboard.ts fetches on)`, async ({
      page,
    }) => {
      await loadWebviewFromFile(page, DEFAULT_HTML_PATH);
      await page.click(`[data-tab="${tabId}"]`);
      const messages = await getPostedMessages(page);
      expect(messages).toContainEqual({ type: "selectTab", tab: tabId });
    });
  }
});

test.describe("Tab activation — state restoration via vscode.setState()/getState()", () => {
  for (const tabId of ALL_TAB_IDS) {
    test(`reopening with activeTab="${tabId}" saved restores that tab and re-triggers selectTab`, async ({
      page,
    }) => {
      // Seeds vscode.getState() the way a real webview reload does — the
      // extension host doesn't own this state, VS Code's webview host does,
      // and it survives the panel.webview.html reassignment every refresh
      // performs (see RetryRoundTrip.playwright.ts's header comment).
      await loadWebviewFromFile(page, fixturePath(`tab-activation--${tabId}`), {
        activeTab: tabId,
      });

      await expect(page.locator(`#tab-panel-${tabId}`)).toHaveClass(/active/);
      const tabButton = page.locator(`[data-tab="${tabId}"]`);
      await expect(tabButton).toHaveClass(/active/);
      await expect(tabButton).toHaveAttribute("aria-selected", "true");

      // The restoration branch posts selectTab too — this is what makes a
      // reopened lazy tab (e.g. Runs, reloaded after a VS Code restart)
      // actually fetch its data instead of showing a stale/empty panel
      // forever (Issue #2582, referenced directly in DashboardHtml.ts).
      const messages = await getPostedMessages(page);
      expect(messages).toContainEqual({ type: "selectTab", tab: tabId });
    });
  }

  test("no saved state falls back to the overview tab", async ({ page }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH); // no initialState seed
    await expect(page.locator("#tab-panel-overview")).toHaveClass(/active/);
    expect(await getVsCodeState(page)).toBeNull();
  });
});

test.describe("Tab activation — keyboard navigation", () => {
  test("ArrowRight moves focus to the next tab button without activating it", async ({ page }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH);
    await page.locator('[data-tab="overview"]').focus();
    await page.keyboard.press("ArrowRight");

    await expect(page.locator('[data-tab="pipeline"]')).toBeFocused();
    // Focus moved, but the panel did not activate — arrow keys browse, Enter/Space commits.
    await expect(page.locator("#tab-panel-overview")).toHaveClass(/active/);
    await expect(page.locator("#tab-panel-pipeline")).not.toHaveClass(/active/);
  });

  test("Enter on a focused tab button activates it", async ({ page }) => {
    await loadWebviewFromFile(page, DEFAULT_HTML_PATH);
    await page.locator('[data-tab="pipeline"]').focus();
    await page.keyboard.press("Enter");

    await expect(page.locator("#tab-panel-pipeline")).toHaveClass(/active/);
    const messages = await getPostedMessages(page);
    expect(messages).toContainEqual({ type: "selectTab", tab: "pipeline" });
  });
});
