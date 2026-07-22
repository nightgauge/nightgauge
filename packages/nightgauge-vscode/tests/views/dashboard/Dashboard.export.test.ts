/**
 * Dashboard.export.test.ts
 *
 * Integration tests for Dashboard export handlers: handleExport and
 * handleExportAnalytics.
 *
 * Regression tests for issue #2794 — history run exports returned zero values
 * in per-stage CSV rows because PipelineRunSummary stages lack tokenUsage for
 * history runs loaded from the TelemetryStore index. The fix enriches stages
 * from the full JSONL record before exporting.
 *
 * @see Issue #2794 - Dashboard/analytics exports return zero values
 * @see Issue #1010 - Telemetry Analytics Export
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import * as vscode from "vscode";

// ============================================================================
// vscode mock
// ============================================================================

const writtenFiles = new Map<string, Buffer>();

vi.mock("vscode", () => ({
  EventEmitter: class {
    private listeners: ((data: unknown) => void)[] = [];
    get event() {
      return (listener: (data: unknown) => void) => {
        this.listeners.push(listener);
        return { dispose: () => {} };
      };
    }
    fire(data: unknown) {
      this.listeners.forEach((l) => l(data));
    }
    dispose = vi.fn();
  },
  Uri: {
    joinPath: vi.fn((uri: unknown, ...parts: string[]) => ({
      fsPath: `/mock/${parts.join("/")}`,
    })),
    file: vi.fn((path: string) => ({ fsPath: path })),
  },
  ViewColumn: { One: 1 },
  window: {
    createWebviewPanel: vi.fn(() => ({
      webview: {
        html: "",
        onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
        postMessage: vi.fn(),
      },
      reveal: vi.fn(),
      onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
      visible: true,
    })),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    showSaveDialog: vi.fn().mockResolvedValue({ fsPath: "/tmp/test-export.csv" }),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn().mockReturnValue(undefined) })),
    fs: {
      writeFile: vi.fn((uri: { fsPath: string }, data: Buffer) => {
        writtenFiles.set(uri.fsPath, data);
        return Promise.resolve();
      }),
    },
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
  },
  RelativePattern: vi.fn(),
  env: {
    openExternal: vi.fn().mockResolvedValue(undefined),
  },
}));

// ============================================================================
// PipelineStateService mock
// ============================================================================

vi.mock("../../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    getInstance: vi.fn(() => ({
      onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
      onStageStart: vi.fn(() => ({ dispose: vi.fn() })),
      onStageComplete: vi.fn(() => ({ dispose: vi.fn() })),
      onStageError: vi.fn(() => ({ dispose: vi.fn() })),
      onPhaseStart: vi.fn(() => ({ dispose: vi.fn() })),
      onPhaseComplete: vi.fn(() => ({ dispose: vi.fn() })),
      onTokenUsageUpdated: vi.fn(() => ({ dispose: vi.fn() })),
      onToolCallRecorded: vi.fn(() => ({ dispose: vi.fn() })),
      onBacktrackTriggered: vi.fn(() => ({ dispose: vi.fn() })),
      onBacktrackBlocked: vi.fn(() => ({ dispose: vi.fn() })),
      onModelEscalated: vi.fn(() => ({ dispose: vi.fn() })),
      onHistoryRecorded: vi.fn(() => ({ dispose: vi.fn() })),
      getState: vi.fn().mockResolvedValue(null),
    })),
    resetInstance: vi.fn(),
  },
}));

// ============================================================================
// TelemetryStore mock
// ============================================================================

const mockGetRunRecord = vi.fn();

vi.mock("../../../src/services/TelemetryStore", () => ({
  TelemetryStore: vi.fn().mockImplementation(() => ({
    getRunRecord: mockGetRunRecord,
    getAllRunSummaries: vi.fn().mockResolvedValue([]),
    getRunSummariesPaginated: vi.fn().mockResolvedValue({ items: [], total: 0, hasMore: false }),
    appendRunRecord: vi.fn().mockResolvedValue(true),
    rebuildIndex: vi.fn().mockResolvedValue(undefined),
    isIndexStale: vi.fn().mockResolvedValue(false),
    cleanupOldFiles: vi.fn().mockResolvedValue({ deleted: [] }),
  })),
  isGhostEntry: vi.fn().mockReturnValue(false),
}));

// ============================================================================
// ExecutionHistoryReader mock
// ============================================================================

vi.mock("../../../src/utils/executionHistoryReader", () => ({
  ExecutionHistoryReader: {
    readAll: vi.fn().mockResolvedValue([]),
    readDateRange: vi.fn().mockResolvedValue([]),
    readForIssue: vi.fn().mockResolvedValue([]),
    listHistoryFiles: vi.fn().mockResolvedValue([]),
    parseJsonlFile: vi.fn().mockResolvedValue([]),
    getCostByIssue: vi.fn().mockResolvedValue([]),
    getFocusLensComparison: vi.fn().mockResolvedValue(null),
    clearCache: vi.fn(),
  },
}));

import { Dashboard } from "../../../src/views/dashboard/Dashboard";
import { DashboardState } from "../../../src/views/dashboard/DashboardState";
import { TelemetryStore } from "../../../src/services/TelemetryStore";
import { ExecutionHistoryReader } from "../../../src/utils/executionHistoryReader";
import type { ExecutionHistoryRunRecordV2 } from "../../../src/schemas/executionHistory";
import type { PipelineRunSummary } from "../../../src/views/dashboard/DashboardState";

// ============================================================================
// Test fixtures
// ============================================================================

const makeFullRecord = (issueNumber = 100): ExecutionHistoryRunRecordV2 => ({
  schema_version: "2",
  record_type: "run",
  issue_number: issueNumber,
  title: `Issue #${issueNumber} — test feature`,
  branch: `feat/${issueNumber}-test`,
  base_branch: "main",
  execution_mode: "automatic",
  started_at: "2026-04-01T09:00:00.000Z",
  completed_at: "2026-04-01T09:42:00.000Z",
  total_duration_ms: 2520000,
  outcome: "complete",
  outcome_type: "productive",
  size: "M",
  type: "feature",
  priority: "high",
  labels: ["size:M", "type:feature", "priority:high"],
  stages: {
    "issue-pickup": { status: "complete", duration_ms: 300000 },
    "feature-planning": { status: "complete", duration_ms: 780000 },
    "feature-dev": { status: "complete", duration_ms: 1200000 },
    "feature-validate": { status: "complete", duration_ms: 120000 },
    "pr-create": { status: "complete", duration_ms: 60000 },
    "pr-merge": { status: "complete", duration_ms: 60000 },
  },
  tokens: {
    total_input: 68500,
    total_output: 18200,
    total_cache_read: 31000,
    total_cache_creation: 12500,
    estimated_cost_usd: 0.18432,
    per_stage: {
      "issue-pickup": {
        input: 4200,
        output: 850,
        cache_read: 1800,
        cache_creation: 600,
        cost_usd: 0.0078,
        model: "claude-haiku-4-5",
        model_source: "auto",
      },
      "feature-planning": {
        input: 18500,
        output: 5200,
        cache_read: 9000,
        cache_creation: 3800,
        cost_usd: 0.0525,
        model: "claude-sonnet-4-6",
        model_source: "config",
      },
      "feature-dev": {
        input: 38000,
        output: 10500,
        cache_read: 18000,
        cache_creation: 7500,
        cost_usd: 0.1134,
        model: "claude-sonnet-4-6",
        model_source: "config",
      },
      "feature-validate": {
        input: 4200,
        output: 900,
        cache_read: 1500,
        cache_creation: 400,
        cost_usd: 0.0082,
        model: "claude-haiku-4-5",
        model_source: "auto",
      },
      "pr-create": {
        input: 2800,
        output: 600,
        cache_read: 600,
        cache_creation: 150,
        cost_usd: 0.0064,
        model: "claude-haiku-4-5",
        model_source: "auto",
      },
      "pr-merge": {
        input: 800,
        output: 150,
        cache_read: 100,
        cache_creation: 50,
        cost_usd: 0.00202,
        model: "claude-haiku-4-5",
        model_source: "default",
      },
    },
  },
  files: { read_count: 18, written_count: 5 },
  routing: { complexity_score: 5, path: "standard", skip_stages: [] },
  recorded_at: "2026-04-01T09:42:00.000Z",
});

/**
 * History run as loaded from the TelemetryStore index — stages have NO
 * tokenUsage. This reproduces the pre-fix state that caused zero values.
 */
