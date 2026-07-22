/**
 * startPipelineForIssue.test.ts
 *
 * Unit tests for the startPipelineForIssue command.
 *
 * @see Issue #210 - Change default issue click action to start pipeline
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock vscode module before imports
vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((_command: string, callback: Function) => ({
      dispose: vi.fn(),
      callback, // Expose callback for testing
    })),
    executeCommand: vi.fn(),
  },
  window: {
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
  },
  TreeItemCollapsibleState: {
    None: 0,
    Collapsed: 1,
    Expanded: 2,
  },
  TreeItemCheckboxState: {
    Unchecked: 0,
    Checked: 1,
  },
  ThemeIcon: class {
    constructor(
      public id: string,
      public color?: any
    ) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  MarkdownString: class {
    value = "";
    isTrusted = false;
    appendMarkdown(text: string) {
      this.value += text;
    }
  },
  TreeItem: class {
    label: string;
    collapsibleState: number;
    iconPath?: any;
    contextValue?: string;
    description?: string;
    tooltip?: any;
    command?: any;
    checkboxState?: any;

    constructor(label: string, collapsibleState?: number) {
      this.label = label;
      this.collapsibleState = collapsibleState ?? 0;
    }
  },
}));

import * as vscode from "vscode";
import { registerStartPipelineForIssueCommand } from "../../src/commands/startPipelineForIssue";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import type { Logger } from "../../src/utils/logger";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import { createMockReadyIssue } from "../mocks/github-api";

// Mock logger
const mockLogger: Logger = {
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  debug: vi.fn(),
  log: vi.fn(),
} as unknown as Logger;

// Helper to create mock orchestrator
function createMockOrchestrator(options: { isRunning?: boolean } = {}): HeadlessOrchestrator {
  const { isRunning = false } = options;
  return {
    getIsRunning: vi.fn(() => isRunning),
  } as unknown as HeadlessOrchestrator;
}

// Helper to extract command callback from registration
function extractCommandCallback(
  registration: vscode.Disposable
): (item?: ReadyIssueTreeItem) => Promise<void> {
  return (registration as any).callback;
}

// Helper to create a ReadyIssueTreeItem for testing
function createTestItem(overrides?: { number?: number; title?: string }): ReadyIssueTreeItem {
  const issue = createMockReadyIssue({
    number: overrides?.number ?? 42,
    title: overrides?.title ?? "Test issue title",
  });
  return new ReadyIssueTreeItem(issue, { showDependencies: false });
}

describe("startPipelineForIssue Command", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("Registration", () => {
    it("should register the nightgauge.startPipelineForIssue command", () => {
      const orchestrator = createMockOrchestrator();

      registerStartPipelineForIssueCommand(mockLogger, orchestrator);

      expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
        "nightgauge.startPipelineForIssue",
        expect.any(Function)
      );
    });

    // 'should return a Disposable' test removed (Issue #1826): TypeScript
    // enforces the return type at compile time; a runtime existence check
    // adds no contract value.
  });

  describe("Guard Clauses", () => {
    it("should return early if no tree item provided", async () => {
      const orchestrator = createMockOrchestrator();

      const disposable = registerStartPipelineForIssueCommand(mockLogger, orchestrator);

      const callback = extractCommandCallback(disposable);
      await callback(undefined);

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "startPipelineForIssue called without a ReadyIssueTreeItem"
      );
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
      expect(vscode.commands.executeCommand).not.toHaveBeenCalled();
    });

    it("should return early if tree item is not a ReadyIssueTreeItem", async () => {
      const orchestrator = createMockOrchestrator();

      const disposable = registerStartPipelineForIssueCommand(mockLogger, orchestrator);

      const callback = extractCommandCallback(disposable);
      await callback({ notAReadyIssue: true } as any);

      expect(mockLogger.warn).toHaveBeenCalled();
      expect(vscode.commands.executeCommand).not.toHaveBeenCalled();
    });
  });

  describe("Immediate Execution (No Pipeline Running)", () => {
    it("should NOT show confirmation dialog when no pipeline is running", async () => {
      const orchestrator = createMockOrchestrator();

      const disposable = registerStartPipelineForIssueCommand(mockLogger, orchestrator);

      const item = createTestItem({ number: 42, title: "Add dark mode" });
      const callback = extractCommandCallback(disposable);
      await callback(item);

      // Verify NO confirmation dialog shown
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("should execute nightgauge.pickupIssue immediately without confirmation", async () => {
      const orchestrator = createMockOrchestrator();

      const disposable = registerStartPipelineForIssueCommand(mockLogger, orchestrator);

      const item = createTestItem({ number: 42, title: "Add dark mode" });
      const callback = extractCommandCallback(disposable);
      await callback(item);

      // Verify command executed directly
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.pickupIssue", item);
    });

    // 'should log issue number when starting pipeline' test removed
    // (Issue #1826): Pure logging assertion — the behavioral outcome
    // (executeCommand called with pickupIssue) is tested above.
  });

  describe("Unified Queue Path", () => {
    it("should enqueue issue regardless of running state (no conflict dialog)", async () => {
      const orchestrator = createMockOrchestrator({ isRunning: true });

      const disposable = registerStartPipelineForIssueCommand(mockLogger, orchestrator);

      const callback = extractCommandCallback(disposable);
      await callback(createTestItem({ number: 99 }));

      // No conflict dialog — all issues go through queue
      expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    });
  });

  describe("Null Orchestrator", () => {
    it("should proceed immediately when orchestrator is null (no running state check)", async () => {
      const disposable = registerStartPipelineForIssueCommand(mockLogger, null, null);

      const item = createTestItem();
      const callback = extractCommandCallback(disposable);
      await callback(item);

      // Should NOT show confirmation dialog
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.pickupIssue", item);
    });
  });

  // Issue #3111 — distinguish shutdown-refusal from missing-identity. When
  // ConcurrentPipelineManager.isShutdownInProgress is true, the command must
  // surface a clear toast and skip enqueue rather than silently no-op with a
  // misleading "repo identity may be missing" warning.
  describe("Shutdown Pre-Check (Issue #3111)", () => {
    it("shows shutdown toast and aborts when manager is mid-shutdown", async () => {
      const cpm = { isShutdownInProgress: true } as any;
      const queueService = { enqueue: vi.fn() } as any;

      const disposable = registerStartPipelineForIssueCommand(mockLogger, null, queueService, cpm);

      const item = createTestItem({ number: 282 });
      await extractCommandCallback(disposable)(item);

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("mid-shutdown")
      );
      expect(queueService.enqueue).not.toHaveBeenCalled();
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Refusing enqueue — pipeline manager is shutting down",
        expect.objectContaining({ issueNumber: 282 })
      );
    });

    it("proceeds normally when manager is not shutting down", async () => {
      const cpm = { isShutdownInProgress: false } as any;
      const queueService = {
        enqueue: vi.fn().mockResolvedValue({ issueNumber: 99 }),
        isQueued: vi.fn().mockResolvedValue(false),
      } as any;

      const disposable = registerStartPipelineForIssueCommand(mockLogger, null, queueService, cpm);

      await extractCommandCallback(disposable)(createTestItem({ number: 99 }));

      expect(vscode.window.showWarningMessage).not.toHaveBeenCalledWith(
        expect.stringContaining("mid-shutdown")
      );
      expect(queueService.enqueue).toHaveBeenCalled();
    });
  });
});
