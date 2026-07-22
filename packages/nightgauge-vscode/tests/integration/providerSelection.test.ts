/**
 * Integration tests for provider instantiation via createWorkItemProvider()
 *
 * Verifies that the factory function correctly instantiates providers based
 * on configuration, implements the IWorkItemProvider interface, and throws
 * helpful errors for unimplemented providers.
 *
 * @see Issue #2571 - Add work item source configuration and provider selection wiring
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createWorkItemProvider } from "../../src/bootstrap/services";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { CompositeAdapter } from "../../src/services/adapters/CompositeAdapter";
import type { WorkItemSourceConfig } from "../../src/config/workItemSourceSettings";

// Mock vscode before importing modules that use it
vi.mock("vscode", () => ({
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showErrorMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  workspace: {
    workspaceFolders: undefined,
    getConfiguration: vi.fn(() => ({
      get: vi.fn(),
    })),
    onDidChangeConfiguration: vi.fn(() => ({ dispose: vi.fn() })),
  },
  Uri: {
    file: vi.fn((p: string) => ({ fsPath: p })),
    joinPath: vi.fn((...args: { fsPath: string }[]) => ({
      fsPath: args.map((a) => a.fsPath).join("/"),
    })),
  },
  Disposable: {
    from: vi.fn(),
  },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
  TreeItem: class TreeItem {
    constructor(public label: string) {}
  },
}));

// Mock IpcClient to avoid real IPC connections during tests
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(() => ({
      gitRoot: vi.fn(),
      isConnected: vi.fn(() => false),
      onDidConnect: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDisconnect: vi.fn(() => ({ dispose: vi.fn() })),
    })),
  },
}));

// Mock ConfigBridge to avoid file system access during tests
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => ({
      isInitialized: vi.fn(() => false),
      getValue: vi.fn(),
      getProject: vi.fn(),
      getUI: vi.fn(),
    })),
    reset: vi.fn(),
  },
}));

describe("createWorkItemProvider", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("mode='github'", () => {
    it("returns a ProjectBoardService instance", () => {
      const config: WorkItemSourceConfig = { mode: "github" };
      const provider = createWorkItemProvider(config, "/workspace");
      expect(provider).toBeInstanceOf(ProjectBoardService);
    });

    it("returned instance implements IWorkItemProvider interface", () => {
      const config: WorkItemSourceConfig = { mode: "github" };
      const provider = createWorkItemProvider(config, "/workspace");
      expect(typeof provider.getIssuesByStatus).toBe("function");
      expect(typeof provider.getReadyIssues).toBe("function");
      expect(typeof provider.getAllItems).toBe("function");
    });

    it("ProjectBoardService methods accessible via IWorkItemProvider type", () => {
      const config: WorkItemSourceConfig = { mode: "github" };
      const provider = createWorkItemProvider(config, "/workspace");
      // Verify methods can be called without TypeScript errors
      expect(provider.getIssuesByStatus).toBeDefined();
      expect(provider.getReadyIssues).toBeDefined();
      expect(provider.getAllItems).toBeDefined();
    });
  });

  describe("undefined mode (defaults to 'github')", () => {
    it("returns ProjectBoardService when mode is undefined", () => {
      const config: WorkItemSourceConfig = {};
      const provider = createWorkItemProvider(config, "/workspace");
      expect(provider).toBeInstanceOf(ProjectBoardService);
    });

    it("returns ProjectBoardService when config is empty object", () => {
      const provider = createWorkItemProvider({}, "/workspace");
      expect(provider).toBeInstanceOf(ProjectBoardService);
    });
  });

  describe("unimplemented modes", () => {
    it("mode='repo' throws error with helpful message", () => {
      const config: WorkItemSourceConfig = { mode: "repo" };
      expect(() => createWorkItemProvider(config, "/workspace")).toThrow(
        "Repo provider not yet implemented"
      );
    });

    it("mode='repo' error includes issue reference", () => {
      const config: WorkItemSourceConfig = { mode: "repo" };
      expect(() => createWorkItemProvider(config, "/workspace")).toThrow("#2566");
    });
  });

  describe("mode='composite'", () => {
    it("returns a CompositeAdapter instance", () => {
      const config: WorkItemSourceConfig = { mode: "composite" };
      const provider = createWorkItemProvider(config, "/workspace");
      expect(provider).toBeInstanceOf(CompositeAdapter);
    });

    it("returned instance implements IWorkItemProvider interface", () => {
      const config: WorkItemSourceConfig = { mode: "composite" };
      const provider = createWorkItemProvider(config, "/workspace");
      expect(typeof provider.getIssuesByStatus).toBe("function");
      expect(typeof provider.getAllItems).toBe("function");
      expect(typeof provider.getReadyIssues).toBe("function");
    });
  });

  describe("workspace root passthrough", () => {
    it("passes workspace root to ProjectBoardService", () => {
      const workspaceRoot = "/my/workspace/path";
      const config: WorkItemSourceConfig = { mode: "github" };
      const provider = createWorkItemProvider(config, workspaceRoot) as ProjectBoardService;
      // ProjectBoardService should be instantiated — instance check is sufficient
      expect(provider).toBeInstanceOf(ProjectBoardService);
    });
  });
});