function makeHistoryRunWithEmptyStages(issueNumber = 100): PipelineRunSummary {
  return {
    issueNumber,
    title: `Issue #${issueNumber} — test feature`,
    branch: `feat/${issueNumber}-test`,
    startedAt: new Date("2026-04-01T09:00:00.000Z"),
    completedAt: new Date("2026-04-01T09:42:00.000Z"),
    status: "complete",
    stages: [
      { stage: "issue-pickup", status: "complete", durationMs: 300000 },
      { stage: "feature-planning", status: "complete", durationMs: 780000 },
      { stage: "feature-dev", status: "complete", durationMs: 1200000 },
      { stage: "feature-validate", status: "complete", durationMs: 120000 },
      { stage: "pr-create", status: "complete", durationMs: 60000 },
      { stage: "pr-merge", status: "complete", durationMs: 60000 },
    ],
    usage: {
      inputTokens: 68500,
      outputTokens: 18200,
      cacheReadTokens: 31000,
      cacheCreationTokens: 12500,
      costUsd: 0.18432,
      durationMs: 2520000,
      stageCount: 6,
    },
    toolCalls: [],
  };
}

// ============================================================================
// Helpers
// ============================================================================

type DashboardPrivate = {
  handleExport(format: "json" | "csv", target: "current" | number): Promise<void>;
  handleExportAnalytics(format: string, dateRange: "last7" | "last30" | "all"): Promise<void>;
  workspaceRoot: string | undefined;
  state: DashboardState;
};

