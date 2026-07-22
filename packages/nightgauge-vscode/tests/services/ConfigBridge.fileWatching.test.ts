/**
 * ConfigBridge.fileWatching.test.ts
 *
 * Unit tests for ConfigBridge file watching behavior, focusing on:
 * - File watchers reacting to config.yaml changes
 * - File watchers reacting to config.local.yaml changes
 * - Debouncing: rapid changes result in single reload
 * - File deletion handled gracefully
 * - Watchers disposed on service dispose
 * - Repository switch triggers cache invalidation
 *
 * @see Issue #473 - ConfigBridge service for unified config access
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Unmock ConfigBridge to test the actual implementation (setup.ts mocks it globally)
vi.unmock("../../src/services/ConfigBridge");

import { ConfigBridge } from "../../src/services/ConfigBridge";

// Mock vscode module
vi.mock("vscode", () => {
  return {
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
      createFileSystemWatcher: vi.fn(() => ({
        onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
        onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
        onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
        dispose: vi.fn(),
      })),
      fs: {
        readFile: vi.fn(),
      },
    },
    Uri: {
      file: (path: string) => ({ fsPath: path }),
    },
    RelativePattern: class RelativePattern {
      constructor(
        public base: string,
        public pattern: string
      ) {}
    },
  };
});

// Track mock calls for IncrediYamlService
let mockReadEffective = vi.fn();
let mockOnDidChangeCallback: (() => void) | null = null;
let mockYamlServiceDispose = vi.fn();

// Mock IncrediYamlService
vi.mock("../../src/views/settings/IncrediYamlService", () => {
  return {
    IncrediYamlService: vi.fn(function () {
      return {
        readEffective: mockReadEffective,
        onDidChange: vi.fn((callback: () => void) => {
          mockOnDidChangeCallback = callback;
          return { dispose: vi.fn() };
        }),
        dispose: mockYamlServiceDispose,
      };
    }),
  };
});

// Mock WorkspaceManager. The bridge no longer subscribes to
// onRepositoryChanged (that event was removed with the current-repo
// pointer) — callers use `retargetToRepository(path)` explicitly.
const createMockWorkspaceManager = () => ({
  getAllRepositories: vi.fn().mockReturnValue([
    {
      name: "test-repo",
      path: "/test/workspace",
    },
  ]),
  isMultiWorkspace: vi.fn().mockReturnValue(false),
  getWorkspaceRoot: vi.fn().mockReturnValue("/test/workspace"),
});

// Default mock config result
const createMockConfigResult = () => ({
  config: {
    project: { number: 10, auto_dates: true },
    pipeline: { ci_timeout: 10, auto_fix: true },
    batch: { max_issues: 5 },
    branch: { base: "main" },
  },
  sources: {
    "project.number": "project",
    "pipeline.ci_timeout": "default",
    "batch.max_issues": "local",
  },
  validation: { valid: true, errors: [] },
  envVarsApplied: [],
  cliOverrides: [],
  envVarErrors: [],
  tiers: {
    hasDefaults: true,
    hasGlobal: false,
    hasProject: true,
    hasLocal: true,
    hasEnv: false,
    hasCli: false,
  },
  mergeTimeMs: 5,
});

describe("ConfigBridge File Watching", () => {
  const workspaceRoot = "/test/workspace";
  let mockWorkspaceManager: ReturnType<typeof createMockWorkspaceManager>;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    ConfigBridge.resetInstance();
    mockWorkspaceManager = createMockWorkspaceManager();
    mockOnDidChangeCallback = null;
    mockReadEffective = vi.fn().mockResolvedValue(createMockConfigResult());
    mockYamlServiceDispose = vi.fn();
  });

  afterEach(() => {
    ConfigBridge.resetInstance();
    vi.useRealTimers();
  });

  describe("File Change Detection", () => {
    it("should subscribe to IncrediYamlService onDidChange", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      expect(mockOnDidChangeCallback).not.toBeNull();
    });

    it("should reload config when file change is detected", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      // Clear the initial call
      mockReadEffective.mockClear();

      // Trigger file change
      mockOnDidChangeCallback?.();

      // Advance past debounce timer
      await vi.advanceTimersByTimeAsync(150);

      expect(mockReadEffective).toHaveBeenCalledTimes(1);
    });

    it("should fire onConfigChanged when file changes", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const changeHandler = vi.fn();
      bridge.onConfigChanged(changeHandler);

      // Clear handler from initialization call
      changeHandler.mockClear();

      // Trigger file change
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(150);

      expect(changeHandler).toHaveBeenCalledTimes(1);
      expect(changeHandler).toHaveBeenCalledWith(
        expect.objectContaining({
          config: expect.any(Object),
          sources: expect.any(Object),
        })
      );
    });

    it("should update cached config after file change", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const initialConfig = bridge.getEffectiveConfig();
      expect(initialConfig?.config.project?.number).toBe(10);

      // Update mock to return new config
      mockReadEffective.mockResolvedValue({
        ...createMockConfigResult(),
        config: {
          ...createMockConfigResult().config,
          project: { number: 20, auto_dates: false },
        },
      });

      // Trigger file change
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(150);

      const newConfig = bridge.getEffectiveConfig();
      expect(newConfig?.config.project?.number).toBe(20);
    });
  });

  describe("Debouncing", () => {
    it("should debounce rapid file changes", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      mockReadEffective.mockClear();

      // Trigger multiple rapid changes
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(50);
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(50);
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(50);
      mockOnDidChangeCallback?.();

      // Advance past debounce timer
      await vi.advanceTimersByTimeAsync(150);

      // Should only reload once despite 4 change events
      expect(mockReadEffective).toHaveBeenCalledTimes(1);
    });

    it("should respect 100ms debounce delay", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      mockReadEffective.mockClear();

      // Trigger change
      mockOnDidChangeCallback?.();

      // Not yet reloaded
      await vi.advanceTimersByTimeAsync(50);
      expect(mockReadEffective).not.toHaveBeenCalled();

      // Still not reloaded
      await vi.advanceTimersByTimeAsync(40);
      expect(mockReadEffective).not.toHaveBeenCalled();

      // Now it should reload (100ms passed)
      await vi.advanceTimersByTimeAsync(20);
      expect(mockReadEffective).toHaveBeenCalledTimes(1);
    });

    it("should reset debounce timer on each new change", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      mockReadEffective.mockClear();

      // Trigger change
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(80);

      // Another change before debounce expires - resets timer
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(80);

      // Still not reloaded
      expect(mockReadEffective).not.toHaveBeenCalled();

      // Complete the debounce period
      await vi.advanceTimersByTimeAsync(30);
      expect(mockReadEffective).toHaveBeenCalledTimes(1);
    });

    it("should fire single onConfigChanged after debounced changes", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const changeHandler = vi.fn();
      bridge.onConfigChanged(changeHandler);
      changeHandler.mockClear();

      // Trigger multiple rapid changes
      for (let i = 0; i < 5; i++) {
        mockOnDidChangeCallback?.();
        await vi.advanceTimersByTimeAsync(30);
      }

      // Advance past debounce
      await vi.advanceTimersByTimeAsync(150);

      // Should only fire once
      expect(changeHandler).toHaveBeenCalledTimes(1);
    });
  });

  describe("Explicit repository re-targeting", () => {
    it("recreates IncrediYamlService and reloads on retargetToRepository()", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      mockYamlServiceDispose.mockClear();
      mockReadEffective.mockClear();

      await bridge.retargetToRepository("/other/workspace");

      expect(mockYamlServiceDispose).toHaveBeenCalled();
      expect(mockReadEffective).toHaveBeenCalled();
    });

    it("fires onConfigChanged after retarget", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const changeHandler = vi.fn();
      bridge.onConfigChanged(changeHandler);
      changeHandler.mockClear();

      await bridge.retargetToRepository("/other/workspace");
      await vi.advanceTimersByTimeAsync(10);

      expect(changeHandler).toHaveBeenCalled();
    });
  });

  describe("Error Handling", () => {
    it("should keep previous config when reload fails", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const initialConfig = bridge.getEffectiveConfig();
      expect(initialConfig?.config.project?.number).toBe(10);

      // Make reload fail
      mockReadEffective.mockRejectedValue(new Error("Read failed"));

      // Trigger file change
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(150);

      // Should still have the old config
      const currentConfig = bridge.getEffectiveConfig();
      expect(currentConfig?.config.project?.number).toBe(10);
    });

    it("should not fire onConfigChanged when reload fails", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const changeHandler = vi.fn();
      bridge.onConfigChanged(changeHandler);
      changeHandler.mockClear();

      // Make reload fail
      mockReadEffective.mockRejectedValue(new Error("Read failed"));

      // Trigger file change
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(150);

      // Should not fire change event on error
      expect(changeHandler).not.toHaveBeenCalled();
    });

    it("should fire onValidationError when config is invalid", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const errorHandler = vi.fn();
      bridge.onValidationError(errorHandler);

      const validationErrors = [{ field: "project.number", message: "Must be positive" }];

      mockReadEffective.mockResolvedValue({
        ...createMockConfigResult(),
        validation: { valid: false, errors: validationErrors },
      });

      // Trigger file change
      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(150);

      expect(errorHandler).toHaveBeenCalledWith(validationErrors);
    });
  });

  describe("Disposal", () => {
    it("should clear debounce timer on dispose", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      mockReadEffective.mockClear();

      // Trigger change (starts debounce timer)
      mockOnDidChangeCallback?.();

      // Dispose before timer fires
      bridge.dispose();

      // Advance time
      await vi.advanceTimersByTimeAsync(200);

      // Should not have reloaded (timer was cleared)
      expect(mockReadEffective).not.toHaveBeenCalled();
    });

    it("should dispose IncrediYamlService on dispose", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      mockYamlServiceDispose.mockClear();

      bridge.dispose();

      expect(mockYamlServiceDispose).toHaveBeenCalled();
    });

    it("should dispose event emitters on dispose", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const changeHandler = vi.fn();
      bridge.onConfigChanged(changeHandler);

      bridge.dispose();

      // After dispose, events should not fire (emitter disposed)
      expect(bridge.isInitialized()).toBe(false);
    });

    it("should handle dispose called before initialization", () => {
      const bridge = ConfigBridge.getInstance();

      // Should not throw
      expect(() => bridge.dispose()).not.toThrow();
    });

    it("should handle file changes after dispose gracefully", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      // Capture the callback before dispose clears it
      const savedCallback = mockOnDidChangeCallback;

      bridge.dispose();

      // Simulate late file change event
      mockReadEffective.mockClear();
      savedCallback?.();
      await vi.advanceTimersByTimeAsync(200);

      // Should not reload after dispose
      // Note: The timer was cleared on dispose, so this won't fire
      expect(mockReadEffective).not.toHaveBeenCalled();
    });
  });

  describe("Config File Types", () => {
    it("should reload for any config file type change", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      mockReadEffective.mockClear();

      // IncrediYamlService handles all file types (config.yaml, config.local.yaml, etc.)
      // ConfigBridge just subscribes to onDidChange which fires for any of them

      mockOnDidChangeCallback?.();
      await vi.advanceTimersByTimeAsync(150);

      expect(mockReadEffective).toHaveBeenCalledTimes(1);
    });
  });

  describe("Concurrent Changes", () => {
    it("handles concurrent file change and explicit retarget", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      mockReadEffective.mockClear();

      // Trigger file change
      mockOnDidChangeCallback?.();

      // Immediately retarget to a different repo
      await bridge.retargetToRepository("/other/workspace");

      // Wait for all operations
      await vi.advanceTimersByTimeAsync(200);

      // Should have loaded config (at least once for the retarget)
      expect(mockReadEffective).toHaveBeenCalled();
    });
  });
});
