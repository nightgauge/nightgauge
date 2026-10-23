/**
 * dashboard-fixtures.ts — Loads the tab/state fixture matrix written by
 * scripts/generate-dashboard-html.ts into /tmp/dashboard-fixtures/ (Issue #751).
 *
 * Each fixture is real HTML from getDashboardHtml() (not a hand-written
 * mirror) rendered with one tab's data set to one state — populated, empty,
 * loading, or a specific PlatformFailureKind. The server pre-renders the
 * requested tab's panel with the `active` CSS class, but DashboardHtml.ts's
 * own bootstrap script re-derives which tab is active from
 * `vscode.getState().activeTab` on load and can override it — so every
 * loader here seeds that state to match the fixture's tab, keeping the
 * server-rendered panel visible (required for `toBeVisible()` assertions
 * and screenshots) instead of silently falling back to "overview".
 *
 * Run `npx tsx scripts/generate-dashboard-html.ts` (or `npm run test:e2e`,
 * which chains it) before any test importing this module — same
 * prerequisite as DashboardInteractions.playwright.ts's /tmp/dashboard-test.html.
 */

import { type Page } from "@playwright/test";
import { loadWebviewFromFile } from "./webview-loader.js";

export const FIXTURES_DIR = "/tmp/dashboard-fixtures";

/** Absolute path to a generated fixture file, without its .html extension. */
export function fixturePath(name: string): string {
  return `${FIXTURES_DIR}/${name}.html`;
}

// Fixture name prefixes that don't match their own tab id 1:1.
const TAB_ID_OVERRIDES: Record<string, string> = {
  // The Retention & Integrity panel is part of AuditTabHtml.ts, not its own tab.
  retention: "audit",
};

/**
 * Derives the dashboard tab id a fixture was rendered with `activeTab` set
 * to, from its filename convention (`<tab>--<state>` or
 * `tab-activation--<tabId>`).
 */
export function tabForFixture(name: string): string {
  const [prefix, second] = name.split("--");
  if (prefix === "tab-activation") return second;
  return TAB_ID_OVERRIDES[prefix] ?? prefix;
}

/**
 * Loads a fixture by name (e.g. "runs--failure-server_error"), seeding
 * vscode.getState() so the tab it was rendered active stays the visible
 * panel after the page's own tab-restoration script runs. Extra keys can
 * be merged into the seeded state (rarely needed — most fixtures only care
 * about `activeTab`).
 */
export async function loadTabFixture(
  page: Page,
  name: string,
  extraState: Record<string, unknown> = {}
): Promise<void> {
  const tab = tabForFixture(name);
  await loadWebviewFromFile(page, fixturePath(name), { activeTab: tab, ...extraState });
}
