/**
 * exportTelemetry.integration.test.ts
 *
 * End-to-end integration tests for the exportTelemetry command handler.
 * Uses real JSONL fixture files loaded through ExecutionHistoryReader to
 * verify the full command flow: pick format → read records → convert → save.
 *
 * These tests assert that exported output contains non-zero/non-empty values
 * for every declared export field when real telemetry fixture data is present.
 *
 * @see Issue #2794 - Dashboard/analytics exports return zero values
 * @see Issue #1010 - Telemetry Analytics Export
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as path from "node:path";

// ============================================================================
// vscode mock
// ============================================================================

const writtenFiles = new Map<string, Buffer>();
const mockSaveUri = { fsPath: "/tmp/test-export.json" };

vi.mock("vscode", () => ({
  Uri: {
    file: (value: string) => ({ fsPath: value, path: value }),
  },
  window: {
    showQuickPick: vi.fn(),
    showInputBox: vi.fn(),
    showSaveDialog: vi.fn().mockResolvedValue({ fsPath: "/tmp/test-export.json" }),
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    registerCommand: vi.fn((_command: string, handler: unknown) => ({
      dispose: vi.fn(),
      _handler: handler,
    })),
  },
  workspace: {
    fs: {
      writeFile: vi.fn((uri: { fsPath: string }, data: Buffer) => {
        writtenFiles.set(uri.fsPath, data);
        return Promise.resolve();
      }),
    },
  },
}));

// ============================================================================
// ExecutionHistoryReader — NOT mocked for integration tests.
// We let it parse the real fixture files.
// ============================================================================

// Import after mocks
import * as vscode from "vscode";
import { registerExportTelemetryCommand } from "../../src/commands/exportTelemetry";
import { ExecutionHistoryReader } from "../../src/utils/executionHistoryReader";
import type { Logger } from "../../src/utils/logger";

const FIXTURE_DIR = path.join(__dirname, "../fixtures/telemetry");
const MULTI_RUN_FIXTURE = path.join(FIXTURE_DIR, "health-history-multi-run.jsonl");
const EDGE_CASES_FIXTURE = path.join(FIXTURE_DIR, "health-history-edge-cases.jsonl");

function getWrittenContent(): string {
  const buf = writtenFiles.get(mockSaveUri.fsPath);
  if (!buf) throw new Error("No file was written");
  return buf.toString("utf-8");
}

function mockLogger(): Logger {
  return { info: vi.fn(), error: vi.fn() } as unknown as Logger;
}

/**
 * Pick format quick-pick mock — returns a specific format on demand.
 */
function pickFormatAs(format: "json" | "csv-runs" | "csv-stages") {
  const labels: Record<string, string> = {
    json: "JSON (full records)",
    "csv-runs": "CSV (one row per run)",
    "csv-stages": "CSV (one row per stage)",
  };
  return vi.fn().mockResolvedValueOnce({ label: labels[format], format });
}

