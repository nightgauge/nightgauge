/**
 * The demo workspace (`demo/workspace/`, #2107, ADR-026): does every dashboard
 * tab that reads local files render at least one populated row from it?
 *
 * The fixture is copied over the open workspace, the real Dashboard is opened,
 * and each file-backed tab is selected the way the webview does it (a
 * `selectTab` message). The tab's panel markup is then read from the webview
 * HTML and must contain content only the demo files carry, so an empty-state
 * placeholder cannot satisfy it.
 *
 * Out of scope, because they do not read local files: Runs, Cost, Trends,
 * Health, Compliance and Audit Trail (platform data over IPC, answered by the
 * demo daemon, #2105; Audit shows "No Access" with the platform off), and
 * Epics, Discovery and Dependencies (GitHub). `platform.enabled` is false in
 * the fixture (ADR-026 section 6). The Knowledge and Attention tree views are
 * covered by the tree-view suites against the same files.
 */

import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { capturedPanels } from "../observe.js";
import { materializeDemoWorkspace, waitFor, workspaceRoot } from "../fixture.js";

/**
 * Tab id and a predicate over that tab's panel markup. Each predicate needs
 * text only the demo workspace's files can put there: a run title or the
 * summed cost of the four seeded history records ($8.52). The overview one
 * also rejects the empty-health placeholder, which shows while history is
 * unread.
 */
const FILE_BACKED_TABS: ReadonlyArray<readonly [string, (panel: string) => boolean]> = [
  [
    "overview",
    (p) => p.includes("$8.52") && !p.includes("Run your first pipeline to see health metrics"),
  ],
  ["pipeline", (p) => p.includes("Fix time-zone drift in arrival times")],
  ["analytics", (p) => p.includes("Token Usage by Stage") && p.includes("claude-sonnet")],
  [
    "history",
    (p) => p.includes("Move berth assignments to the") && p.includes("Split the invoice worker"),
  ],
];

function tabPanel(html: string, tab: string): string {
  const start = html.indexOf(`id="tab-panel-${tab}"`);
  if (start < 0) return "";
  const next = html.indexOf('class="tab-panel', start + 1);
  return html.slice(start, next < 0 ? undefined : next);
}

suite("demo workspace", () => {
  test("the demo workspace is fictional and offline", () => {
    materializeDemoWorkspace();
    const config = fs.readFileSync(
      path.join(workspaceRoot(), ".nightgauge", "config.yaml"),
      "utf8"
    );
    assert.match(config, /owner: lanternworks/);
    assert.match(config, /platform:\s*\n\s+enabled: false/);
  });

  test("each file-backed dashboard tab renders a populated row", async () => {
    materializeDemoWorkspace();
    for (const entry of capturedPanels().filter((p) => p.viewType === "nightgaugeDashboard")) {
      if (!entry.disposed) entry.panel.dispose();
    }
    await vscode.commands.executeCommand("nightgauge.showDashboard");
    const dashboard = await waitFor(
      () => capturedPanels().find((p) => p.viewType === "nightgaugeDashboard" && !p.disposed),
      5_000,
      "the Dashboard panel"
    );
    try {
      // Twice: a refresh computes the health widget in parallel with the
      // history backfill, so the first one sees no history and the second
      // is the one that reads the seeded runs.
      for (let pass = 0; pass < 2; pass++) {
        for (const listener of dashboard.messageListeners) {
          await listener({ type: "refresh" });
        }
      }
      for (const [tab, populated] of FILE_BACKED_TABS) {
        for (const listener of dashboard.messageListeners) {
          await listener({ type: "selectTab", tab });
        }
        const panel = await waitFor(
          () => {
            const body = tabPanel(dashboard.panel.webview.html, tab);
            return populated(body) ? body : undefined;
          },
          15_000,
          `the ${tab} tab to render rows from the demo workspace`
        );
        assert.ok(panel.length > 0);
      }
    } finally {
      dashboard.panel.dispose();
    }
  });
});
