/**
 * Show Dashboard command
 *
 * Opens the Nightgauge Pipeline dashboard webview with metrics,
 * token usage charts, tool call logs, and pipeline history.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 */

import * as vscode from "vscode";
import type { Dashboard } from "../views";
import type { Logger } from "../utils/logger";

/**
 * Register the Show Dashboard command
 *
 * Opens the dashboard WebView panel. The dashboard subscribes to
 * PipelineStateService for real-time pipeline progress updates.
 */
export function registerShowDashboardCommand(
  dashboard: Dashboard,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.showDashboard", () => {
    logger.info("Opening dashboard");
    dashboard.show();
  });
}

/**
 * Register the Rescrub Dashboard History command
 *
 * Clears existing dashboard history and rebuilds from all pipeline
 * artifacts on disk (state files + JSONL history records). This primes
 * the dashboard with accurate historical token and cost data.
 */
export function registerRescrubDashboardCommand(
  dashboard: Dashboard,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.rescrubDashboardHistory", async () => {
    logger.info("Rescrubbing dashboard history from pipeline artifacts");
    const imported = await dashboard.rescrubHistory();
    vscode.window.showInformationMessage(
      `Dashboard history rebuilt: ${imported} pipeline run${imported !== 1 ? "s" : ""} imported from disk artifacts.`
    );
  });
}
