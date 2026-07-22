/**
 * Export Telemetry Analytics command
 *
 * Exports pipeline telemetry data (multiple runs) in CSV or JSON format
 * for external analysis in spreadsheets, BI dashboards, or custom scripts.
 *
 * Supports date range filtering (last 7d, 30d, all time, custom range)
 * and three export formats: JSON, CSV runs, CSV stages.
 *
 * @see Issue #1010 - Telemetry Analytics Export
 * @see docs/ARCHITECTURE.md for command registration patterns
 */

import * as vscode from "vscode";
import { ExecutionHistoryReader } from "../utils/executionHistoryReader";
import {
  exportAsJson,
  exportAsCsvRuns,
  exportAsCsvStages,
  type ExportFormat,
} from "../utils/telemetryExporter";
import type { Logger } from "../utils/logger";

interface DateRange {
  start: Date;
  end: Date;
}

/**
 * Prompt user for export format via quick pick.
 * Returns null if cancelled.
 */
async function pickFormat(): Promise<ExportFormat | null> {
  const pick = await vscode.window.showQuickPick(
    [
      {
        label: "JSON (full records)",
        description: "Complete JSONL records as a JSON array",
        format: "json" as ExportFormat,
      },
      {
        label: "CSV (one row per run)",
        description: "Run-level summary for BI dashboards",
        format: "csv-runs" as ExportFormat,
      },
      {
        label: "CSV (one row per stage)",
        description: "Stage-level breakdown for cost analysis",
        format: "csv-stages" as ExportFormat,
      },
    ],
    { placeHolder: "Select export format", title: "Export Telemetry Analytics" }
  );

  return pick?.format ?? null;
}

/**
 * Prompt user for date range via quick pick.
 * Returns null if cancelled.
 */
async function pickDateRange(): Promise<DateRange | "all" | null> {
  const pick = await vscode.window.showQuickPick(
    [
      { label: "Last 7 days", value: "7d" },
      { label: "Last 30 days", value: "30d" },
      { label: "All time", value: "all" },
      { label: "Custom range...", value: "custom" },
    ],
    { placeHolder: "Select date range", title: "Export Date Range" }
  );

  if (!pick) return null;

  if (pick.value === "all") return "all";

  if (pick.value === "7d") {
    const end = new Date();
    const start = new Date();
    start.setDate(start.getDate() - 7);
    return { start, end };
  }

  if (pick.value === "30d") {
    const end = new Date();
    const start = new Date();
    start.setDate(start.getDate() - 30);
    return { start, end };
  }

  // Custom range
  const startStr = await vscode.window.showInputBox({
    prompt: "Start date (YYYY-MM-DD)",
    placeHolder: "e.g., 2026-01-01",
    validateInput: (v) => (/^\d{4}-\d{2}-\d{2}$/.test(v) ? null : "Use YYYY-MM-DD format"),
  });
  if (!startStr) return null;

  const endStr = await vscode.window.showInputBox({
    prompt: "End date (YYYY-MM-DD)",
    placeHolder: "e.g., 2026-02-19",
    validateInput: (v) => (/^\d{4}-\d{2}-\d{2}$/.test(v) ? null : "Use YYYY-MM-DD format"),
  });
  if (!endStr) return null;

  return {
    start: new Date(startStr + "T00:00:00Z"),
    end: new Date(endStr + "T23:59:59Z"),
  };
}

/**
 * Get file extension for export format.
 */
function getExtension(format: ExportFormat): string {
  return format === "json" ? "json" : "csv";
}

/**
 * Register the Export Telemetry Analytics command.
 *
 * @param workspaceRoot - Workspace root for reading JSONL files
 * @param logger - Logger instance
 */
export function registerExportTelemetryCommand(
  workspaceRoot: string,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.exportTelemetry", async () => {
    logger.info("Export telemetry analytics started");

    const format = await pickFormat();
    if (!format) return;

    const dateRange = await pickDateRange();
    if (dateRange === null) return;

    try {
      // Read records
      const records =
        dateRange === "all"
          ? await ExecutionHistoryReader.readAll(workspaceRoot)
          : await ExecutionHistoryReader.readDateRange(
              workspaceRoot,
              dateRange.start,
              dateRange.end
            );

      if (records.length === 0) {
        vscode.window.showWarningMessage("No telemetry records found for the selected date range.");
        return;
      }

      // Convert to export format
      let content: string;
      switch (format) {
        case "json":
          content = exportAsJson(records);
          break;
        case "csv-runs":
          content = exportAsCsvRuns(records);
          break;
        case "csv-stages":
          content = exportAsCsvStages(records);
          break;
      }

      const ext = getExtension(format);
      const defaultName = `telemetry-export.${ext}`;

      // Show save dialog
      const uri = await vscode.window.showSaveDialog({
        defaultUri: vscode.Uri.file(defaultName),
        filters: {
          [ext.toUpperCase()]: [ext],
        },
      });

      if (uri) {
        await vscode.workspace.fs.writeFile(uri, Buffer.from(content, "utf-8"));
        vscode.window.showInformationMessage(`Exported ${records.length} records to ${uri.fsPath}`);
        logger.info("Export telemetry complete", {
          format,
          recordCount: records.length,
          path: uri.fsPath,
        });
      }
    } catch (error) {
      const msg = error instanceof Error ? error.message : "Unknown error";
      logger.error("Export telemetry failed", error as Error);
      vscode.window.showErrorMessage(`Failed to export telemetry: ${msg}`);
    }
  });
}
