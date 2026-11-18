/**
 * Dashboard.messageSurface.test.ts
 *
 * The Dashboard host acts only on validated webview messages: an exact
 * command allowlist for `executeCommand`, a per-type field guard that drops
 * unknown or malformed messages, Dependabot merges limited to PRs the host
 * fetched (after a modal confirmation), and recommendation patches limited to
 * the ones the host is showing.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import * as vscode from "vscode";

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
    showSaveDialog: vi.fn().mockResolvedValue({ fsPath: "/tmp/test-export.csv" } as vscode.Uri),
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
      writeFile: vi.fn().mockResolvedValue(undefined),
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
  commands: {
    executeCommand: vi.fn().mockResolvedValue(undefined),
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

vi.mock("../../../src/services/TelemetryStore", () => ({
  TelemetryStore: vi.fn().mockImplementation(() => ({
    getRunRecord: vi.fn(),
    getAllRunSummaries: vi.fn().mockResolvedValue([]),
    getRunSummariesPaginated: vi.fn().mockResolvedValue({ items: [], total: 0, hasMore: false }),
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
    clearCache: vi.fn(),
  },
}));

import { Dashboard } from "../../../src/views/dashboard/Dashboard";
import { IpcClient } from "../../../src/services/IpcClient";

interface DashboardPrivate {
  handleMessage(raw: unknown): Promise<void>;
  dependabotData: unknown;
  healthWidgetData: unknown;
  recommendationApplier: unknown;
  logger: { warn: (...args: unknown[]) => void };
  refreshDependabotData: () => Promise<void>;
}

const asPrivate = (d: Dashboard) => d as unknown as DashboardPrivate;

const pr = (nodeId: string, number: number) => ({
  nodeId,
  number,
  title: `Bump dep ${number}`,
  repo: "acme/widgets",
  prType: "dependency",
  staleDays: 0,
  isStale: false,
});

describe("Dashboard webview message surface", () => {
  let dashboard: Dashboard;
  const executeCommand = vi.mocked(vscode.commands.executeCommand);
  const showWarningMessage = vi.mocked(vscode.window.showWarningMessage);

  let mockPrMerge: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    mockPrMerge = vi.fn().mockResolvedValue(undefined);
    vi.spyOn(IpcClient.getInstance(), "prMerge").mockImplementation(mockPrMerge as never);
    dashboard = new Dashboard(
      { fsPath: "/mock/extension" } as vscode.Uri,
      createMockMemento(),
      "/test/workspace"
    );
    // The post-merge refresh reaches the forge; it is not under test here.
    asPrivate(dashboard).refreshDependabotData = vi.fn().mockResolvedValue(undefined);
  });

  describe("executeCommand", () => {
    it("does not run a command outside the allowlist", async () => {
      await asPrivate(dashboard).handleMessage({
        type: "executeCommand",
        command: "nightgauge.resetPipeline",
      });
      expect(executeCommand).not.toHaveBeenCalled();
    });

    it("runs a command the Dashboard renders a button for", async () => {
      await asPrivate(dashboard).handleMessage({
        type: "executeCommand",
        command: "nightgauge.exportTelemetry",
      });
      expect(executeCommand).toHaveBeenCalledWith("nightgauge.exportTelemetry");
    });
  });

  describe("message guard", () => {
    it("logs and drops an unknown message type", async () => {
      const warn = vi.spyOn(asPrivate(dashboard).logger, "warn");
      await asPrivate(dashboard).handleMessage({ type: "runArbitrary", command: "x" });
      expect(warn).toHaveBeenCalledWith(
        "dashboard:message-dropped",
        expect.objectContaining({ reason: "unknown-type" })
      );
      expect(executeCommand).not.toHaveBeenCalled();
    });

    it("drops a known type whose fields fail validation", async () => {
      await asPrivate(dashboard).handleMessage({ type: "openSlotOutput", slotIndex: "0; x" });
      expect(executeCommand).not.toHaveBeenCalled();
    });

    it("drops a non-object message", async () => {
      await asPrivate(dashboard).handleMessage("executeCommand");
      await asPrivate(dashboard).handleMessage(null);
      expect(executeCommand).not.toHaveBeenCalled();
    });
  });

  describe("mergeDependabotPR", () => {
    beforeEach(() => {
      asPrivate(dashboard).dependabotData = {
        data: { prs: [pr("PR_known", 12)], staleCount: 0, securityCount: 0, fetchedAt: "" },
      };
    });

    const merge = (prNodeId: string) =>
      asPrivate(dashboard).handleMessage({
        type: "mergeDependabotPR",
        prNodeId,
        owner: "someone",
        repo: "elsewhere",
      });

    it("does not merge a PR that is not in the fetched Dependabot list", async () => {
      showWarningMessage.mockResolvedValue("Merge" as never);
      await merge("PR_unknown");
      await vi.waitFor(() => expect(mockPrMerge).not.toHaveBeenCalled());
      expect(showWarningMessage).not.toHaveBeenCalled();
    });

    it("does not merge when the operator declines the modal", async () => {
      showWarningMessage.mockResolvedValue(undefined as never);
      await merge("PR_known");
      await vi.waitFor(() => expect(showWarningMessage).toHaveBeenCalled());
      expect(showWarningMessage.mock.calls[0][1]).toMatchObject({ modal: true });
      expect(mockPrMerge).not.toHaveBeenCalled();
    });

    it("merges a listed PR after confirmation, using the host's owner and repo", async () => {
      showWarningMessage.mockResolvedValue("Merge" as never);
      await merge("PR_known");
      await vi.waitFor(() => expect(mockPrMerge).toHaveBeenCalled());
      expect(mockPrMerge).toHaveBeenCalledWith("acme", "widgets", "PR_known");
    });
  });

  describe("applyRecommendation", () => {
    let apply: ReturnType<typeof vi.fn>;

    beforeEach(() => {
      apply = vi.fn().mockResolvedValue({ success: true });
      asPrivate(dashboard).recommendationApplier = { apply, revert: vi.fn() };
      asPrivate(dashboard).healthWidgetData = {
        recommendations: [
          {
            category: "cache",
            title: "t",
            description: "d",
            estimatedSavingsUsd: 0,
            severity: "info",
            action: {
              type: "config-patch",
              configPath: "pipeline.max_concurrent",
              suggestedValue: 2,
              label: "Apply",
            },
          },
        ],
      };
    });

    it("refuses a config path the host is not showing", async () => {
      await asPrivate(dashboard).handleMessage({
        type: "applyRecommendation",
        category: "cache",
        configPath: "github_auth.app",
        value: "x",
      });
      expect(apply).not.toHaveBeenCalled();
    });

    it("applies the host's suggested value, not the webview's", async () => {
      await asPrivate(dashboard).handleMessage({
        type: "applyRecommendation",
        category: "cache",
        configPath: "pipeline.max_concurrent",
        value: 99,
      });
      expect(apply).toHaveBeenCalledWith("cache", "pipeline.max_concurrent", 2);
    });
  });
});
