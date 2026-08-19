/**
 * WorkspaceManager.test.ts
 *
 * Unit tests for WorkspaceManager service, focusing on:
 * - Singleton pattern (getInstance, resetInstance)
 * - Workspace detection and initialization
 * - Repository loading and switching
 * - Event emission on state changes
 * - Session persistence (workspaceState)
 *
 * @see Issue #324 - WorkspaceManager service and repository loading
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs/promises";
import * as vscode from "vscode";
import { WorkspaceManager } from "../../src/services/WorkspaceManager";
import { Repository } from "../../src/models/Repository";

// Mock fs/promises
vi.mock("fs/promises");

// Mock workspaceDetection module
vi.mock("../../src/utils/workspaceDetection", () => ({
  detectWorkspaceType: vi.fn(),
  loadWorkspaceConfig: vi.fn(),
}));

// Mock configPathResolver so getRepoIdentity doesn't hit the filesystem
vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: vi.fn().mockResolvedValue(null),
  resolveConfigPath: vi.fn().mockResolvedValue({
    path: "/test/.nightgauge/config.yaml",
    isLegacy: false,
    exists: false,
  }),
  resolveConfigPathSync: vi.fn().mockReturnValue({
    path: "/test/.nightgauge/config.yaml",
    isLegacy: false,
    exists: false,
  }),
  getConfigPaths: vi.fn().mockReturnValue({
    primary: "/test/.nightgauge/config.yaml",
    legacy: "/test/.nightgauge/nightgauge.yaml",
  }),
  getRelativeConfigPath: vi.fn().mockReturnValue(".nightgauge/config.yaml"),
  CONFIG_FILE_NAME: "config.yaml",
  LEGACY_CONFIG_FILE_NAME: "nightgauge.yaml",
  LOCAL_CONFIG_FILE_NAME: "config.local.yaml",
  NIGHTGAUGE_DIR: ".nightgauge",
}));

// Import mocked modules
import { detectWorkspaceType, loadWorkspaceConfig } from "../../src/utils/workspaceDetection";
import { resolveConfigPath } from "../../src/utils/configPathResolver";

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
  },
}));

describe("WorkspaceManager", () => {
  const workspaceRoot = "/test/workspace";

  // Mock workspace state (simulates VSCode Memento)
  const createMockWorkspaceState = () => {
    const storage = new Map<string, unknown>();
    return {
      get: vi.fn((key: string, defaultValue?: unknown) => {
        return storage.get(key) ?? defaultValue;
      }),
      update: vi.fn(async (key: string, value: unknown) => {
        if (value === undefined) {
          storage.delete(key);
        } else {
          storage.set(key, value);
        }
      }),
      keys: vi.fn(() => Array.from(storage.keys())),
    };
  };

  beforeEach(() => {
    vi.clearAllMocks();
    // Reset singleton between tests
    WorkspaceManager.resetInstance();
  });

  afterEach(() => {
    WorkspaceManager.resetInstance();
  });

  describe("Singleton Pattern", () => {
    it("should return the same instance on multiple calls", () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const instance1 = WorkspaceManager.getInstance(workspaceRoot);
      const instance2 = WorkspaceManager.getInstance(workspaceRoot);

      expect(instance1).toBe(instance2);
    });

    it("should clear instance on resetInstance()", () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const instance1 = WorkspaceManager.getInstance(workspaceRoot);
      WorkspaceManager.resetInstance();
      const instance2 = WorkspaceManager.getInstance(workspaceRoot);

      expect(instance1).not.toBe(instance2);
    });
  });

  describe("initialize()", () => {
    it("should initialize in single-repo mode when no workspace config", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.isInitialized()).toBe(true);
      expect(manager.detectWorkspaceMode()).toBe("single");
      expect(manager.isMultiWorkspace()).toBe(false);
      expect(manager.getRepositoryCount()).toBe(1);
    });

    it("should initialize in multi-workspace mode with explicit config", async () => {
      const workspaceConfig = {
        workspace: { name: "Test Workspace" },
        repositories: [
          {
            name: "frontend",
            path: "./packages/frontend",
            role: "primary" as const,
          },
          {
            name: "backend",
            path: "./packages/backend",
            role: "secondary" as const,
          },
        ],
      };

      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "multi-workspace",
        config: workspaceConfig,
        detection_method: "explicit",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.isInitialized()).toBe(true);
      expect(manager.detectWorkspaceMode()).toBe("multi-workspace");
      expect(manager.isMultiWorkspace()).toBe(true);
      expect(manager.getRepositoryCount()).toBe(2);
      expect(manager.getRepositoryNames()).toEqual(["frontend", "backend"]);
    });

    it("should initialize in multi-workspace mode with auto-detected folders", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "multi-workspace",
        config: null,
        detection_method: "auto-detected",
      });

      // Mock workspace folders
      vi.mocked(vscode.workspace).workspaceFolders = [
        { name: "repo1", uri: { fsPath: "/test/repo1" } },
        { name: "repo2", uri: { fsPath: "/test/repo2" } },
      ] as any;

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.isInitialized()).toBe(true);
      expect(manager.detectWorkspaceMode()).toBe("multi-workspace");
      expect(manager.getRepositoryCount()).toBe(2);
    });

    it("should only initialize once on multiple calls", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();
      await manager.initialize();

      // Should only call detectWorkspaceType once
      expect(detectWorkspaceType).toHaveBeenCalledTimes(1);
    });

    it("should handle initialization errors gracefully", async () => {
      vi.mocked(detectWorkspaceType).mockRejectedValue(new Error("Detection failed"));
      const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      // Should fallback to single-repo mode
      expect(manager.isInitialized()).toBe(true);
      expect(manager.detectWorkspaceMode()).toBe("single");

      errorSpy.mockRestore();
    });
  });

  // NOTE: The `getCurrentRepository()` + `switchRepository()` surface was
  // removed when the workspace-global current-repo pointer was retired in
  // favor of `resolveActiveRepository()` (derived from the active editor).
  // See `tests/utils/resolveActiveRepository.test.ts` for coverage of the
  // replacement.

  describe("getRepository()", () => {
    it("should return repository by name", async () => {
      const workspaceConfig = {
        workspace: { name: "Test" },
        repositories: [
          { name: "frontend", path: "./frontend" },
          { name: "backend", path: "./backend" },
        ],
      };

      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "multi-workspace",
        config: workspaceConfig,
        detection_method: "explicit",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      const repo = manager.getRepository("frontend");
      expect(repo).toBeDefined();
      expect(repo?.name).toBe("frontend");
    });

    it("should return undefined for unknown repository", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.getRepository("unknown")).toBeUndefined();
    });
  });

  describe("getAllRepositories()", () => {
    it("should return all loaded repositories", async () => {
      const workspaceConfig = {
        workspace: { name: "Test" },
        repositories: [
          { name: "frontend", path: "./frontend" },
          { name: "backend", path: "./backend" },
          { name: "shared", path: "./shared" },
        ],
      };

      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "multi-workspace",
        config: workspaceConfig,
        detection_method: "explicit",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      const repos = manager.getAllRepositories();
      expect(repos).toHaveLength(3);
      expect(repos.map((r) => r.name)).toEqual(["frontend", "backend", "shared"]);
    });

    it("should return single repository in single mode", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      const repos = manager.getAllRepositories();
      expect(repos).toHaveLength(1);
      expect(repos[0].name).toBe("workspace");
    });
  });

  // NOTE: The persisted "last repo" pointer was dropped along with the
  // current-repo refactor. Active repo is now derived from the editor and
  // not session state, so nothing is persisted to workspaceState anymore.

  describe("onWorkspaceChanged event", () => {
    it("should fire on initialization", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      const handler = vi.fn();
      manager.onWorkspaceChanged(handler);

      await manager.initialize();

      expect(handler).toHaveBeenCalledTimes(1);
      expect(handler).toHaveBeenCalledWith(
        expect.arrayContaining([expect.objectContaining({ name: "workspace" })])
      );
    });

    it("should fire on reload", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      const handler = vi.fn();
      manager.onWorkspaceChanged(handler);

      await manager.reload();

      expect(handler).toHaveBeenCalledTimes(1);
    });
  });

  describe("reload()", () => {
    it("should re-detect workspace and reload repositories", async () => {
      // First detection: single mode
      vi.mocked(detectWorkspaceType).mockResolvedValueOnce({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.detectWorkspaceMode()).toBe("single");
      expect(manager.getRepositoryCount()).toBe(1);

      // Second detection: multi-workspace mode (config changed)
      const newConfig = {
        workspace: { name: "Updated" },
        repositories: [
          { name: "app", path: "./app" },
          { name: "lib", path: "./lib" },
        ],
      };

      vi.mocked(detectWorkspaceType).mockResolvedValueOnce({
        type: "multi-workspace",
        config: newConfig,
        detection_method: "explicit",
      });

      await manager.reload();

      expect(manager.detectWorkspaceMode()).toBe("multi-workspace");
      expect(manager.getRepositoryCount()).toBe(2);
    });
  });

  describe("getWorkspaceConfig()", () => {
    it("should return null in single mode", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.getWorkspaceConfig()).toBeNull();
    });

    it("should return config in explicit multi-workspace mode", async () => {
      const workspaceConfig = {
        workspace: { name: "Test Workspace", description: "Test" },
        repositories: [{ name: "repo", path: "./repo" }],
      };

      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "multi-workspace",
        config: workspaceConfig,
        detection_method: "explicit",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.getWorkspaceConfig()).toEqual(workspaceConfig);
    });
  });

  describe("getDetectionMethod()", () => {
    it("should return detection method used", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "multi-workspace",
        config: null,
        detection_method: "auto-detected",
      });

      vi.mocked(vscode.workspace).workspaceFolders = [
        { name: "repo1", uri: { fsPath: "/test/repo1" } },
        { name: "repo2", uri: { fsPath: "/test/repo2" } },
      ] as any;

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      expect(manager.getDetectionMethod()).toBe("auto-detected");
    });
  });

  describe("dispose()", () => {
    it("should dispose event emitters", async () => {
      vi.mocked(detectWorkspaceType).mockResolvedValue({
        type: "single",
        config: null,
        detection_method: "single-repo",
      });

      const manager = WorkspaceManager.getInstance(workspaceRoot);
      await manager.initialize();

      // Should not throw
      expect(() => manager.dispose()).not.toThrow();
    });
  });
});

describe("Repository", () => {
  const repoPath = "/test/repo";

  beforeEach(() => {
    vi.clearAllMocks();
    // Default: config file exists (individual tests that need "not found" override this)
    vi.mocked(resolveConfigPath).mockResolvedValue({
      path: `${repoPath}/.nightgauge/config.yaml`,
      isLegacy: false,
      exists: true,
    });
  });

  describe("constructor", () => {
    it("should create repository with required fields", () => {
      const repo = new Repository("test-repo", repoPath);

      expect(repo.name).toBe("test-repo");
      expect(repo.path).toBe(repoPath);
      expect(repo.role).toBeUndefined();
    });

    it("should create repository with role", () => {
      const repo = new Repository("test-repo", repoPath, "primary");

      expect(repo.role).toBe("primary");
    });
  });

  describe("loadConfig()", () => {
    it("should load and cache nightgauge config", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
github:
  owner: test
  repo: my-repo
  project_number: 1
`);

      const repo = new Repository("test", repoPath);
      const config = await repo.loadConfig();

      expect(config).not.toBeNull();
      expect(config?.github?.owner).toBe("test");
      expect(config?.github?.repo).toBe("my-repo");
    });

    it("should return cached config on subsequent calls", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue("github:\n  owner: test");

      const repo = new Repository("test", repoPath);

      const config1 = await repo.loadConfig();
      const config2 = await repo.loadConfig();

      expect(config1).toBe(config2);
      // Should only read file once
      expect(fs.readFile).toHaveBeenCalledTimes(1);
    });

    it("should return null when config file does not exist", async () => {
      vi.mocked(resolveConfigPath).mockResolvedValueOnce({
        path: `${repoPath}/.nightgauge/config.yaml`,
        isLegacy: false,
        exists: false,
      });
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const repo = new Repository("test", repoPath);
      const config = await repo.loadConfig();

      expect(config).toBeNull();
    });

    it("should return null on invalid YAML", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue("invalid: yaml: syntax [[[");

      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      const repo = new Repository("test", repoPath);
      const config = await repo.loadConfig();

      expect(config).toBeNull();

      warnSpy.mockRestore();
    });
  });

  describe("reloadConfig()", () => {
    it("should reload config from disk", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile)
        .mockResolvedValueOnce("github:\n  owner: old")
        .mockResolvedValueOnce("github:\n  owner: new");

      const repo = new Repository("test", repoPath);

      const config1 = await repo.loadConfig();
      expect(config1?.github?.owner).toBe("old");

      const config2 = await repo.reloadConfig();
      expect(config2?.github?.owner).toBe("new");
    });
  });

  describe("clearCache()", () => {
    it("should clear cached config", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue("github:\n  owner: test");

      const repo = new Repository("test", repoPath);

      await repo.loadConfig();
      expect(repo.isConfigLoaded).toBe(true);

      repo.clearCache();
      expect(repo.isConfigLoaded).toBe(false);
    });
  });

  describe("hasIncrediConfig()", () => {
    it("should return true when config file exists", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const repo = new Repository("test", repoPath);
      const hasConfig = await repo.hasIncrediConfig();

      expect(hasConfig).toBe(true);
    });

    it("should return false when config file does not exist", async () => {
      vi.mocked(resolveConfigPath).mockResolvedValueOnce({
        path: `${repoPath}/.nightgauge/config.yaml`,
        isLegacy: false,
        exists: false,
      });
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const repo = new Repository("test", repoPath);
      const hasConfig = await repo.hasIncrediConfig();

      expect(hasConfig).toBe(false);
    });
  });

  describe("getDisplayName()", () => {
    it("should return name only when no role", () => {
      const repo = new Repository("frontend", repoPath);
      expect(repo.getDisplayName()).toBe("frontend");
    });

    it("should return name with role when role is set", () => {
      const repo = new Repository("frontend", repoPath, "primary");
      expect(repo.getDisplayName()).toBe("frontend (primary)");
    });
  });

  describe("fromWorkspaceConfig()", () => {
    it("should create repository from workspace config", () => {
      const config = {
        name: "frontend",
        path: "./packages/frontend",
        role: "primary" as const,
      };

      const repo = Repository.fromWorkspaceConfig(config, "/workspace");

      expect(repo.name).toBe("frontend");
      expect(repo.path).toBe("/workspace/packages/frontend");
      expect(repo.role).toBe("primary");
    });

    it("should handle absolute paths", () => {
      const config = {
        name: "external",
        path: "/absolute/path/to/repo",
      };

      const repo = Repository.fromWorkspaceConfig(config, "/workspace");

      expect(repo.path).toBe("/absolute/path/to/repo");
    });
  });

  describe("github getter", () => {
    it("should return undefined before config is loaded", () => {
      const repo = new Repository("test", repoPath);
      expect(repo.github).toBeUndefined();
    });

    it("should return github config after loading", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
github:
  owner: test-org
  repo: test-repo
  project_number: 42
`);

      const repo = new Repository("test", repoPath);
      await repo.loadConfig();

      expect(repo.github).toEqual({
        owner: "test-org",
        repo: "test-repo",
        project_number: 42,
      });
    });

    it("should return github info for flat-config repos (top-level owner/repo)", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
owner: nightgauge
repo: nightgauge
project:
  number: 1
`);

      const repo = new Repository("test", repoPath);
      await repo.loadConfig();

      expect(repo.github).toEqual({
        owner: "nightgauge",
        repo: "nightgauge",
        project_number: 1,
      });
    });
  });
});

/**
 * Manifest hot-reload hardening (#704).
 *
 * reload() is driven by a file watcher, which changes what "failure" means.
 * A watcher fires on every save, so it observes the manifest mid-edit — and
 * before #704, initialize()'s catch block responded to that by setting
 * _mode = "single" and dropping every loaded repository. An operator typing a
 * quote into a valid manifest lost their whole workspace until a window reload.
 */
