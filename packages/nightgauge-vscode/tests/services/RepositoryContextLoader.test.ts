/**
 * RepositoryContextLoader.test.ts
 *
 * Unit tests for RepositoryContextLoader service, focusing on:
 * - Singleton pattern (getInstance, resetInstance)
 * - Context directory path resolution
 * - Context file path generation
 * - CLAUDE.md loading with precedence
 * - Docs and standards file loading
 * - Repository switching and cache invalidation
 * - Path traversal prevention
 *
 * @see Issue #327 - Repository-scoped context loading and pipeline isolation
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs/promises";
import * as vscode from "vscode";
import { RepositoryContextLoader } from "../../src/services/RepositoryContextLoader";
import { Repository } from "../../src/models/Repository";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";

// Mock fs/promises
vi.mock("fs/promises");

// Mock vscode module
vi.mock("vscode", () => ({
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
    workspaceFolders: undefined as any,
    getWorkspaceFolder: vi.fn(),
  },
  window: {
    activeTextEditor: undefined as any,
    onDidChangeActiveTextEditor: undefined as any,
  },
}));

describe("RepositoryContextLoader", () => {
  const workspaceRoot = "/test/workspace";
  const repoPath = "/test/workspace/packages/frontend";

  // Mock WorkspaceManager. Tests that previously relied on
  // `_triggerRepoChange` now hit the resolveActiveRepository path through
  // `getAllRepositories()` + `role` fallback. We expose `_triggerWorkspaceChange`
  // for suites that need to simulate reload events.
  const createMockWorkspaceManager = (
    currentRepo: Repository | null = null,
    mode: "single" | "multi-workspace" = "single"
  ): WorkspaceManager => {
    const onWorkspaceChangedListeners: Array<(repos: Repository[]) => void> = [];
    const allRepos = currentRepo ? [currentRepo] : [];

    return {
      getAllRepositories: vi.fn(() => allRepos),
      getWorkspaceRoot: vi.fn(() => workspaceRoot),
      detectWorkspaceMode: vi.fn(() => mode),
      isMultiWorkspace: vi.fn(() => mode === "multi-workspace"),
      onWorkspaceChanged: vi.fn((listener) => {
        onWorkspaceChangedListeners.push(listener);
        return { dispose: () => {} };
      }),
      _triggerWorkspaceChange: (repos: Repository[]) => {
        onWorkspaceChangedListeners.forEach((l) => l(repos));
      },
    } as unknown as WorkspaceManager & {
      _triggerWorkspaceChange: (repos: Repository[]) => void;
    };
  };

  beforeEach(() => {
    vi.clearAllMocks();
    // Reset singleton between tests
    RepositoryContextLoader.resetInstance();
    // Setup default workspace folders
    vi.mocked(vscode.workspace).workspaceFolders = [{ uri: { fsPath: workspaceRoot } }] as any;
  });

  afterEach(() => {
    RepositoryContextLoader.resetInstance();
  });

  describe("Singleton Pattern", () => {
    it("should return the same instance on multiple calls", () => {
      const instance1 = RepositoryContextLoader.getInstance();
      const instance2 = RepositoryContextLoader.getInstance();

      expect(instance1).toBe(instance2);
    });

    it("should clear instance on resetInstance()", () => {
      const instance1 = RepositoryContextLoader.getInstance();
      RepositoryContextLoader.resetInstance();
      const instance2 = RepositoryContextLoader.getInstance();

      expect(instance1).not.toBe(instance2);
    });
  });

  describe("initialize()", () => {
    it("should set initialized flag after initialization", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      expect(loader.isInitialized()).toBe(false);

      await loader.initialize(manager);

      expect(loader.isInitialized()).toBe(true);
    });

    it("should only subscribe to workspace changes once on multiple inits", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);
      await loader.initialize(manager);

      expect(manager.onWorkspaceChanged).toHaveBeenCalledTimes(1);
    });

    it("should subscribe to workspace changes", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      expect(manager.onWorkspaceChanged).toHaveBeenCalled();
    });
  });

  describe("getContextDir()", () => {
    it("should return correct path for current repository", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const contextDir = loader.getContextDir();

      expect(contextDir).toBe(`${repoPath}/.nightgauge/pipeline`);
    });

    it("should return correct path for specified repository", async () => {
      const currentRepo = new Repository("frontend", repoPath);
      const otherRepo = new Repository("backend", "/test/workspace/packages/backend");
      const manager = createMockWorkspaceManager(currentRepo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const contextDir = loader.getContextDir(otherRepo);

      expect(contextDir).toBe("/test/workspace/packages/backend/.nightgauge/pipeline");
    });

    it("should fallback to workspace root when no repository", async () => {
      const manager = createMockWorkspaceManager(null);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const contextDir = loader.getContextDir();

      expect(contextDir).toBe(`${workspaceRoot}/.nightgauge/pipeline`);
    });
  });

  describe("getContextFile()", () => {
    it("should return correct path for issue context file", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const filePath = loader.getContextFile("issue", 42);

      expect(filePath).toBe(`${repoPath}/.nightgauge/pipeline/issue-42.json`);
    });

    it("should return correct path for planning context file", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const filePath = loader.getContextFile("planning", 42);

      expect(filePath).toBe(`${repoPath}/.nightgauge/pipeline/planning-42.json`);
    });

    it("should return correct path for state.json", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const filePath = loader.getContextFile("state");

      expect(filePath).toBe(`${repoPath}/.nightgauge/pipeline/state.json`);
    });

    it("should return correct path for batch-state.json", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const filePath = loader.getContextFile("batch-state");

      expect(filePath).toBe(`${repoPath}/.nightgauge/pipeline/batch-state.json`);
    });

    it("should return correct paths for all context file types", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      expect(loader.getContextFile("dev", 42)).toBe(`${repoPath}/.nightgauge/pipeline/dev-42.json`);
      expect(loader.getContextFile("validate", 42)).toBe(
        `${repoPath}/.nightgauge/pipeline/validate-42.json`
      );
      expect(loader.getContextFile("pr", 42)).toBe(`${repoPath}/.nightgauge/pipeline/pr-42.json`);
      expect(loader.getContextFile("running", 42)).toBe(
        `${repoPath}/.nightgauge/pipeline/running-42.json`
      );
    });

    it("should throw error for unknown context file type", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      expect(() => loader.getContextFile("unknown" as any, 42)).toThrow(
        "Unknown context file type: unknown"
      );
    });
  });

  describe("getPlansDir() and getPlanFile()", () => {
    it("should return correct plans directory", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const plansDir = loader.getPlansDir();

      expect(plansDir).toBe(`${repoPath}/.nightgauge/plans`);
    });

    it("should return correct plan file path with slug", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const planPath = loader.getPlanFile(42, "add-auth");

      expect(planPath).toBe(`${repoPath}/.nightgauge/plans/42-add-auth.md`);
    });

    it("should return default plan file name without slug", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const planPath = loader.getPlanFile(42);

      expect(planPath).toBe(`${repoPath}/.nightgauge/plans/42-plan.md`);
    });
  });

  describe("loadClaudeMd()", () => {
    it("should load CLAUDE.md from repository", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile).mockResolvedValueOnce("# Repository CLAUDE.md");

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const content = await loader.loadClaudeMd();

      expect(content).toBe("# Repository CLAUDE.md");
      expect(fs.readFile).toHaveBeenCalledWith(`${repoPath}/CLAUDE.md`, "utf-8");
    });

    it("should fallback to workspace CLAUDE.md if repo has none", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile)
        .mockRejectedValueOnce(new Error("ENOENT"))
        .mockResolvedValueOnce("# Workspace CLAUDE.md");

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const content = await loader.loadClaudeMd();

      expect(content).toBe("# Workspace CLAUDE.md");
    });

    it("should return null if no CLAUDE.md exists", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const content = await loader.loadClaudeMd();

      expect(content).toBeNull();
    });

    it("should cache CLAUDE.md content", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile).mockResolvedValue("# CLAUDE.md");

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      await loader.loadClaudeMd();
      await loader.loadClaudeMd();

      // Should only read once due to caching
      expect(fs.readFile).toHaveBeenCalledTimes(1);
    });
  });

  describe("getPrecedence()", () => {
    it("should return repository source when repo CLAUDE.md exists", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.access).mockResolvedValueOnce(undefined);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const precedence = await loader.getPrecedence();

      expect(precedence.source).toBe("repository");
      expect(precedence.configPath).toBe(`${repoPath}/CLAUDE.md`);
    });

    it("should return workspace source when only workspace CLAUDE.md exists", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.access)
        .mockRejectedValueOnce(new Error("ENOENT"))
        .mockResolvedValueOnce(undefined);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const precedence = await loader.getPrecedence();

      expect(precedence.source).toBe("workspace");
      expect(precedence.configPath).toBe(`${workspaceRoot}/CLAUDE.md`);
    });

    it("should return default source when no CLAUDE.md exists", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const precedence = await loader.getPrecedence();

      expect(precedence.source).toBe("default");
      expect(precedence.configPath).toBeUndefined();
    });
  });

  describe("loadDocsFile()", () => {
    it("should load docs file from repository", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile).mockResolvedValueOnce("# Git Workflow");

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const result = await loader.loadDocsFile("GIT_WORKFLOW.md");

      expect(result.found).toBe(true);
      expect(result.content).toBe("# Git Workflow");
      expect(result.sourceRepository).toBe("frontend");
      expect(result.path).toBe(`${repoPath}/docs/GIT_WORKFLOW.md`);
    });

    it("should return not found for missing docs file", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile).mockRejectedValueOnce(new Error("ENOENT"));

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const result = await loader.loadDocsFile("MISSING.md");

      expect(result.found).toBe(false);
      expect(result.content).toBeUndefined();
    });

    it("should prevent path traversal in docs path", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile).mockRejectedValueOnce(new Error("ENOENT"));

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const result = await loader.loadDocsFile("../../../etc/passwd");

      // Path should be sanitized - no traversal
      expect(result.path).toBe(`${repoPath}/docs/etc/passwd`);
    });
  });

  describe("loadStandardsFile()", () => {
    it("should load standards file from repository", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      vi.mocked(fs.readFile).mockResolvedValueOnce("# Security Standards");

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const result = await loader.loadStandardsFile("security.md");

      expect(result.found).toBe(true);
      expect(result.content).toBe("# Security Standards");
      expect(result.sourceRepository).toBe("frontend");
      expect(result.path).toBe(`${repoPath}/standards/security.md`);
    });
  });

  describe("getWorkingDirectory()", () => {
    it("should return repository path as working directory", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const workDir = loader.getWorkingDirectory();

      expect(workDir).toBe(repoPath);
    });

    it("should fallback to workspace root when no repository", async () => {
      const manager = createMockWorkspaceManager(null);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const workDir = loader.getWorkingDirectory();

      expect(workDir).toBe(workspaceRoot);
    });
  });

  describe("Workspace Reload", () => {
    it("clears caches when the workspace repo set changes", async () => {
      const frontendRepo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(frontendRepo) as ReturnType<
        typeof createMockWorkspaceManager
      > & {
        _triggerWorkspaceChange: (repos: Repository[]) => void;
      };

      vi.mocked(fs.readFile).mockResolvedValue("# CLAUDE.md");

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      // Warm the cache
      await loader.loadClaudeMd();
      expect(fs.readFile).toHaveBeenCalledTimes(1);

      // Fire a workspace-changed event — caches should clear and the next
      // read should hit disk again.
      (manager as any)._triggerWorkspaceChange([frontendRepo]);
      await loader.loadClaudeMd();

      expect(fs.readFile).toHaveBeenCalledTimes(2);
    });

    it("fires onContextChanged when the workspace reloads", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo) as ReturnType<
        typeof createMockWorkspaceManager
      > & {
        _triggerWorkspaceChange: (repos: Repository[]) => void;
      };

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const handler = vi.fn();
      loader.onContextChanged(handler);

      (manager as any)._triggerWorkspaceChange([repo]);

      expect(handler).toHaveBeenCalled();
    });
  });

  describe("Backward Compatibility", () => {
    it("should work correctly in single-repo mode", async () => {
      const repo = new Repository("default", workspaceRoot);
      const manager = createMockWorkspaceManager(repo, "single");

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      const contextDir = loader.getContextDir();
      const issuePath = loader.getContextFile("issue", 42);
      const workDir = loader.getWorkingDirectory();

      expect(contextDir).toBe(`${workspaceRoot}/.nightgauge/pipeline`);
      expect(issuePath).toBe(`${workspaceRoot}/.nightgauge/pipeline/issue-42.json`);
      expect(workDir).toBe(workspaceRoot);
    });
  });

  describe("dispose()", () => {
    it("should dispose event emitters and clear caches", async () => {
      const repo = new Repository("frontend", repoPath);
      const manager = createMockWorkspaceManager(repo);

      const loader = RepositoryContextLoader.getInstance();
      await loader.initialize(manager);

      // Load some content into cache
      vi.mocked(fs.readFile).mockResolvedValueOnce("# CLAUDE.md");
      await loader.loadClaudeMd();

      // Should not throw
      expect(() => loader.dispose()).not.toThrow();
    });
  });
});