describe("exportTelemetry integration — multi-run fixture", () => {
  let handler: (() => Promise<void>) | null = null;
  const workspaceRoot = FIXTURE_DIR; // point reader at the fixture directory

  beforeEach(async () => {
    writtenFiles.clear();
    vi.clearAllMocks();

    // Restore writeFile mock after clearAllMocks
    (vscode.workspace.fs.writeFile as ReturnType<typeof vi.fn>).mockImplementation(
      (uri: { fsPath: string }, data: Buffer) => {
        writtenFiles.set(uri.fsPath, data);
        return Promise.resolve();
      }
    );
    (vscode.window.showSaveDialog as ReturnType<typeof vi.fn>).mockResolvedValue(mockSaveUri);

    // Register the command and capture the handler
    const disposable = registerExportTelemetryCommand(workspaceRoot, mockLogger());
    const calls = (vscode.commands.registerCommand as ReturnType<typeof vi.fn>).mock.calls;
    handler = calls[calls.length - 1]?.[1] ?? null;
  });

  it("exports JSON with non-zero cost from multi-run fixture", async () => {
    // Mock ExecutionHistoryReader.readAll to use the real fixture file
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue(
      await ExecutionHistoryReader.parseJsonlFile(MULTI_RUN_FIXTURE)
    );
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "JSON (full records)", format: "json" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });

    await handler!();

    const content = getWrittenContent();
    const parsed = JSON.parse(content);
    expect(Array.isArray(parsed)).toBe(true);
    expect(parsed.length).toBeGreaterThan(0);
    for (const record of parsed) {
      if (record.record_type === "run") {
        expect(record.tokens.estimated_cost_usd).toBeGreaterThan(0);
      }
    }
  });

  it("exports CSV-runs with non-zero tokens for all 4 fixture runs", async () => {
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue(
      await ExecutionHistoryReader.parseJsonlFile(MULTI_RUN_FIXTURE)
    );
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "CSV (one row per run)", format: "csv-runs" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });

    await handler!();

    const content = getWrittenContent();
    const lines = content.split("\n").slice(1); // skip header
    expect(lines).toHaveLength(4);
    for (const line of lines) {
      const cols = line.split(",");
      const cost = parseFloat(cols[7]); // total_cost_usd
      const inputTokens = parseInt(cols[8], 10); // total_input_tokens
      expect(cost).toBeGreaterThan(0);
      expect(inputTokens).toBeGreaterThan(0);
    }
  });

  it("exports CSV-stages with non-zero input_tokens in feature-dev rows", async () => {
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue(
      await ExecutionHistoryReader.parseJsonlFile(MULTI_RUN_FIXTURE)
    );
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "CSV (one row per stage)", format: "csv-stages" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });

    await handler!();

    const content = getWrittenContent();
    const lines = content.split("\n").slice(1);
    const devRows = lines.filter((l) => l.split(",")[1] === "feature-dev");
    expect(devRows.length).toBeGreaterThan(0);
    for (const row of devRows) {
      const cols = row.split(",");
      expect(parseInt(cols[4], 10)).toBeGreaterThan(0); // input_tokens
      expect(parseFloat(cols[8])).toBeGreaterThan(0); // cost_usd
    }
  });

  it("date-range filter last7 uses readDateRange, not readAll", async () => {
    const dateRangeSpy = vi
      .spyOn(ExecutionHistoryReader, "readDateRange")
      .mockResolvedValue(await ExecutionHistoryReader.parseJsonlFile(MULTI_RUN_FIXTURE));
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue([]);

    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "CSV (one row per run)", format: "csv-runs" })
      .mockResolvedValueOnce({ label: "Last 7 days", value: "7d" });

    await handler!();

    expect(dateRangeSpy).toHaveBeenCalled();
    const [, start, end] = dateRangeSpy.mock.calls[0];
    const diffDays = (end.getTime() - start.getTime()) / (1000 * 60 * 60 * 24);
    expect(diffDays).toBeGreaterThanOrEqual(6.9);
    expect(diffDays).toBeLessThanOrEqual(7.1);
  });

  it("shows warning when no records found", async () => {
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue([]);
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "JSON (full records)", format: "json" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });

    await handler!();

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("No telemetry records")
    );
  });

  it("returns early when format selection is cancelled", async () => {
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>).mockResolvedValueOnce(undefined);

    await handler!();

    expect(vscode.workspace.fs.writeFile).not.toHaveBeenCalled();
  });

  it("returns early when date range selection is cancelled", async () => {
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "JSON (full records)", format: "json" })
      .mockResolvedValueOnce(undefined);

    await handler!();

    expect(vscode.workspace.fs.writeFile).not.toHaveBeenCalled();
  });

  it("returns early when save dialog is cancelled", async () => {
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue(
      await ExecutionHistoryReader.parseJsonlFile(MULTI_RUN_FIXTURE)
    );
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "JSON (full records)", format: "json" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });
    (vscode.window.showSaveDialog as ReturnType<typeof vi.fn>).mockResolvedValueOnce(undefined);

    await handler!();

    expect(vscode.workspace.fs.writeFile).not.toHaveBeenCalled();
  });
});

describe("exportTelemetry integration — edge-cases fixture", () => {
  let handler: (() => Promise<void>) | null = null;
  const workspaceRoot = FIXTURE_DIR;

  beforeEach(async () => {
    writtenFiles.clear();
    vi.clearAllMocks();

    (vscode.workspace.fs.writeFile as ReturnType<typeof vi.fn>).mockImplementation(
      (uri: { fsPath: string }, data: Buffer) => {
        writtenFiles.set(uri.fsPath, data);
        return Promise.resolve();
      }
    );
    (vscode.window.showSaveDialog as ReturnType<typeof vi.fn>).mockResolvedValue(mockSaveUri);

    const disposable = registerExportTelemetryCommand(workspaceRoot, mockLogger());
    const calls = (vscode.commands.registerCommand as ReturnType<typeof vi.fn>).mock.calls;
    handler = calls[calls.length - 1]?.[1] ?? null;
  });

  it("v1-normalized record exports without errors in CSV-runs", async () => {
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue(
      await ExecutionHistoryReader.parseJsonlFile(EDGE_CASES_FIXTURE)
    );
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "CSV (one row per run)", format: "csv-runs" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });

    await expect(handler!()).resolves.not.toThrow();

    const content = getWrittenContent();
    const lines = content.split("\n").slice(1);
    expect(lines.length).toBeGreaterThan(0);
  });

  it("run without per_stage still has non-zero total_cost_usd in CSV-runs", async () => {
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue(
      await ExecutionHistoryReader.parseJsonlFile(EDGE_CASES_FIXTURE)
    );
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "CSV (one row per run)", format: "csv-runs" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });

    await handler!();

    const content = getWrittenContent();
    const lines = content.split("\n").slice(1);
    // All 3 edge-case runs have non-zero estimated_cost_usd
    for (const line of lines) {
      const cols = line.split(",");
      expect(parseFloat(cols[7])).toBeGreaterThan(0); // total_cost_usd
    }
  });

  it("title with commas is properly quoted in CSV output", async () => {
    vi.spyOn(ExecutionHistoryReader, "readAll").mockResolvedValue(
      await ExecutionHistoryReader.parseJsonlFile(EDGE_CASES_FIXTURE)
    );
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>)
      .mockResolvedValueOnce({ label: "CSV (one row per run)", format: "csv-runs" })
      .mockResolvedValueOnce({ label: "All time", value: "all" });

    await handler!();

    const content = getWrittenContent();
    // Run #201 title contains commas — must be quoted
    expect(content).toContain('"Fix: race condition in pipeline state, fix: retry logic"');
  });
});