function asDashboardPrivate(d: Dashboard): DashboardPrivate {
  return d as unknown as DashboardPrivate;
}

function getWrittenContent(): string {
  const buf = writtenFiles.get("/tmp/test-export.csv");
  if (!buf) throw new Error("No file written to /tmp/test-export.csv");
  return buf.toString("utf-8");
}

// ============================================================================
// DashboardState.exportAsCsv — zero-value baseline (pre-fix behavior)
// ============================================================================

describe("DashboardState.exportAsCsv — REGRESSION baseline (#2794)", () => {
  it("stage rows have zero input tokens when tokenUsage is absent (pre-fix behavior)", () => {
    const state = new DashboardState(createMockMemento());
    const run = makeHistoryRunWithEmptyStages();
    const csv = state.exportAsCsv(run);
    const lines = csv.split("\n");
    const featureDevRow = lines.find((l) => l.split(",")[0] === "feature-dev");
    expect(featureDevRow).toBeDefined();
    const cols = featureDevRow!.split(",");
    // Without the fix, input tokens (col 2) would be 0 — this documents the bug
    expect(parseInt(cols[2], 10)).toBe(0);
  });
});

// ============================================================================
// Dashboard.handleExport — post-fix behavior
// ============================================================================

describe("Dashboard.handleExport — post-fix (#2794)", () => {
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;
  let dashboard: Dashboard;

  beforeEach(() => {
    writtenFiles.clear();
    mockGetRunRecord.mockReset();

    // Re-apply writeFile mock (clearAllMocks would strip it)
    vi.mocked(vscode.workspace.fs.writeFile).mockImplementation(
      (uri: { fsPath: string }, data: Buffer) => {
        writtenFiles.set(uri.fsPath, data);
        return Promise.resolve();
      }
    );
    vi.mocked(vscode.window.showSaveDialog).mockResolvedValue({ fsPath: "/tmp/test-export.csv" });

    const workspaceState = createMockMemento();
    // Build a plain mock object rather than using new TelemetryStore() (mock is not constructable)
    const mockTelemetryStore = {
      getRunRecord: mockGetRunRecord,
      getAllRunSummaries: vi.fn().mockResolvedValue([]),
      getRunSummariesPaginated: vi.fn().mockResolvedValue({ items: [], total: 0, hasMore: false }),
      appendRunRecord: vi.fn().mockResolvedValue(true),
      rebuildIndex: vi.fn().mockResolvedValue(undefined),
      isIndexStale: vi.fn().mockResolvedValue(false),
      cleanupOldFiles: vi.fn().mockResolvedValue({ deleted: [] }),
    } as unknown as TelemetryStore;
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot, mockTelemetryStore);

    // Seed history with a run that has empty stages
    asDashboardPrivate(dashboard).state["history"] = [makeHistoryRunWithEmptyStages(100)];
  });

  it("history run CSV has non-zero input tokens in feature-dev row (fix applied)", async () => {
    mockGetRunRecord.mockResolvedValue(makeFullRecord(100));

    await asDashboardPrivate(dashboard).handleExport("csv", 100);

    const content = getWrittenContent();
    const lines = content.split("\n");
    const featureDevRow = lines.find((l) => l.split(",")[0] === "feature-dev");
    expect(featureDevRow).toBeDefined();
    const cols = featureDevRow!.split(",");
    // After fix: input tokens (col 2) should be populated from per_stage
    expect(parseInt(cols[2], 10)).toBe(38000);
    expect(parseFloat(cols[6])).toBeGreaterThan(0); // cost_usd
  });

  it("history run CSV has non-zero output tokens in feature-planning row", async () => {
    mockGetRunRecord.mockResolvedValue(makeFullRecord(100));

    await asDashboardPrivate(dashboard).handleExport("csv", 100);

    const content = getWrittenContent();
    const lines = content.split("\n");
    const planningRow = lines.find((l) => l.split(",")[0] === "feature-planning");
    expect(planningRow).toBeDefined();
    const cols = planningRow!.split(",");
    expect(parseInt(cols[3], 10)).toBe(5200); // output tokens
  });

  it("history run JSON export includes tokenUsage in serialized stages", async () => {
    mockGetRunRecord.mockResolvedValue(makeFullRecord(100));

    await asDashboardPrivate(dashboard).handleExport("json", 100);

    const content = getWrittenContent();
    const parsed = JSON.parse(content);
    const featureDevStage = parsed.stages?.find(
      (s: { stage: string }) => s.stage === "feature-dev"
    );
    expect(featureDevStage).toBeDefined();
    expect(featureDevStage.tokenUsage).toBeDefined();
    expect(featureDevStage.tokenUsage.inputTokens).toBe(38000);
    expect(featureDevStage.tokenUsage.costUsd).toBeGreaterThan(0);
  });

  it("current run export does NOT call getRunRecord (live tokenUsage used instead)", async () => {
    asDashboardPrivate(dashboard).state.startRun(42, "Live run", "feat/42-live");

    await asDashboardPrivate(dashboard).handleExport("csv", "current");

    expect(mockGetRunRecord).not.toHaveBeenCalled();
  });

  it("shows warning when no run found for requested issueNumber", async () => {
    await asDashboardPrivate(dashboard).handleExport("csv", 9999);

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("No pipeline data to export.");
  });

  it("falls back gracefully when TelemetryStore.getRunRecord returns undefined", async () => {
    mockGetRunRecord.mockResolvedValue(undefined);

    // Should not throw — falls back to DashboardState export with empty stages
    await expect(asDashboardPrivate(dashboard).handleExport("csv", 100)).resolves.not.toThrow();
  });

  it("saves file to the URI from showSaveDialog", async () => {
    mockGetRunRecord.mockResolvedValue(makeFullRecord(100));

    await asDashboardPrivate(dashboard).handleExport("csv", 100);

    expect(vscode.workspace.fs.writeFile).toHaveBeenCalledWith(
      { fsPath: "/tmp/test-export.csv" },
      expect.any(Buffer)
    );
  });
});

