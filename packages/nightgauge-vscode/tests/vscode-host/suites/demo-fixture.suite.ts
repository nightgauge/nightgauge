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
 * Audit Trail reads the local run history while `platform.enabled` is false,
 * as it is in the fixture (ADR-026 section 6). Discovery reads the
 * release-watch and continuous-improvement logs.
 *
 * Out of scope, because they do not read local files: Runs, Cost, Trends,
 * Health and Compliance (platform data over IPC, answered by the demo daemon,
 * #2105), and Epics and Dependencies (GitHub). The Knowledge and Attention
 * tree views are covered by the tree-view suites against the same files.
 */

import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { capturedPanels } from "../observe.js";
import { materializeDemoWorkspace, waitFor, workspaceRoot } from "../fixture.js";

/**
 * The Audit Trail's date range, as its filter inputs send it: the seeded
 * history is dated January 2026, and the tab opens on the last seven days.
 */
const AUDIT_RANGE = {
  type: "auditFilter",
  filters: {
    dateFrom: "2026-01-01T00:00:00.000Z",
    dateTo: "2026-01-31T23:59:59.999Z",
    actionFilter: "",
    userFilter: "",
  },
};

/**
 * A message the user sends once the tab has rendered: `after` recognises
 * that render, so the message is not dropped by a fetch still in flight.
 */
interface FollowUp {
  after: (panel: string) => boolean;
  message: Record<string, unknown>;
}

/**
 * Tab id, a predicate over that tab's panel markup, and any follow-up the
 * user would send before reading it. Each predicate needs text only the demo
 * workspace's files can put there: a run title or the summed cost of the four
 * seeded history records ($8.52). The overview one also rejects the
 * empty-health placeholder, which shows while history is unread.
 */
const FILE_BACKED_TABS: ReadonlyArray<readonly [string, (panel: string) => boolean, FollowUp?]> = [
  [
    "overview",
    (p) => p.includes("$8.52") && !p.includes("Run your first pipeline to see health metrics"),
  ],
  // The run the demo workspace's state.json and the daemon both hold in
  // flight, adopted at connect (#2105).
  [
    "pipeline",
    (p) => p.includes("Current Pipeline Run") && p.includes("Retry failed harbour-fee webhooks"),
  ],
  ["analytics", (p) => p.includes("Token Usage by Stage") && p.includes("claude-sonnet")],
  [
    "history",
    (p) => p.includes("Move berth assignments to the") && p.includes("Split the invoice worker"),
  ],
  [
    "audit",
    (p) =>
      p.includes("platform communication is off") &&
      p.includes("pipeline_run_failed") &&
      p.includes("Move berth assignments to the new schema"),
    { after: (p) => p.includes("Showing local telemetry"), message: AUDIT_RANGE },
  ],
  [
    "discovery",
    (p) =>
      p.includes("Adopt the tide-table client") &&
      p.includes("Cache the pilot roster") &&
      p.includes("Retire the legacy anchorage-zone lookup table"),
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
      // One refresh: it loads history before computing the health widget, so
      // the first pass already reads the seeded runs.
      for (const listener of dashboard.messageListeners) {
        await listener({ type: "refresh" });
      }
      const send = async (message: Record<string, unknown>) => {
        for (const listener of dashboard.messageListeners) await listener(message);
      };
      for (const [tab, populated, followUp] of FILE_BACKED_TABS) {
        await send({ type: "selectTab", tab });
        if (followUp) {
          await waitFor(
            () => (followUp.after(tabPanel(dashboard.panel.webview.html, tab)) ? true : undefined),
            15_000,
            `the ${tab} tab's first render`
          );
          await send(followUp.message);
        }
        const panel = await waitFor(
          () => {
            const body = tabPanel(dashboard.panel.webview.html, tab);
            return populated(body) ? body : undefined;
          },
          15_000,
          `the ${tab} tab to render rows from the demo workspace`
        ).catch(() => undefined);
        const shown = tabPanel(dashboard.panel.webview.html, tab);
        assert.ok(
          panel,
          `The ${tab} tab shows no demo workspace rows:\n${shown
            .replace(/<script\b[\s\S]*?<\/script>/gi, " ")
            .replace(/<[^>]+>/g, " ")
            .replace(/\s+/g, " ")
            .slice(0, 2000)}`
        );
      }
    } finally {
      dashboard.panel.dispose();
    }
  });
});
