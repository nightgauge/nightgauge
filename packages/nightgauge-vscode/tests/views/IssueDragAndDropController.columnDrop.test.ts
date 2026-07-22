/**
 * Tests for cross-column drag-and-drop (Issue #1795)
 *
 * Covers ColumnDragAndDropController routing, epic cascade, no-op detection,
 * and multi-select batch moves.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";

// --- Module mocks ---

vi.mock("../../src/utils/projectFieldWriter", () => ({
  updateProjectItemStatus: vi.fn(),
  clearConfigCache: vi.fn(),
}));

vi.mock("../../src/config/warningSettings", () => ({
  getWarningSettings: vi.fn(() => ({ enabled: false })),
}));

vi.mock("../../src/utils/dialogs", () => ({
  showStatusWarningDialog: vi.fn(),
  openUrl: vi.fn(),
}));

vi.mock("../../src/utils/prDetection", () => ({
  getPRForIssue: vi.fn(),
}));

vi.mock("../../src/utils/logger", () => ({
  Logger: class {
    info = vi.fn();
    warn = vi.fn();
    error = vi.fn();
    debug = vi.fn();
  },
}));

const mockEpicTransitionStatus = vi.fn();
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      epicTransitionStatus: mockEpicTransitionStatus,
    }),
  },
}));

// --- Imports after mocks ---

import {
  IssueDragAndDropController,
  ColumnDragAndDropController,
} from "../../src/views/IssueDragAndDropController";
import { updateProjectItemStatus } from "../../src/utils/projectFieldWriter";
import type { BaseTreeItem } from "../../src/views/items/BaseTreeItem";

// --- Helpers ---

function makePayload(overrides: Partial<any> = {}): any {
  return {
    issueNumber: 42,
    title: "Test issue",
    labels: [],
    url: "https://github.com/test/repo/issues/42",
    sourceTabStatus: "Backlog",
    isEpic: false,
    subIssueNumbers: undefined,
    ...overrides,
  };
}

function makeDataTransfer(payload: any[]): vscode.DataTransfer {
  const mimeTypes = ["application/vnd.code.tree.nightgauge-issue"];
  const dt: any = {
    get: vi.fn((mime: string) => {
      if (mime === "application/vnd.code.tree.nightgauge-issue") {
        return new vscode.DataTransferItem(JSON.stringify(payload));
      }
      return undefined;
    }),
    set: vi.fn(),
    forEach: vi.fn((callback: (item: any, mime: string) => void) => {
      mimeTypes.forEach((mime) => {
        const item = dt.get(mime);
        if (item) {
          callback(item, mime);
        }
      });
    }),
  };
  return dt as vscode.DataTransfer;
}

const cancelToken: vscode.CancellationToken = {
  isCancellationRequested: false,
  onCancellationRequested: vi.fn() as any,
};

// --- Tests ---

describe("ColumnDragAndDropController", () => {
  let mockProvider: any;

  beforeEach(() => {
    vi.clearAllMocks();
    mockProvider = {
      refresh: vi.fn(),
      isMultiSelectEnabled: vi.fn(() => false),
      getSelectedIssues: vi.fn(() => []),
    };

    // Add missing window methods to the global vscode mock
    (vscode.window as any).setStatusBarMessage = vi.fn();
    (vscode.window as any).withProgress = vi.fn(async (_opts: any, task: any) => {
      await task({ report: vi.fn() });
    });
  });

  describe("resolveTargetColumnStatus", () => {
    it("returns the configured tab status regardless of drop target", () => {
      const controller = new ColumnDragAndDropController("Ready");
      const result = (controller as any).resolveTargetColumnStatus(undefined);
      expect(result).toBe("Ready");
    });

    it("returns Backlog status for backlog controller", () => {
      const controller = new ColumnDragAndDropController("Backlog");
      const result = (controller as any).resolveTargetColumnStatus(undefined);
      expect(result).toBe("Backlog");
    });

    it("base class returns null (no cross-column routing)", () => {
      const controller = new IssueDragAndDropController();
      const result = (controller as any).resolveTargetColumnStatus(undefined);
      expect(result).toBeNull();
    });
  });

  describe("resolveSourceTabStatus", () => {
    it("returns configured tab status as source", () => {
      const controller = new ColumnDragAndDropController("Backlog");
      const result = (controller as any).resolveSourceTabStatus();
      expect(result).toBe("Backlog");
    });

    it("base class returns undefined", () => {
      const controller = new IssueDragAndDropController();
      const result = (controller as any).resolveSourceTabStatus();
      expect(result).toBeUndefined();
    });
  });

  describe("handleDrop — individual issue cross-column move", () => {
    it("calls updateProjectItemStatus for issue moving to Ready", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({ success: true });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);

      await controller.handleDrop(
        undefined,
        makeDataTransfer([makePayload({ sourceTabStatus: "Backlog" })]),
        cancelToken
      );

      expect(updateProjectItemStatus).toHaveBeenCalledWith(
        42,
        "Ready",
        "/workspace",
        expect.any(Object)
      );
      expect(mockProvider.refresh).toHaveBeenCalled();
    });

    it("shows error when updateProjectItemStatus fails", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({
        success: false,
        error: "No project config",
      });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);

      await controller.handleDrop(
        undefined,
        makeDataTransfer([makePayload({ issueNumber: 99, sourceTabStatus: "Backlog" })]),
        cancelToken
      );

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(expect.stringContaining("#99"));
    });
  });

  describe("handleDrop — same-column no-op", () => {
    it("does not call updateProjectItemStatus when sourceTabStatus matches target", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({ success: true });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);

      // Source and target both 'Ready'
      await controller.handleDrop(
        undefined,
        makeDataTransfer([makePayload({ sourceTabStatus: "Ready" })]),
        cancelToken
      );

      expect(updateProjectItemStatus).not.toHaveBeenCalled();
      expect(mockProvider.refresh).not.toHaveBeenCalled();
    });
  });

  describe("handleDrop — epic cascade", () => {
    const mockBoardService: any = {
      getOwner: () => "nightgauge",
      getProjectNumber: () => 1,
    };

    it("calls epicTransitionStatus via IPC for epic with sub-issues", async () => {
      mockEpicTransitionStatus.mockResolvedValue({
        epicNumber: 100,
        newStatus: "Ready",
        epicSynced: true,
        subIssueTotal: 3,
        subIssueMoved: 3,
      });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);
      controller.setBoardService(mockBoardService);

      const epicPayload = makePayload({
        issueNumber: 100,
        isEpic: true,
        subIssueNumbers: [101, 102, 103],
        sourceTabStatus: "Backlog",
        url: "https://github.com/nightgauge/nightgauge/issues/100",
      });

      await controller.handleDrop(undefined, makeDataTransfer([epicPayload]), cancelToken);

      // Single IPC call handles epic + all sub-issues
      expect(mockEpicTransitionStatus).toHaveBeenCalledWith(
        "nightgauge",
        "nightgauge",
        100,
        1,
        "Ready"
      );
      // updateProjectItemStatus should NOT be called (Go handles it all)
      expect(updateProjectItemStatus).not.toHaveBeenCalled();
      expect(mockProvider.refresh).toHaveBeenCalled();
    });

    it("shows warning when some sub-issues fail", async () => {
      mockEpicTransitionStatus.mockResolvedValue({
        epicNumber: 100,
        newStatus: "Ready",
        epicSynced: true,
        subIssueTotal: 2,
        subIssueMoved: 1,
        failures: [{ number: 102, error: "not on board" }],
      });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);
      controller.setBoardService(mockBoardService);

      await controller.handleDrop(
        undefined,
        makeDataTransfer([
          makePayload({
            issueNumber: 100,
            isEpic: true,
            subIssueNumbers: [101, 102],
            sourceTabStatus: "Backlog",
            url: "https://github.com/nightgauge/nightgauge/issues/100",
          }),
        ]),
        cancelToken
      );

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("1 sub-issue(s) failed")
      );
    });

    it("calls epicTransitionStatus for epic with no sub-issues", async () => {
      mockEpicTransitionStatus.mockResolvedValue({
        epicNumber: 200,
        newStatus: "Ready",
        epicSynced: true,
        subIssueTotal: 0,
        subIssueMoved: 0,
      });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);
      controller.setBoardService(mockBoardService);

      await controller.handleDrop(
        undefined,
        makeDataTransfer([
          makePayload({
            issueNumber: 200,
            isEpic: true,
            subIssueNumbers: [],
            sourceTabStatus: "Backlog",
            url: "https://github.com/nightgauge/nightgauge/issues/200",
          }),
        ]),
        cancelToken
      );

      expect(mockEpicTransitionStatus).toHaveBeenCalledWith(
        "nightgauge",
        "nightgauge",
        200,
        1,
        "Ready"
      );
    });

    it("falls back to updateProjectItemStatus when boardService is missing", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({ success: true });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);
      // No setBoardService — triggers fallback

      await controller.handleDrop(
        undefined,
        makeDataTransfer([
          makePayload({
            issueNumber: 200,
            isEpic: true,
            subIssueNumbers: [],
            sourceTabStatus: "Backlog",
          }),
        ]),
        cancelToken
      );

      // Falls back to projectFieldWriter
      expect(updateProjectItemStatus).toHaveBeenCalledWith(
        200,
        "Ready",
        "/workspace",
        expect.any(Object)
      );
      expect(mockEpicTransitionStatus).not.toHaveBeenCalled();
    });
  });

  describe("handleDrop — multi-select batch", () => {
    it("moves all issues in the payload", async () => {
      vi.mocked(updateProjectItemStatus).mockResolvedValue({ success: true });

      const controller = new ColumnDragAndDropController("Ready");
      controller.setWorkspaceRoot("/workspace");
      controller.setProjectBoardProvider(mockProvider);

      await controller.handleDrop(
        undefined,
        makeDataTransfer([
          makePayload({ issueNumber: 1, sourceTabStatus: "Backlog" }),
          makePayload({ issueNumber: 2, sourceTabStatus: "Backlog" }),
          makePayload({ issueNumber: 3, sourceTabStatus: "Backlog" }),
        ]),
        cancelToken
      );

      expect(updateProjectItemStatus).toHaveBeenCalledTimes(3);
      expect(mockProvider.refresh).toHaveBeenCalled();
    });
  });

  describe("handleDrop — missing workspaceRoot", () => {
    it("does not call updateProjectItemStatus when workspaceRoot is not set", async () => {
      const controller = new ColumnDragAndDropController("Ready");
      // No setWorkspaceRoot call
      controller.setProjectBoardProvider(mockProvider);

      await controller.handleDrop(
        undefined,
        makeDataTransfer([makePayload({ sourceTabStatus: "Backlog" })]),
        cancelToken
      );

      // Without workspaceRoot, cross-column path is skipped; falls to pipeline path
      // which calls validateDropTarget(undefined)=true but then checks pipeline state
      // Since stateService is null, isIssueInPipeline returns false → tries to add to pipeline
      // For this test, we just verify updateProjectItemStatus was NOT called (it's not the pipeline path)
      expect(updateProjectItemStatus).not.toHaveBeenCalled();
    });
  });
});
