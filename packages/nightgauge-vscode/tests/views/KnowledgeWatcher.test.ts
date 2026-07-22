/**
 * KnowledgeWatcher.test.ts
 *
 * Unit tests for the FileSystemWatcher integrated into KnowledgeTreeProvider
 * (Issue #1689). Tests focus on:
 * - Watcher registration with correct glob pattern
 * - Subscriptions to create, change, and delete events
 * - 500ms debounce batching rapid file changes
 * - Watcher disposal on provider dispose
 * - Watcher reinitialization on workspace root change
 * - Graceful degradation when watcher creation fails
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { KnowledgeTreeProvider } from "../../src/views/KnowledgeTreeProvider";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { IpcClient } from "../../src/services/IpcClient";

// Mock KnowledgeService to prevent real FS calls (legacy — kept for safety)
vi.mock("@nightgauge/sdk", () => ({
  KnowledgeService: vi.fn(function () {
    return { list: vi.fn().mockResolvedValue([]) };
  }),
}));

const makePss = (): PipelineStateService =>
  ({
    getActiveIssueBlockingPickup: vi.fn(() => null),
    onStateChanged: vi.fn(() => ({ dispose: vi.fn() })),
  }) as unknown as PipelineStateService;

const makeIpc = (): IpcClient =>
  ({
    knowledgeRelatedToIssue: vi.fn(async () => ({ hits: [] })),
    knowledgeSearch: vi.fn(async () => ({ hits: [], total_hits: 0 })),
  }) as unknown as IpcClient;

const newProvider = (root: string): KnowledgeTreeProvider =>
  new KnowledgeTreeProvider(root, makePss(), makeIpc());

// Watcher callback stores — captured during createFileSystemWatcher calls
const watcherCallbacks = {
  onCreate: null as ((uri: any) => void) | null,
  onChange: null as ((uri: any) => void) | null,
  onDelete: null as ((uri: any) => void) | null,
};

let mockWatcherDispose: ReturnType<typeof vi.fn>;

// Local vscode mock with callback capture support
vi.mock("vscode", () => {
  return {
    RelativePattern: class RelativePattern {
      constructor(
        public base: string,
        public pattern: string
      ) {}
    },
    workspace: {
      createFileSystemWatcher: vi.fn(() => {
        mockWatcherDispose = vi.fn();
        return {
          onDidCreate: vi.fn((cb: (uri: any) => void) => {
            watcherCallbacks.onCreate = cb;
          }),
          onDidChange: vi.fn((cb: (uri: any) => void) => {
            watcherCallbacks.onChange = cb;
          }),
          onDidDelete: vi.fn((cb: (uri: any) => void) => {
            watcherCallbacks.onDelete = cb;
          }),
          dispose: vi.fn(() => mockWatcherDispose()),
        };
      }),
    },
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    TreeItem: class TreeItem {
      label: string;
      collapsibleState: number;
      constructor(label: string, collapsibleState = 0) {
        this.label = label;
        this.collapsibleState = collapsibleState;
      }
    },
    ThemeIcon: class ThemeIcon {
      constructor(public id: string) {}
    },
    EventEmitter: class EventEmitter {
      event: any = vi.fn();
      fire = vi.fn();
      dispose = vi.fn();
    },
    window: {
      onDidChangeActiveTextEditor: vi.fn(() => ({ dispose: vi.fn() })),
    },
  };
});

describe("KnowledgeTreeProvider — FileSystemWatcher (Issue #1689)", () => {
  let provider: KnowledgeTreeProvider;
  let vscode: any;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    watcherCallbacks.onCreate = null;
    watcherCallbacks.onChange = null;
    watcherCallbacks.onDelete = null;
    vscode = await import("vscode");
  });

  afterEach(() => {
    provider?.dispose();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  describe("Registration", () => {
    it("should create a FileSystemWatcher on construction", () => {
      provider = newProvider("/workspace");
      expect(vscode.workspace.createFileSystemWatcher).toHaveBeenCalledTimes(1);
    });

    it("should watch .nightgauge/knowledge/**/*.md via RelativePattern", () => {
      provider = newProvider("/workspace");
      const pattern = vscode.workspace.createFileSystemWatcher.mock.calls[0][0];
      expect(pattern.base).toBe("/workspace");
      expect(pattern.pattern).toBe(".nightgauge/knowledge/**/*.md");
    });

    it("should subscribe to onDidCreate, onDidChange, and onDidDelete", () => {
      provider = newProvider("/workspace");
      const watcher = vscode.workspace.createFileSystemWatcher.mock.results[0].value;
      expect(watcher.onDidCreate).toHaveBeenCalledTimes(1);
      expect(watcher.onDidChange).toHaveBeenCalledTimes(1);
      expect(watcher.onDidDelete).toHaveBeenCalledTimes(1);
    });

    it("should not create watcher when workspaceRoot is empty", () => {
      provider = newProvider("");
      expect(vscode.workspace.createFileSystemWatcher).not.toHaveBeenCalled();
    });
  });

  describe("Auto-refresh on file events", () => {
    it("should refresh after onCreate event (after debounce)", () => {
      provider = newProvider("/workspace");
      const refreshSpy = vi.spyOn(provider, "refresh");

      watcherCallbacks.onCreate?.({
        fsPath: "/workspace/.nightgauge/knowledge/epics/1-a/PRD.md",
      });

      expect(refreshSpy).not.toHaveBeenCalled();
      vi.advanceTimersByTime(500);
      expect(refreshSpy).toHaveBeenCalledTimes(1);
    });

    it("should refresh after onChange event (after debounce)", () => {
      provider = newProvider("/workspace");
      const refreshSpy = vi.spyOn(provider, "refresh");

      watcherCallbacks.onChange?.({
        fsPath: "/workspace/.nightgauge/knowledge/features/2-b/PRD.md",
      });

      vi.advanceTimersByTime(500);
      expect(refreshSpy).toHaveBeenCalledTimes(1);
    });

    it("should refresh after onDelete event (after debounce)", () => {
      provider = newProvider("/workspace");
      const refreshSpy = vi.spyOn(provider, "refresh");

      watcherCallbacks.onDelete?.({
        fsPath: "/workspace/.nightgauge/knowledge/glossary/term.md",
      });

      vi.advanceTimersByTime(500);
      expect(refreshSpy).toHaveBeenCalledTimes(1);
    });
  });

  describe("Debouncing", () => {
    it("should debounce rapid events into a single refresh", () => {
      provider = newProvider("/workspace");
      const refreshSpy = vi.spyOn(provider, "refresh");

      // Simulate rapid creation of multiple files (e.g., issue-pickup scaffolding)
      watcherCallbacks.onCreate?.({
        fsPath: "/workspace/.nightgauge/knowledge/epics/1/PRD.md",
      });
      watcherCallbacks.onCreate?.({
        fsPath: "/workspace/.nightgauge/knowledge/epics/1/decisions.md",
      });
      watcherCallbacks.onChange?.({
        fsPath: "/workspace/.nightgauge/knowledge/epics/1/PRD.md",
      });
      watcherCallbacks.onDelete?.({
        fsPath: "/workspace/.nightgauge/knowledge/epics/0/old.md",
      });

      // Before debounce fires, no refresh
      expect(refreshSpy).not.toHaveBeenCalled();

      vi.advanceTimersByTime(500);

      // Only a single refresh despite 4 events
      expect(refreshSpy).toHaveBeenCalledTimes(1);
    });

    it("should reset debounce timer on each event", () => {
      provider = newProvider("/workspace");
      const refreshSpy = vi.spyOn(provider, "refresh");

      watcherCallbacks.onCreate?.({ fsPath: "/a.md" });
      vi.advanceTimersByTime(300);

      // Timer reset by second event
      watcherCallbacks.onChange?.({ fsPath: "/b.md" });
      vi.advanceTimersByTime(300);

      // 600ms elapsed but debounce was reset at 300ms — should not have fired yet
      expect(refreshSpy).not.toHaveBeenCalled();

      vi.advanceTimersByTime(200);
      expect(refreshSpy).toHaveBeenCalledTimes(1);
    });

    it("should not refresh before 500ms debounce window elapses", () => {
      provider = newProvider("/workspace");
      const refreshSpy = vi.spyOn(provider, "refresh");

      watcherCallbacks.onCreate?.({ fsPath: "/a.md" });
      vi.advanceTimersByTime(499);

      expect(refreshSpy).not.toHaveBeenCalled();
    });
  });

  describe("Disposal", () => {
    it("should dispose the watcher on provider dispose", () => {
      provider = newProvider("/workspace");
      const watcher = vscode.workspace.createFileSystemWatcher.mock.results[0].value;

      provider.dispose();

      expect(watcher.dispose).toHaveBeenCalledTimes(1);
    });

    it("should clear pending debounce timer on dispose", () => {
      provider = newProvider("/workspace");
      const refreshSpy = vi.spyOn(provider, "refresh");

      watcherCallbacks.onCreate?.({ fsPath: "/a.md" });
      provider.dispose();

      vi.advanceTimersByTime(500);
      expect(refreshSpy).not.toHaveBeenCalled();
    });

    it("should allow multiple dispose calls without throwing", () => {
      provider = newProvider("/workspace");
      expect(() => {
        provider.dispose();
        provider.dispose();
      }).not.toThrow();
    });
  });

  describe("Workspace root change", () => {
    it("should reinitialize watcher with new workspace root", () => {
      provider = newProvider("/workspace-a");
      expect(vscode.workspace.createFileSystemWatcher).toHaveBeenCalledTimes(1);

      provider.updateWorkspaceRoot("/workspace-b");

      expect(vscode.workspace.createFileSystemWatcher).toHaveBeenCalledTimes(2);
      const newPattern = vscode.workspace.createFileSystemWatcher.mock.calls[1][0];
      expect(newPattern.base).toBe("/workspace-b");
    });

    it("should dispose old watcher when workspace root changes", () => {
      provider = newProvider("/workspace-a");
      const firstWatcher = vscode.workspace.createFileSystemWatcher.mock.results[0].value;

      provider.updateWorkspaceRoot("/workspace-b");

      expect(firstWatcher.dispose).toHaveBeenCalledTimes(1);
    });
  });

  describe("Graceful degradation", () => {
    it("should not throw when watcher creation fails", () => {
      vscode.workspace.createFileSystemWatcher.mockImplementationOnce(() => {
        throw new Error("watcher unavailable");
      });

      expect(() => {
        provider = newProvider("/workspace");
      }).not.toThrow();
    });
  });
});
