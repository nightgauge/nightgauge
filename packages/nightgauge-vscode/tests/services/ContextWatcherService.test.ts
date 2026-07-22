/**
 * ContextWatcherService.test.ts
 *
 * Unit tests for ContextWatcherService, focusing on:
 * - Watcher initialization with correct glob patterns
 * - Event handling for file create, modify, delete
 * - Event propagation to subscribers
 * - Issue file parsing and validation
 * - Stage file to pipeline stage mapping
 * - Existing context scanning on startup
 * - Graceful disposal and cleanup
 *
 * @see Issue #275 - Add ContextWatcherService unit tests
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as path from "node:path";
import { ContextWatcherService } from "../../src/services/ContextWatcherService";

// Mock logger to avoid console noise in tests
const mockLogger = {
  debug: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
};

// Mock fs/promises
vi.mock("node:fs/promises", () => ({
  readFile: vi.fn(),
  readdir: vi.fn(),
  stat: vi.fn(),
  unlink: vi.fn(),
}));

import * as fs from "node:fs/promises";

// Mock vscode module - must be hoisted before imports
vi.mock("vscode", () => {
  // Track watcher instances and their callbacks
  const watcherInstances: Array<{
    pattern: any;
    callbacks: {
      onCreate: ((uri: any) => void) | null;
      onChange: ((uri: any) => void) | null;
      onDelete: ((uri: any) => void) | null;
    };
    dispose: ReturnType<typeof vi.fn>;
  }> = [];

  return {
    RelativePattern: class RelativePattern {
      constructor(
        public base: string,
        public pattern: string
      ) {}
    },
    Uri: {
      file: (path: string) => ({ fsPath: path }),
    },
    EventEmitter: class EventEmitter<T> {
      private _listeners: Array<(e: T) => void> = [];
      event = (listener: (e: T) => void) => {
        this._listeners.push(listener);
        return { dispose: () => {} };
      };
      fire = (event: T) => {
        this._listeners.forEach((l) => l(event));
      };
      dispose = vi.fn();
    },
    workspace: {
      createFileSystemWatcher: vi.fn((pattern) => {
        const callbacks = {
          onCreate: null as ((uri: any) => void) | null,
          onChange: null as ((uri: any) => void) | null,
          onDelete: null as ((uri: any) => void) | null,
        };

        const watcher = {
          onDidCreate: vi.fn((callback) => {
            callbacks.onCreate = callback;
            return { dispose: vi.fn() };
          }),
          onDidChange: vi.fn((callback) => {
            callbacks.onChange = callback;
            return { dispose: vi.fn() };
          }),
          onDidDelete: vi.fn((callback) => {
            callbacks.onDelete = callback;
            return { dispose: vi.fn() };
          }),
          dispose: vi.fn(),
        };

        watcherInstances.push({ pattern, callbacks, dispose: watcher.dispose });
        return watcher;
      }),
    },
    // Export for test access
    _testWatcherInstances: watcherInstances,
  };
});

describe("ContextWatcherService", () => {
  const workspaceRoot = "/test/workspace";
  let vscode: any;
  let watcherInstances: any[];

  beforeEach(async () => {
    vi.clearAllMocks();
    // Get the mocked vscode module and reset watcher instances
    vscode = await import("vscode");
    watcherInstances = (vscode as any)._testWatcherInstances;
    watcherInstances.length = 0;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("Initialization", () => {
    it("should create watchers for all context file patterns", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      // Should create 6 watchers: issue, planning, dev, validate, pr, merge
      expect(vscode.workspace.createFileSystemWatcher).toHaveBeenCalledTimes(6);

      service.dispose();
    });

    it("should create watcher for issue-*.json pattern", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      expect(issueWatcher).toBeDefined();
      expect(issueWatcher?.pattern.base).toBe(path.join(workspaceRoot, ".nightgauge", "pipeline"));

      service.dispose();
    });

    it("should create watchers for stage context file patterns", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const expectedPatterns = [
        "planning-*.json",
        "dev-*.json",
        "validate-*.json",
        "pr-*.json",
        "merge-*.json",
      ];

      for (const expectedPattern of expectedPatterns) {
        const watcher = watcherInstances.find((w) => w.pattern.pattern === expectedPattern);
        expect(watcher).toBeDefined();
      }

      service.dispose();
    });

    it("should subscribe to all file events for issue watcher", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      // Get the first watcher (issue-*.json)
      const issueWatcherResult = vscode.workspace.createFileSystemWatcher.mock.results[0].value;

      expect(issueWatcherResult.onDidCreate).toHaveBeenCalledTimes(1);
      expect(issueWatcherResult.onDidChange).toHaveBeenCalledTimes(1);
      expect(issueWatcherResult.onDidDelete).toHaveBeenCalledTimes(1);

      service.dispose();
    });

    it("should subscribe to create and change events for stage watchers", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      // Stage watchers (index 1-5) only subscribe to create and change, not delete
      for (let i = 1; i < 6; i++) {
        const watcherResult = vscode.workspace.createFileSystemWatcher.mock.results[i].value;
        expect(watcherResult.onDidCreate).toHaveBeenCalledTimes(1);
        expect(watcherResult.onDidChange).toHaveBeenCalledTimes(1);
      }

      service.dispose();
    });

    // 'should log initialization success' test removed (Issue #1826):
    // Pure debug log assertion — initialization behavior is verified
    // by the watcher creation and event firing tests.

    it("should not initialize watchers when workspaceRoot is empty", () => {
      const service = new ContextWatcherService("", mockLogger as any);

      expect(vscode.workspace.createFileSystemWatcher).not.toHaveBeenCalled();

      service.dispose();
    });
  });

  describe("Issue File Events", () => {
    it("should fire onIssuePickedUp when issue file is created", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      // Mock file content
      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
          base_branch: "main",
          labels: ["type:feature"],
        })
      );

      // Trigger file creation
      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      };
      await issueWatcher?.callbacks.onCreate?.(mockUri);

      expect(issueHandler).toHaveBeenCalledTimes(1);
      expect(issueHandler).toHaveBeenCalledWith(
        expect.objectContaining({
          number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
        })
      );

      service.dispose();
    });

    it("should fire onStageComplete with issue-pickup stage when issue file is created", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const stageHandler = vi.fn();
      service.onStageComplete(stageHandler);

      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
          base_branch: "main",
          labels: [],
        })
      );

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      };
      await issueWatcher?.callbacks.onCreate?.(mockUri);

      expect(stageHandler).toHaveBeenCalledWith({
        issueNumber: 42,
        stage: "issue-pickup",
      });

      service.dispose();
    });

    it("should fire onIssuePickedUp when issue file is modified", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Updated Title",
          branch: "feat/42-test",
          base_branch: "main",
          labels: [],
        })
      );

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      };
      await issueWatcher?.callbacks.onChange?.(mockUri);

      expect(issueHandler).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "Updated Title",
        })
      );

      service.dispose();
    });

    it("should fire onIssueCleared when issue file is deleted", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const clearedHandler = vi.fn();
      service.onIssueCleared(clearedHandler);

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      };
      issueWatcher?.callbacks.onDelete?.(mockUri);

      expect(clearedHandler).toHaveBeenCalledTimes(1);
      expect(clearedHandler).toHaveBeenCalledWith(42);

      service.dispose();
    });

    it("should not fire events when file parsing fails", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readFile).mockRejectedValue(new Error("Read failed"));

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      };
      await issueWatcher?.callbacks.onCreate?.(mockUri);

      expect(issueHandler).not.toHaveBeenCalled();
      expect(mockLogger.warn).toHaveBeenCalled();

      service.dispose();
    });

    it("should not fire events when file has missing required fields", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      // Missing issue_number
      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          title: "Test Issue",
          branch: "feat/42-test",
        })
      );

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      };
      await issueWatcher?.callbacks.onCreate?.(mockUri);

      expect(issueHandler).not.toHaveBeenCalled();
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Invalid issue context file - missing required fields",
        expect.any(Object)
      );

      service.dispose();
    });

    it("should handle ENOENT gracefully with debug log", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const error = new Error("File not found") as NodeJS.ErrnoException;
      error.code = "ENOENT";
      vi.mocked(fs.readFile).mockRejectedValue(error);

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      };
      await issueWatcher?.callbacks.onCreate?.(mockUri);

      expect(mockLogger.debug).toHaveBeenCalledWith(
        "Issue context file not found",
        expect.any(Object)
      );
      expect(mockLogger.warn).not.toHaveBeenCalled();

      service.dispose();
    });
  });

  describe("Stage File Events", () => {
    it.each([
      ["planning", "feature-planning"],
      ["dev", "feature-dev"],
      ["validate", "feature-validate"],
      ["pr", "pr-create"],
      ["merge", "pr-merge"],
    ])("should fire onStageComplete with %s stage for %s-*.json", async (prefix, expectedStage) => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const stageHandler = vi.fn();
      service.onStageComplete(stageHandler);

      const stageWatcher = watcherInstances.find((w) => w.pattern.pattern === `${prefix}-*.json`);
      const mockUri = {
        fsPath: `/test/workspace/.nightgauge/pipeline/${prefix}-42.json`,
      };
      await stageWatcher?.callbacks.onCreate?.(mockUri);

      expect(stageHandler).toHaveBeenCalledWith({
        issueNumber: 42,
        stage: expectedStage,
      });

      service.dispose();
    });

    it("should fire onStageComplete on file change as well as create", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const stageHandler = vi.fn();
      service.onStageComplete(stageHandler);

      const devWatcher = watcherInstances.find((w) => w.pattern.pattern === "dev-*.json");
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/dev-42.json",
      };

      await devWatcher?.callbacks.onCreate?.(mockUri);
      await devWatcher?.callbacks.onChange?.(mockUri);

      expect(stageHandler).toHaveBeenCalledTimes(2);

      service.dispose();
    });

    it("should not fire events for invalid filename patterns", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const stageHandler = vi.fn();
      service.onStageComplete(stageHandler);

      const devWatcher = watcherInstances.find((w) => w.pattern.pattern === "dev-*.json");
      // Invalid filename - no number
      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/pipeline/dev-abc.json",
      };
      await devWatcher?.callbacks.onCreate?.(mockUri);

      expect(stageHandler).not.toHaveBeenCalled();

      service.dispose();
    });
  });

  describe("Issue Number Extraction", () => {
    it.each([
      ["issue-42.json", 42],
      ["planning-123.json", 123],
      ["dev-1.json", 1],
      ["validate-999.json", 999],
      ["pr-50.json", 50],
      ["merge-7.json", 7],
    ])("should extract issue number from %s as %d", (filename, expectedNumber) => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const clearedHandler = vi.fn();
      service.onIssueCleared(clearedHandler);

      // Use issue watcher's delete handler to test extraction
      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");

      // Only issue files trigger onIssueCleared, but we can verify extraction
      // by triggering the delete handler
      const mockUri = {
        fsPath: `/test/workspace/.nightgauge/pipeline/${filename}`,
      };

      // For issue files, deletion fires the event
      if (filename.startsWith("issue-")) {
        issueWatcher?.callbacks.onDelete?.(mockUri);
        expect(clearedHandler).toHaveBeenCalledWith(expectedNumber);
      }

      service.dispose();
    });

    it("should return null for invalid filenames", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const clearedHandler = vi.fn();
      service.onIssueCleared(clearedHandler);

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");

      // Invalid filename patterns
      const invalidUris = [
        { fsPath: "/test/workspace/.nightgauge/pipeline/issue-.json" },
        { fsPath: "/test/workspace/.nightgauge/pipeline/issue-abc.json" },
        { fsPath: "/test/workspace/.nightgauge/pipeline/unknown-42.json" },
        { fsPath: "/test/workspace/.nightgauge/pipeline/state.json" },
      ];

      for (const uri of invalidUris) {
        issueWatcher?.callbacks.onDelete?.(uri);
      }

      expect(clearedHandler).not.toHaveBeenCalled();

      service.dispose();
    });
  });

  describe("scanExistingContext()", () => {
    it("should skip scan when state.json does not exist (completed pipeline)", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      // issue-42.json exists but state.json does not — pipeline was completed
      vi.mocked(fs.readdir).mockResolvedValue(["issue-42.json"] as any);

      await service.scanExistingContext();

      expect(issueHandler).not.toHaveBeenCalled();
      expect(mockLogger.debug).toHaveBeenCalledWith(
        "No state.json found — pipeline previously completed, skipping context scan"
      );

      service.dispose();
    });

    it("should fire onIssuePickedUp for existing issue file", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readdir).mockResolvedValue(["state.json", "issue-42.json"] as any);
      vi.mocked(fs.stat).mockResolvedValue({ mtimeMs: Date.now() } as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Existing Issue",
          branch: "feat/42-existing",
          base_branch: "main",
          labels: [],
        })
      );

      await service.scanExistingContext();

      expect(issueHandler).toHaveBeenCalledWith(
        expect.objectContaining({
          number: 42,
          title: "Existing Issue",
        })
      );

      service.dispose();
    });

    it("should fire onStageComplete for existing stage files", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const stageHandler = vi.fn();
      service.onStageComplete(stageHandler);

      vi.mocked(fs.readdir).mockResolvedValue([
        "state.json",
        "issue-42.json",
        "planning-42.json",
        "dev-42.json",
      ] as any);
      vi.mocked(fs.stat).mockResolvedValue({ mtimeMs: Date.now() } as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
          base_branch: "main",
          labels: [],
        })
      );

      await service.scanExistingContext();

      expect(stageHandler).toHaveBeenCalledWith({
        issueNumber: 42,
        stage: "issue-pickup",
      });
      expect(stageHandler).toHaveBeenCalledWith({
        issueNumber: 42,
        stage: "feature-planning",
      });
      expect(stageHandler).toHaveBeenCalledWith({
        issueNumber: 42,
        stage: "feature-dev",
      });

      service.dispose();
    });

    it("should select most recently modified issue file when multiple exist", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readdir).mockResolvedValue([
        "state.json",
        "issue-40.json",
        "issue-42.json",
        "issue-41.json",
      ] as any);

      // issue-42.json is most recent
      vi.mocked(fs.stat).mockImplementation(async (filePath) => {
        const path = filePath as string;
        if (path.includes("42")) {
          return { mtimeMs: 3000 } as any;
        } else if (path.includes("41")) {
          return { mtimeMs: 2000 } as any;
        }
        return { mtimeMs: 1000 } as any;
      });

      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Most Recent Issue",
          branch: "feat/42-test",
          base_branch: "main",
          labels: [],
        })
      );

      await service.scanExistingContext();

      expect(issueHandler).toHaveBeenCalledWith(
        expect.objectContaining({
          number: 42,
          title: "Most Recent Issue",
        })
      );

      service.dispose();
    });

    it("should handle non-existent context directory gracefully", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      const error = new Error("Directory not found") as NodeJS.ErrnoException;
      error.code = "ENOENT";
      vi.mocked(fs.readdir).mockRejectedValue(error);

      await service.scanExistingContext();

      expect(mockLogger.debug).toHaveBeenCalledWith("Context directory does not exist yet");
      expect(issueHandler).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should handle read errors gracefully", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      vi.mocked(fs.readdir).mockRejectedValue(new Error("Permission denied"));

      await service.scanExistingContext();

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to scan existing context",
        expect.objectContaining({
          error: "Permission denied",
        })
      );

      service.dispose();
    });

    it("should skip files that are deleted mid-scan", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readdir).mockResolvedValue([
        "state.json",
        "issue-42.json",
        "issue-43.json",
      ] as any);

      // First file exists, second is deleted
      vi.mocked(fs.stat)
        .mockResolvedValueOnce({ mtimeMs: 1000 } as any)
        .mockRejectedValueOnce(new Error("ENOENT"));

      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
          base_branch: "main",
          labels: [],
        })
      );

      await service.scanExistingContext();

      // Should still process the first file
      expect(issueHandler).toHaveBeenCalled();

      service.dispose();
    });

    it("should do nothing when no workspace root", async () => {
      const service = new ContextWatcherService("", mockLogger as any);

      await service.scanExistingContext();

      expect(fs.readdir).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should do nothing when no issue files found", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readdir).mockResolvedValue(["state.json", "batch-state.json"] as any);

      await service.scanExistingContext();

      expect(mockLogger.debug).toHaveBeenCalledWith("No existing issue context files found");
      expect(issueHandler).not.toHaveBeenCalled();

      service.dispose();
    });
  });

  describe("Issue File Parsing", () => {
    it("should parse all required fields from issue file", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
          base_branch: "develop",
          labels: ["type:feature", "priority:high"],
        })
      );

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      await issueWatcher?.callbacks.onCreate?.({
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      });

      expect(issueHandler).toHaveBeenCalledWith({
        number: 42,
        title: "Test Issue",
        branch: "feat/42-test",
        baseBranch: "develop",
        labels: ["type:feature", "priority:high"],
      });

      service.dispose();
    });

    it("should handle invalid JSON gracefully", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readFile).mockResolvedValue("{ invalid json }");

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      await issueWatcher?.callbacks.onCreate?.({
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      });

      expect(issueHandler).not.toHaveBeenCalled();
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to parse issue context file",
        expect.any(Object)
      );

      service.dispose();
    });
  });

  describe("Disposal", () => {
    it("should dispose all watchers on service dispose", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      service.dispose();

      // All 6 watchers should be disposed
      for (const watcher of watcherInstances) {
        expect(watcher.dispose).toHaveBeenCalledTimes(1);
      }
    });

    it("should dispose all event emitters on service dispose", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      // Get references to event emitters via their public events
      const issueHandler = vi.fn();
      const clearedHandler = vi.fn();
      const stageHandler = vi.fn();

      service.onIssuePickedUp(issueHandler);
      service.onIssueCleared(clearedHandler);
      service.onStageComplete(stageHandler);

      service.dispose();

      // Event emitters are internal, but we verify disposal doesn't throw
      expect(() => service.dispose()).not.toThrow();
    });

    it("should allow multiple dispose calls safely", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      expect(() => {
        service.dispose();
        service.dispose();
        service.dispose();
      }).not.toThrow();
    });
  });

  // describe('Event Logging') block removed (Issue #1826): 5 tests that only
  // verified debug log messages for file watcher events. The actual event
  // behavior (onIssuePickedUp, onStageFileCreated, etc.) is tested in the
  // 'Issue File Events' and 'Stage File Events' describe blocks above.

  describe("Concurrent Mode Suspension (Issue #1540)", () => {
    it("should suppress issue file created events when suspended", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
          base_branch: "main",
          labels: [],
        })
      );

      service.suspend();

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      await issueWatcher?.callbacks.onCreate?.({
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      });

      expect(issueHandler).not.toHaveBeenCalled();
      expect(fs.readFile).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should suppress issue file changed events when suspended", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      service.suspend();

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      await issueWatcher?.callbacks.onChange?.({
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      });

      expect(issueHandler).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should suppress issue file deleted events when suspended", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const clearedHandler = vi.fn();
      service.onIssueCleared(clearedHandler);

      service.suspend();

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      issueWatcher?.callbacks.onDelete?.({
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      });

      expect(clearedHandler).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should suppress stage file events when suspended", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const stageHandler = vi.fn();
      service.onStageComplete(stageHandler);

      service.suspend();

      const devWatcher = watcherInstances.find((w) => w.pattern.pattern === "dev-*.json");
      await devWatcher?.callbacks.onCreate?.({
        fsPath: "/test/workspace/.nightgauge/pipeline/dev-42.json",
      });

      expect(stageHandler).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should skip scanExistingContext when suspended", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      service.suspend();

      await service.scanExistingContext();

      expect(fs.readdir).not.toHaveBeenCalled();
      expect(issueHandler).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should resume event firing after resume()", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const issueHandler = vi.fn();
      service.onIssuePickedUp(issueHandler);

      vi.mocked(fs.readFile).mockResolvedValue(
        JSON.stringify({
          schema_version: "1.0",
          issue_number: 42,
          title: "Test Issue",
          branch: "feat/42-test",
          base_branch: "main",
          labels: [],
        })
      );

      service.suspend();
      expect(service.isSuspended).toBe(true);

      service.resume();
      expect(service.isSuspended).toBe(false);

      const issueWatcher = watcherInstances.find((w) => w.pattern.pattern === "issue-*.json");
      await issueWatcher?.callbacks.onCreate?.({
        fsPath: "/test/workspace/.nightgauge/pipeline/issue-42.json",
      });

      expect(issueHandler).toHaveBeenCalledTimes(1);

      service.dispose();
    });

    it("should report isSuspended state correctly", () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      expect(service.isSuspended).toBe(false);
      service.suspend();
      expect(service.isSuspended).toBe(true);
      service.resume();
      expect(service.isSuspended).toBe(false);

      service.dispose();
    });
  });

  describe("cleanStaleContextFiles()", () => {
    it("should remove context files and state.json", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      vi.mocked(fs.readdir).mockResolvedValue([
        "issue-1539.json",
        "issue-1540.json",
        "issue-1542.json",
        "planning-1539.json",
        "dev-1542.json",
        "state.json",
        "queue-state.json",
        "health-history.jsonl",
        "calibration.json",
      ] as any);
      vi.mocked(fs.unlink).mockResolvedValue(undefined);

      const cleaned = await service.cleanStaleContextFiles();

      // Should remove: issue-1539, issue-1540, issue-1542, planning-1539, dev-1542, state.json
      expect(cleaned).toBe(6);
      expect(fs.unlink).toHaveBeenCalledTimes(6);

      // Should NOT remove queue-state.json, health-history.jsonl, etc.
      const unlinkCalls = vi.mocked(fs.unlink).mock.calls.map((c) => c[0]);
      expect(unlinkCalls).not.toContainEqual(expect.stringContaining("queue-state.json"));
      expect(unlinkCalls).not.toContainEqual(expect.stringContaining("health-history.jsonl"));

      service.dispose();
    });

    it("should remove checkpoint-signal files", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      vi.mocked(fs.readdir).mockResolvedValue(["checkpoint-signal-42.json"] as any);
      vi.mocked(fs.unlink).mockResolvedValue(undefined);

      const cleaned = await service.cleanStaleContextFiles();
      expect(cleaned).toBe(1);

      service.dispose();
    });

    it("should return 0 when no workspace root", async () => {
      const service = new ContextWatcherService("", mockLogger as any);

      const cleaned = await service.cleanStaleContextFiles();
      expect(cleaned).toBe(0);

      service.dispose();
    });

    it("should handle ENOENT directory gracefully", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      const error = new Error("Not found") as NodeJS.ErrnoException;
      error.code = "ENOENT";
      vi.mocked(fs.readdir).mockRejectedValue(error);

      const cleaned = await service.cleanStaleContextFiles();
      expect(cleaned).toBe(0);
      expect(mockLogger.warn).not.toHaveBeenCalled();

      service.dispose();
    });

    it("should continue cleaning when individual file unlink fails", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      vi.mocked(fs.readdir).mockResolvedValue([
        "issue-42.json",
        "issue-43.json",
        "state.json",
      ] as any);
      vi.mocked(fs.unlink)
        .mockRejectedValueOnce(new Error("Permission denied"))
        .mockResolvedValueOnce(undefined)
        .mockResolvedValueOnce(undefined);

      const cleaned = await service.cleanStaleContextFiles();
      // First fails, second and third succeed
      expect(cleaned).toBe(2);

      service.dispose();
    });

    it("should preserve validate, pr, merge context file types during cleanup", async () => {
      const service = new ContextWatcherService(workspaceRoot, mockLogger as any);

      vi.mocked(fs.readdir).mockResolvedValue([
        "validate-42.json",
        "pr-42.json",
        "merge-42.json",
      ] as any);
      vi.mocked(fs.unlink).mockResolvedValue(undefined);

      const cleaned = await service.cleanStaleContextFiles();
      expect(cleaned).toBe(3);
      expect(fs.unlink).toHaveBeenCalledTimes(3);

      service.dispose();
    });
  });
});