// ============================================================================
// Dashboard.handleExportAnalytics
// ============================================================================

describe("Dashboard.handleExportAnalytics", () => {
  const workspaceRoot = "/test/workspace";
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;
  let dashboard: Dashboard;

  beforeEach(() => {
    writtenFiles.clear();
    vi.clearAllMocks();

    vi.mocked(vscode.workspace.fs.writeFile).mockImplementation(
      (uri: { fsPath: string }, data: Buffer) => {
        writtenFiles.set(uri.fsPath, data);
        return Promise.resolve();
      }
    );
    vi.mocked(vscode.window.showSaveDialog).mockResolvedValue({ fsPath: "/tmp/test-export.csv" });

    const workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, workspaceRoot);
  });

  it("exports all-time CSV runs with non-zero cost values", async () => {
    (ExecutionHistoryReader.readAll as ReturnType<typeof vi.fn>).mockResolvedValue([
      makeFullRecord(100),
      makeFullRecord(101),
    ]);

    await asDashboardPrivate(dashboard).handleExportAnalytics("csv-runs", "all");

    const content = getWrittenContent();
    const lines = content.split("\n").slice(1);
    expect(lines).toHaveLength(2);
    for (const line of lines) {
      const cols = line.split(",");
      expect(parseFloat(cols[7])).toBeGreaterThan(0); // total_cost_usd
      expect(parseInt(cols[8], 10)).toBeGreaterThan(0); // total_input_tokens
    }
  });

  it("exports stage CSV with non-zero per-stage input tokens", async () => {
    (ExecutionHistoryReader.readAll as ReturnType<typeof vi.fn>).mockResolvedValue([
      makeFullRecord(100),
    ]);

    await asDashboardPrivate(dashboard).handleExportAnalytics("csv-stages", "all");

    const content = getWrittenContent();
    const lines = content.split("\n").slice(1);
    const devRow = lines.find((l) => l.split(",")[1] === "feature-dev");
    expect(devRow).toBeDefined();
    const cols = devRow!.split(",");
    expect(parseInt(cols[4], 10)).toBe(38000); // input_tokens from fixture
    expect(parseFloat(cols[8])).toBeGreaterThan(0); // cost_usd
  });

  it("shows warning when no records in date range", async () => {
    (ExecutionHistoryReader.readAll as ReturnType<typeof vi.fn>).mockResolvedValue([]);

    await asDashboardPrivate(dashboard).handleExportAnalytics("csv-runs", "all");

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      "No telemetry records found for the selected date range."
    );
  });

  it("uses readDateRange for last7 with correct 7-day window", async () => {
    (ExecutionHistoryReader.readDateRange as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    vi.mocked(vscode.window.showSaveDialog).mockResolvedValue(undefined); // cancel save to skip write

    await asDashboardPrivate(dashboard).handleExportAnalytics("csv-runs", "last7");

    expect(ExecutionHistoryReader.readDateRange).toHaveBeenCalled();
    const [, start, end] = (ExecutionHistoryReader.readDateRange as ReturnType<typeof vi.fn>).mock
      .calls[0];
    const diffDays = (end.getTime() - start.getTime()) / (1000 * 60 * 60 * 24);
    expect(diffDays).toBeGreaterThanOrEqual(6.9);
    expect(diffDays).toBeLessThanOrEqual(7.1);
  });

  it("uses readDateRange for last30 with correct 30-day window", async () => {
    (ExecutionHistoryReader.readDateRange as ReturnType<typeof vi.fn>).mockResolvedValue([]);
    vi.mocked(vscode.window.showSaveDialog).mockResolvedValue(undefined);

    await asDashboardPrivate(dashboard).handleExportAnalytics("csv-runs", "last30");

    const [, start, end] = (ExecutionHistoryReader.readDateRange as ReturnType<typeof vi.fn>).mock
      .calls[0];
    const diffDays = (end.getTime() - start.getTime()) / (1000 * 60 * 60 * 24);
    expect(diffDays).toBeGreaterThanOrEqual(29.9);
    expect(diffDays).toBeLessThanOrEqual(30.1);
  });
});