describe("reload() hardening (#704)", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    WorkspaceManager.resetInstance();
    vi.mocked(fs.access).mockResolvedValue(undefined);
  });

  afterEach(() => {
    WorkspaceManager.resetInstance();
  });

  const multiConfig = {
    workspace: { name: "W" },
    repositories: [
      { name: "alpha", path: "/test/alpha" },
      { name: "beta", path: "/test/beta" },
    ],
  };

  const okDetection = {
    type: "multi-workspace" as const,
    config: multiConfig,
    detection_method: "workspace-yaml",
  };

  it("retains the loaded repositories when the manifest is malformed mid-edit", async () => {
    vi.mocked(detectWorkspaceType).mockResolvedValue(okDetection);

    const manager = WorkspaceManager.getInstance(workspaceRoot);
    await manager.initialize();
    const before = manager.getAllRepositories().map((r) => r.name);
    expect(before.length).toBeGreaterThan(0);

    // Next detection throws, as it does for a half-written YAML document.
    vi.mocked(detectWorkspaceType).mockRejectedValueOnce(
      new Error("bad indentation of a mapping entry at line 7")
    );
    await manager.reload();

    expect(manager.getAllRepositories().map((r) => r.name)).toEqual(before);
    expect(manager.isMultiWorkspace()).toBe(true);
  });

  it("does not fire onWorkspaceChanged when a failed reload retained prior state", async () => {
    vi.mocked(detectWorkspaceType).mockResolvedValue(okDetection);

    const manager = WorkspaceManager.getInstance(workspaceRoot);
    await manager.initialize();

    let fired = 0;
    manager.onWorkspaceChanged(() => {
      fired++;
    });

    vi.mocked(detectWorkspaceType).mockRejectedValueOnce(new Error("parse error"));
    await manager.reload();

    // Subscribers already hold a good set; re-firing with unchanged content
    // only churns the tree.
    expect(fired).toBe(0);
  });

  it("recovers on the next good save after a malformed one", async () => {
    vi.mocked(detectWorkspaceType).mockResolvedValue(okDetection);
    const manager = WorkspaceManager.getInstance(workspaceRoot);
    await manager.initialize();

    vi.mocked(detectWorkspaceType).mockRejectedValueOnce(new Error("parse error"));
    await manager.reload();

    vi.mocked(detectWorkspaceType).mockResolvedValue({
      ...okDetection,
      config: {
        workspace: { name: "W" },
        repositories: [
          { name: "alpha", path: "/test/alpha" },
          { name: "beta", path: "/test/beta" },
          { name: "gamma", path: "/test/gamma" },
        ],
      },
    });
    await manager.reload();

    expect(manager.getAllRepositories().map((r) => r.name)).toContain("gamma");
  });

  it("still falls back to single-repo mode when the FIRST detection fails", async () => {
    // Nothing loaded yet means there is no good state to preserve, so the
    // pre-#704 fallback is still the right behavior here.
    vi.mocked(detectWorkspaceType).mockRejectedValue(new Error("no config anywhere"));

    const manager = WorkspaceManager.getInstance(workspaceRoot);
    await manager.initialize();

    expect(manager.isMultiWorkspace()).toBe(false);
  });

  it("coalesces concurrent reloads into one detection pass", async () => {
    // reload() clears _initialized before calling initialize(), whose only
    // guard was that same flag — so a debounced watcher and an explicit
    // command could both proceed and race two detections against one map.
    vi.mocked(detectWorkspaceType).mockResolvedValue(okDetection);
    const manager = WorkspaceManager.getInstance(workspaceRoot);
    await manager.initialize();

    vi.mocked(detectWorkspaceType).mockClear();

    let resolveDetection: ((v: typeof okDetection) => void) | undefined;
    vi.mocked(detectWorkspaceType).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveDetection = resolve;
        })
    );

    const first = manager.reload();
    const second = manager.reload();
    resolveDetection!(okDetection);
    await Promise.all([first, second]);

    expect(vi.mocked(detectWorkspaceType)).toHaveBeenCalledTimes(1);
  });
});
