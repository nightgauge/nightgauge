/**
 * Run Pipeline Health Check command
 *
 * Registers a VSCode command that prompts for period/severity presets via
 * quick pick, runs PipelineHealthRunner with progress reporting, and displays
 * results in the Dashboard health report section.
 *
 * @see Issue #1104 - Pipeline Health VSCode Command & Dashboard Integration
 * @see docs/ARCHITECTURE.md for command registration patterns
 */

import * as vscode from "vscode";
import { PipelineHealthRunner } from "../services/PipelineHealthRunner";
import type { HealthCheckParams, HealthSeverity } from "../types/pipelineHealth";
import type { Dashboard } from "../views/dashboard/Dashboard";
import type { Logger } from "../utils/logger";

/**
 * Prompt user for analysis period via quick pick.
 * Returns null if cancelled.
 */
async function pickPeriod(): Promise<number | null> {
  const pick = await vscode.window.showQuickPick(
    [
      { label: "Last 7 days", value: 7 },
      { label: "Last 30 days", value: 30 },
      { label: "Last 90 days", value: 90 },
    ],
    { placeHolder: "Select analysis period", title: "Pipeline Health Check" }
  );

  return pick?.value ?? null;
}

/**
 * Prompt user for severity filter via quick pick.
 * Returns null if cancelled.
 */
async function pickSeverity(): Promise<HealthSeverity | null> {
  const pick = await vscode.window.showQuickPick(
    [
      {
        label: "All findings",
        description: "Include informational findings",
        value: "info" as HealthSeverity,
      },
      {
        label: "Warnings and above",
        description: "Skip informational findings",
        value: "warning" as HealthSeverity,
      },
      {
        label: "High and critical only",
        description: "Only actionable findings",
        value: "high" as HealthSeverity,
      },
    ],
    { placeHolder: "Select minimum severity", title: "Finding Severity Filter" }
  );

  return pick?.value ?? null;
}

/**
 * Register the Run Pipeline Health Check command.
 *
 * @param workspaceRoot - Workspace root for data aggregation
 * @param logger - Logger instance
 * @param dashboard - Dashboard instance for displaying results
 */
export function registerRunPipelineHealthCommand(
  workspaceRoot: string,
  logger: Logger,
  dashboard: Dashboard
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.runPipelineHealth", async () => {
    logger.info("Pipeline health check started");

    const period = await pickPeriod();
    if (period === null) return;

    const severity = await pickSeverity();
    if (severity === null) return;

    const params: HealthCheckParams = {
      period,
      severity,
      dryRun: false,
    };

    try {
      const report = await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Pipeline Health Check",
          cancellable: false,
        },
        async (progress) => {
          progress.report({ message: "Aggregating data..." });
          // Small delay for UI feedback
          await new Promise((r) => setTimeout(r, 100));

          progress.report({ message: "Running analyzers..." });
          const result = await PipelineHealthRunner.run(workspaceRoot, params);

          progress.report({ message: "Generating report..." });
          return result;
        }
      );

      // Set the report on the dashboard and show it
      dashboard.setHealthCheckReport(report);
      dashboard.show();

      // Summary notification
      const highCount = report.findings_by_severity.critical + report.findings_by_severity.high;
      const warnCount = report.findings_by_severity.warning;
      vscode.window.showInformationMessage(
        `Pipeline health check complete: ${report.findings.length} findings (${highCount} high/critical, ${warnCount} warnings)`
      );

      logger.info("Pipeline health check complete", {
        period: params.period,
        findingsCount: report.findings.length,
        highCount,
        warnCount,
      });
    } catch (error) {
      const msg = error instanceof Error ? error.message : "Unknown error";
      logger.error("Pipeline health check failed", error as Error);
      vscode.window.showErrorMessage(`Pipeline health check failed: ${msg}`);
    }
  });
}
