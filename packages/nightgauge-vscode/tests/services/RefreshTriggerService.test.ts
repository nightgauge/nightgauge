/**
 * RefreshTriggerService.test.ts
 *
 * Unit tests for RefreshTriggerService, focusing on:
 * - File watcher initialization with correct pattern
 * - Event subscription (onCreate and onChange)
 * - Debouncing logic for multiple rapid triggers
 * - Tree provider refresh integration
 * - Graceful disposal and cleanup
 *
 * @see Issue #308 - Add auto-refresh when GitHub issues are created via CLI
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { RefreshTriggerService } from "../../src/services/RefreshTriggerService";

// Mock logger to avoid console noise in tests
const mockLogger = {
  debug: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
};

// Mock vscode module - must be hoisted before imports
vi.mock("vscode", () => {
  // Mock watcher callbacks stored at module level
  const callbacks = {
    onCreate: null as ((uri: any) => void) | null,
    onChange: null as ((uri: any) => void) | null,
  };

  return {
    RelativePattern: class RelativePattern {
      constructor(
        public base: string,
        public pattern: string
      ) {}
    },
    workspace: {
      createFileSystemWatcher: vi.fn((pattern) => ({
        onDidCreate: vi.fn((callback) => {
          callbacks.onCreate = callback;
        }),
        onDidChange: vi.fn((callback) => {
          callbacks.onChange = callback;
        }),
        dispose: vi.fn(),
      })),
    },
    // Export callbacks for test access
    _testCallbacks: callbacks,
  };
});

describe("RefreshTriggerService", () => {
  const workspaceRoot = "/test/workspace";
  let vscode: any;
  let mockWatcherCallbacks: any;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    // Get the mocked vscode module
    vscode = await import("vscode");
    mockWatcherCallbacks = (vscode as any)._testCallbacks;
    mockWatcherCallbacks.onCreate = null;
    mockWatcherCallbacks.onChange = null;
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  describe("Initialization", () => {
    it("should create file watcher with correct RelativePattern", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      expect(vscode.workspace.createFileSystemWatcher).toHaveBeenCalledTimes(1);
      const pattern = vscode.workspace.createFileSystemWatcher.mock.calls[0][0];
      expect(pattern.base).toBe(workspaceRoot);
      expect(pattern.pattern).toBe(".nightgauge/.refresh-trigger");

      service.dispose();
    });

    it("should subscribe to onCreate and onChange events", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      const watcher = vscode.workspace.createFileSystemWatcher.mock.results[0].value;
      expect(watcher.onDidCreate).toHaveBeenCalledTimes(1);
      expect(watcher.onDidChange).toHaveBeenCalledTimes(1);

      service.dispose();
    });

    // 'should log initialization success' test removed (Issue #1826):
    // Pure debug log assertion — initialization is verified by the watcher
    // creation and event subscription tests above.

    it("should handle watcher creation failure gracefully", () => {
      vscode.workspace.createFileSystemWatcher.mockImplementationOnce(() => {
        throw new Error("Watcher creation failed");
      });

      // Should not throw - graceful degradation
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to initialize RefreshTriggerService",
        expect.objectContaining({
          error: "Watcher creation failed",
        })
      );

      service.dispose();
    });
  });

  describe("Debouncing", () => {
    it("should debounce multiple rapid file changes to single refresh", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      const mockProvider = { refresh: vi.fn() };
      service.registerTreeProvider(mockProvider);

      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/.refresh-trigger",
      };

      // Simulate multiple rapid triggers
      mockWatcherCallbacks.onCreate?.(mockUri);
      mockWatcherCallbacks.onChange?.(mockUri);
      mockWatcherCallbacks.onChange?.(mockUri);
      mockWatcherCallbacks.onCreate?.(mockUri);

      // Before debounce completes, no refresh should have occurred
      expect(mockProvider.refresh).not.toHaveBeenCalled();

      // Advance time by 100ms (debounce period)
      vi.advanceTimersByTime(100);

      // After debounce, only one refresh should occur
      expect(mockProvider.refresh).toHaveBeenCalledTimes(1);

      service.dispose();
    });

    it("should reset debounce timer on each trigger", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      const mockProvider = { refresh: vi.fn() };
      service.registerTreeProvider(mockProvider);

      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/.refresh-trigger",
      };

      // First trigger
      mockWatcherCallbacks.onCreate?.(mockUri);
      vi.advanceTimersByTime(50);

      // Second trigger (resets timer)
      mockWatcherCallbacks.onChange?.(mockUri);
      vi.advanceTimersByTime(50);

      // Only 100ms total elapsed, but timer was reset at 50ms
      // So refresh should not have occurred yet
      expect(mockProvider.refresh).not.toHaveBeenCalled();

      // Advance another 50ms to complete the second debounce
      vi.advanceTimersByTime(50);

      // Now refresh should occur
      expect(mockProvider.refresh).toHaveBeenCalledTimes(1);

      service.dispose();
    });
  });

  describe("Tree Provider Refresh", () => {
    it("should refresh all registered tree providers", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      const provider1 = { refresh: vi.fn() };
      const provider2 = { refresh: vi.fn() };
      const provider3 = { refresh: vi.fn() };

      service.registerTreeProvider(provider1);
      service.registerTreeProvider(provider2);
      service.registerTreeProvider(provider3);

      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/.refresh-trigger",
      };
      mockWatcherCallbacks.onCreate?.(mockUri);
      vi.advanceTimersByTime(100);

      expect(provider1.refresh).toHaveBeenCalledTimes(1);
      expect(provider2.refresh).toHaveBeenCalledTimes(1);
      expect(provider3.refresh).toHaveBeenCalledTimes(1);

      service.dispose();
    });

    it("should continue refreshing other providers if one fails", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      const provider1 = { refresh: vi.fn() };
      const provider2 = {
        refresh: vi.fn(() => {
          throw new Error("Refresh failed");
        }),
      };
      const provider3 = { refresh: vi.fn() };

      service.registerTreeProvider(provider1);
      service.registerTreeProvider(provider2);
      service.registerTreeProvider(provider3);

      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/.refresh-trigger",
      };
      mockWatcherCallbacks.onCreate?.(mockUri);
      vi.advanceTimersByTime(100);

      // All providers should be called, even though provider2 threw
      expect(provider1.refresh).toHaveBeenCalledTimes(1);
      expect(provider2.refresh).toHaveBeenCalledTimes(1);
      expect(provider3.refresh).toHaveBeenCalledTimes(1);

      // Error should be logged
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "Failed to refresh tree provider",
        expect.objectContaining({
          error: "Refresh failed",
        })
      );

      service.dispose();
    });

    // 'should log registered provider count' test removed (Issue #1826):
    // Pure debug log assertion — provider registration behavior is verified
    // by the refresh tests above (registered providers receive refresh calls).
  });

  describe("Disposal", () => {
    it("should dispose watcher on service dispose", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      const watcher = vscode.workspace.createFileSystemWatcher.mock.results[0].value;

      service.dispose();

      expect(watcher.dispose).toHaveBeenCalledTimes(1);
    });

    it("should clear debounce timer on dispose", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      const mockProvider = { refresh: vi.fn() };
      service.registerTreeProvider(mockProvider);

      const mockUri = {
        fsPath: "/test/workspace/.nightgauge/.refresh-trigger",
      };
      mockWatcherCallbacks.onCreate?.(mockUri);

      // Dispose before debounce completes
      service.dispose();

      // Advance time - refresh should NOT occur since timer was cleared
      vi.advanceTimersByTime(100);

      expect(mockProvider.refresh).not.toHaveBeenCalled();
    });

    it("should allow multiple dispose calls safely", () => {
      const service = new RefreshTriggerService(workspaceRoot, mockLogger as any);

      // Should not throw on multiple dispose calls
      expect(() => {
        service.dispose();
        service.dispose();
        service.dispose();
      }).not.toThrow();
    });
  });
});
