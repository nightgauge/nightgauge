/**
 * Cycle Usage Metric command (Issue #659)
 *
 * Advances the status-bar usage meter to the next window in the active
 * adapter's `UsageSnapshot` (e.g. "This session" → "Today" → "This month")
 * and persists the selection in workspace state so it survives a window
 * reload.
 *
 * Reassigns the usage item's click gesture from opening the dashboard to
 * cycling — "Open Dashboard" remains reachable via a command link in the
 * item's tooltip (`buildUsageTooltip` in `../utils/statusBar`).
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #659 - Status bar usage meter with click-to-cycle metrics
 */

import * as vscode from "vscode";
import type { StatusBarManager } from "../utils/statusBar";

/**
 * Workspace-state key the selected usage window id is persisted under.
 * Read back in `bootstrap/services.ts` on activation, before the first
 * `UsageSnapshot` arrives, via `StatusBarManager.setSelectedUsageWindowId`.
 */
export const USAGE_WINDOW_STATE_KEY = "nightgauge.usageWindowId";

/**
 * Register the `nightgauge.cycleUsageMetric` command.
 *
 * Safe to invoke with no snapshot yet, or an unknown/empty snapshot —
 * `StatusBarManager.cycleUsageWindow()` returns `null` in both cases and
 * nothing is persisted.
 */
export function registerCycleUsageMetricCommand(
  context: vscode.ExtensionContext,
  statusBar: StatusBarManager
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.cycleUsageMetric", () => {
    const selectedId = statusBar.cycleUsageWindow();
    if (selectedId !== null) {
      void context.workspaceState.update(USAGE_WINDOW_STATE_KEY, selectedId);
    }
  });
}
