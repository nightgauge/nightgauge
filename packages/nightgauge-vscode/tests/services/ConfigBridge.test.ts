/**
 * ConfigBridge.test.ts
 *
 * Unit tests for ConfigBridge service, focusing on:
 * - Singleton pattern (getInstance, resetInstance)
 * - Initialization lifecycle
 * - Configuration loading via IncrediYamlService
 * - Typed section getters
 * - Source tracking (getSource)
 * - Error handling for missing/invalid config files
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

// Mock IncrediYamlService
vi.mock("../../src/views/settings/IncrediYamlService", () => {
  return {
    IncrediYamlService: vi.fn(function () {
      return {
        readEffective: vi.fn().mockResolvedValue({
          config: {
            project: { number: 10, auto_dates: true },
            pipeline: { ci_timeout: 10, auto_fix: true },
            branch: { base: "main" },
          },
          sources: {
            "project.number": "project",
            "pipeline.ci_timeout": "default",
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
        }),
        onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
        dispose: vi.fn(),
      };
    }),
  };
});

// Mock WorkspaceManager. ConfigBridge previously subscribed to
// onRepositoryChanged to re-target on repo switch — that subscription was
// dropped in the current-repo refactor, so the mock no longer needs it.
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

describe("ConfigBridge", () => {
  const workspaceRoot = "/test/workspace";
  let mockWorkspaceManager: ReturnType<typeof createMockWorkspaceManager>;

  beforeEach(() => {
    vi.clearAllMocks();
    ConfigBridge.resetInstance();
    mockWorkspaceManager = createMockWorkspaceManager();
  });

  afterEach(() => {
    ConfigBridge.resetInstance();
  });

  describe("Singleton Pattern", () => {
    it("should return same instance on multiple getInstance calls", () => {
      const instance1 = ConfigBridge.getInstance();
      const instance2 = ConfigBridge.getInstance();

      expect(instance1).toBe(instance2);
    });

    it("should create new instance after resetInstance", () => {
      const instance1 = ConfigBridge.getInstance();
      ConfigBridge.resetInstance();
      const instance2 = ConfigBridge.getInstance();

      expect(instance1).not.toBe(instance2);
    });

    it("should dispose resources on resetInstance", async () => {
      const instance = ConfigBridge.getInstance();
      await instance.initialize(mockWorkspaceManager as any, workspaceRoot);

      ConfigBridge.resetInstance();

      // Getting new instance should work
      const newInstance = ConfigBridge.getInstance();
      expect(newInstance.isInitialized()).toBe(false);
    });
  });

  describe("Initialization", () => {
    it("should set initialized flag after successful initialization", async () => {
      const bridge = ConfigBridge.getInstance();

      expect(bridge.isInitialized()).toBe(false);

      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      expect(bridge.isInitialized()).toBe(true);
    });

    it("should skip re-initialization if already initialized", async () => {
      const bridge = ConfigBridge.getInstance();

      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      // Should still work without errors
      expect(bridge.isInitialized()).toBe(true);
    });

    it("should load config via IncrediYamlService on initialize", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const result = bridge.getEffectiveConfig();
      expect(result).not.toBeNull();
      expect(result?.config.project?.number).toBe(10);
    });

    // The onRepositoryChanged subscription was removed alongside the
    // workspace-global current-repo pointer. Callers that need to
    // re-target ConfigBridge after pointing at a different repo call
    // `retargetToRepository()` explicitly.
  });

  describe("getEffectiveConfig()", () => {
    it("should return null before initialization", () => {
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getEffectiveConfig()).toBeNull();
    });

    it("should return cached ConfigMergeResult after initialization", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const result = bridge.getEffectiveConfig();
      expect(result).not.toBeNull();
      expect(result?.config).toBeDefined();
      expect(result?.sources).toBeDefined();
      expect(result?.validation).toBeDefined();
      expect(result?.tiers).toBeDefined();
    });
  });

  describe("Typed Section Getters", () => {
    beforeEach(async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
    });

    it("getProject() should return typed ProjectConfig", () => {
      const bridge = ConfigBridge.getInstance();
      const project = bridge.getProject();

      expect(project?.number).toBe(10);
      expect(project?.auto_dates).toBe(true);
    });

    it("getPipeline() should return typed PipelineConfig", () => {
      const bridge = ConfigBridge.getInstance();
      const pipeline = bridge.getPipeline();

      expect(pipeline?.ci_timeout).toBe(10);
      expect(pipeline?.auto_fix).toBe(true);
    });

    it("getBranch() should return typed BranchConfig", () => {
      const bridge = ConfigBridge.getInstance();
      const branch = bridge.getBranch();

      expect(branch?.base).toBe("main");
    });

    it("should return undefined for missing sections", () => {
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getRouting()).toBeUndefined();
      expect(bridge.getEnforcement()).toBeUndefined();
      expect(bridge.getAutomations()).toBeUndefined();
    });
  });

  describe("getSource()", () => {
    beforeEach(async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
    });

    it("should return correct source for tracked paths", () => {
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getSource("project.number")).toBe("project");
      expect(bridge.getSource("pipeline.ci_timeout")).toBe("default");
    });

    it('should return "default" for untracked paths', () => {
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getSource("unknown.path")).toBe("default");
    });

    it('should return "default" before initialization', () => {
      ConfigBridge.resetInstance();
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getSource("project.number")).toBe("default");
    });
  });

  describe("getValue()", () => {
    beforeEach(async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);
    });

    it("should return value at path", () => {
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getValue("project.number")).toBe(10);
      expect(bridge.getValue("pipeline.auto_fix")).toBe(true);
    });

    it("should return undefined for non-existent path", () => {
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getValue("unknown.path")).toBeUndefined();
    });

    it("should return undefined before initialization", () => {
      ConfigBridge.resetInstance();
      const bridge = ConfigBridge.getInstance();

      expect(bridge.getValue("project.number")).toBeUndefined();
    });
  });

  describe("Error Handling", () => {
    it("should handle missing config files gracefully", async () => {
      // Mock readEffective to return empty config
      const { IncrediYamlService } = await import("../../src/views/settings/IncrediYamlService");
      (IncrediYamlService as any).mockImplementation(function () {
        return {
          readEffective: vi.fn().mockResolvedValue({
            config: {},
            sources: {},
            validation: { valid: true, errors: [] },
            envVarsApplied: [],
            cliOverrides: [],
            envVarErrors: [],
            tiers: {
              hasDefaults: true,
              hasGlobal: false,
              hasProject: false,
              hasLocal: false,
              hasEnv: false,
              hasCli: false,
            },
            mergeTimeMs: 1,
          }),
          onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
          dispose: vi.fn(),
        };
      });

      ConfigBridge.resetInstance();
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      expect(bridge.getProject()).toBeUndefined();
      expect(bridge.getPipeline()).toBeUndefined();
    });

    it("should fire onValidationError for invalid config", async () => {
      const validationErrors = [{ field: "project.number", message: "Must be a positive integer" }];

      const { IncrediYamlService } = await import("../../src/views/settings/IncrediYamlService");
      (IncrediYamlService as any).mockImplementation(function () {
        return {
          readEffective: vi.fn().mockResolvedValue({
            config: { project: { number: -1 } },
            sources: {},
            validation: { valid: false, errors: validationErrors },
            envVarsApplied: [],
            cliOverrides: [],
            envVarErrors: [],
            tiers: {
              hasDefaults: true,
              hasGlobal: false,
              hasProject: true,
              hasLocal: false,
              hasEnv: false,
              hasCli: false,
            },
            mergeTimeMs: 1,
          }),
          onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
          dispose: vi.fn(),
        };
      });

      ConfigBridge.resetInstance();
      const bridge = ConfigBridge.getInstance();

      const errorHandler = vi.fn();
      bridge.onValidationError(errorHandler);

      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      expect(errorHandler).toHaveBeenCalledWith(validationErrors);
    });
  });

  describe("Events", () => {
    it("should fire onConfigChanged after initialization", async () => {
      const bridge = ConfigBridge.getInstance();
      const changeHandler = vi.fn();
      bridge.onConfigChanged(changeHandler);

      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      expect(changeHandler).toHaveBeenCalled();
      expect(changeHandler.mock.calls[0][0].config).toBeDefined();
    });

    it("should fire onConfigChanged on reload", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      const changeHandler = vi.fn();
      bridge.onConfigChanged(changeHandler);

      await bridge.reload();

      expect(changeHandler).toHaveBeenCalled();
    });
  });

  describe("Disposal", () => {
    it("should dispose cleanly", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      expect(() => bridge.dispose()).not.toThrow();
      expect(bridge.isInitialized()).toBe(false);
      expect(bridge.getEffectiveConfig()).toBeNull();
    });

    it("should allow multiple dispose calls", async () => {
      const bridge = ConfigBridge.getInstance();
      await bridge.initialize(mockWorkspaceManager as any, workspaceRoot);

      expect(() => {
        bridge.dispose();
        bridge.dispose();
        bridge.dispose();
      }).not.toThrow();
    });
  });
});
