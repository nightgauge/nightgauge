/**
 * workspaceDetection.test.ts
 *
 * Unit tests for workspace detection and configuration loading.
 *
 * @see Issue #323 - Workspace configuration schema and detection
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs/promises";
import * as vscode from "vscode";
import {
  detectWorkspaceType,
  loadWorkspaceConfig,
  validateWorkspaceConfig,
  isMultiWorkspace,
} from "../../src/utils/workspaceDetection";

// Mock dependencies
vi.mock("fs/promises");

describe("workspaceDetection", () => {
  const workspaceRoot = "/test/workspace";
  const configPath = "/test/workspace/.vscode/nightgauge-workspace.yaml";

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("detectWorkspaceType()", () => {
    it("should detect explicit workspace config when file exists", async () => {
      // Mock file exists with valid config
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
workspace:
  name: "Test Workspace"
repositories:
  - name: frontend
    path: ./packages/frontend
`);

      const result = await detectWorkspaceType(workspaceRoot);

      expect(result.type).toBe("multi-workspace");
      expect(result.detection_method).toBe("explicit");
      expect(result.config).not.toBeNull();
      expect(result.config?.workspace.name).toBe("Test Workspace");
    });

    it("should auto-detect multi-workspace from multiple folders", async () => {
      // Mock no config file
      vi.mocked(fs.access).mockRejectedValueOnce(new Error("ENOENT: file not found"));

      // Mock multiple workspace folders with nightgauge config
      const mockWorkspaceFolders = [
        { uri: { fsPath: "/test/repo1" } },
        { uri: { fsPath: "/test/repo2" } },
      ];
      vi.mocked(vscode.workspace).workspaceFolders = mockWorkspaceFolders as any;

      // Both folders have .nightgauge/nightgauge.yaml
      vi.mocked(fs.access)
        .mockResolvedValueOnce(undefined) // repo1 .nightgauge/nightgauge.yaml exists
        .mockResolvedValueOnce(undefined); // repo2 .nightgauge/nightgauge.yaml exists

      const result = await detectWorkspaceType(workspaceRoot);

      expect(result.type).toBe("multi-workspace");
      expect(result.detection_method).toBe("auto-detected");
      expect(result.config).toBeNull(); // No explicit config
    });

    it("should return single-repo for single workspace folder", async () => {
      // Mock no config file
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT: file not found"));

      // Mock single workspace folder
      const mockWorkspaceFolders = [{ uri: { fsPath: workspaceRoot } }];
      vi.mocked(vscode.workspace).workspaceFolders = mockWorkspaceFolders as any;

      const result = await detectWorkspaceType(workspaceRoot);

      expect(result.type).toBe("single");
      expect(result.detection_method).toBe("single-repo");
      expect(result.config).toBeNull();
    });

    it("should return single-repo when no workspace folders", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT: file not found"));
      vi.mocked(vscode.workspace).workspaceFolders = undefined;

      const result = await detectWorkspaceType(workspaceRoot);

      expect(result.type).toBe("single");
      expect(result.detection_method).toBe("single-repo");
    });

    it("should return single-repo when only one folder has nightgauge config", async () => {
      // Mock no workspace config file
      vi.mocked(fs.access).mockRejectedValueOnce(new Error("ENOENT: file not found"));

      const mockWorkspaceFolders = [
        { uri: { fsPath: "/test/repo1" } },
        { uri: { fsPath: "/test/repo2" } },
      ];
      vi.mocked(vscode.workspace).workspaceFolders = mockWorkspaceFolders as any;

      // Mock file checks for auto-detect:
      // Now checks both config.yaml and nightgauge.yaml for each repo
      // For each repo: first checks primary (config.yaml), then legacy (nightgauge.yaml)
      vi.mocked(fs.access)
        .mockResolvedValueOnce(undefined) // repo1: primary exists
        // repo2: primary doesn't exist
        .mockRejectedValueOnce(new Error("ENOENT"))
        // repo2: legacy doesn't exist either
        .mockRejectedValueOnce(new Error("ENOENT"));

      const result = await detectWorkspaceType(workspaceRoot);

      expect(result.type).toBe("single");
      expect(result.detection_method).toBe("single-repo");
    });

    it("should handle file system errors gracefully", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("EACCES: permission denied"));
      vi.mocked(vscode.workspace).workspaceFolders = undefined;

      const result = await detectWorkspaceType(workspaceRoot);

      // Should fallback to single-repo on errors
      expect(result.type).toBe("single");
      expect(result.detection_method).toBe("single-repo");
    });
  });

  describe("loadWorkspaceConfig()", () => {
    it("should load and parse valid workspace config", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
workspace:
  name: "My Workspace"
  description: "Test workspace"
repositories:
  - name: frontend
    path: ./packages/frontend
    role: primary
  - name: backend
    path: ./packages/backend
    role: primary
routing:
  patterns:
    "area:frontend": frontend
    "area:backend": backend
  default_repository: frontend
epic:
  cross_repo_tracking: true
  shared_milestones: false
`);

      const config = await loadWorkspaceConfig(workspaceRoot);

      expect(config).not.toBeNull();
      expect(config?.workspace.name).toBe("My Workspace");
      expect(config?.workspace.description).toBe("Test workspace");
      expect(config?.repositories).toHaveLength(2);
      expect(config?.repositories[0].role).toBe("primary");
      expect(config?.routing?.default_repository).toBe("frontend");
      expect(config?.epic?.cross_repo_tracking).toBe(true);
    });

    it("should return null when config file does not exist", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT: file not found"));

      const config = await loadWorkspaceConfig(workspaceRoot);

      expect(config).toBeNull();
    });

    it("should throw on invalid YAML syntax", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
workspace:
  name: "Unclosed quote
repositories:
  - invalid yaml [[[
`);

      await expect(loadWorkspaceConfig(workspaceRoot)).rejects.toThrow(/Failed to parse/);
    });

    it("should throw on validation failure", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
workspace:
  name: ""
repositories: []
`);

      await expect(loadWorkspaceConfig(workspaceRoot)).rejects.toThrow(
        /Invalid workspace configuration/
      );
    });

    it("should handle file read permission errors", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockRejectedValue(new Error("EACCES: permission denied"));

      await expect(loadWorkspaceConfig(workspaceRoot)).rejects.toThrow(/permission denied/);
    });
  });

  describe("validateWorkspaceConfig()", () => {
    it("should validate complete valid config", () => {
      const config = {
        workspace: {
          name: "Valid Workspace",
          description: "Description",
        },
        repositories: [
          { name: "repo1", path: "./repo1", role: "primary" },
          { name: "repo2", path: "./repo2", role: "secondary" },
        ],
        routing: {
          patterns: { "area:frontend": "repo1" },
          default_repository: "repo1",
        },
        epic: {
          cross_repo_tracking: true,
          shared_milestones: false,
        },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(true);
      expect(result.errors).toHaveLength(0);
    });

    it("should validate minimal valid config (required fields only)", () => {
      const config = {
        workspace: {
          name: "Minimal Workspace",
        },
        repositories: [{ name: "repo1", path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(true);
      expect(result.errors).toHaveLength(0);
    });

    it("should reject non-object config", () => {
      const result = validateWorkspaceConfig("not an object");

      expect(result.valid).toBe(false);
      expect(result.errors).toHaveLength(1);
      expect(result.errors[0].path).toBe("$");
    });

    it("should reject missing workspace section", () => {
      const config = {
        repositories: [{ name: "repo1", path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "workspace")).toBe(true);
    });

    it("should reject missing workspace.name", () => {
      const config = {
        workspace: {},
        repositories: [{ name: "repo1", path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "workspace.name")).toBe(true);
    });

    it("should reject empty workspace.name", () => {
      const config = {
        workspace: { name: "" },
        repositories: [{ name: "repo1", path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "workspace.name")).toBe(true);
    });

    it("should reject non-string workspace.name", () => {
      const config = {
        workspace: { name: 123 },
        repositories: [{ name: "repo1", path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "workspace.name")).toBe(true);
    });

    it("should reject non-string workspace.description", () => {
      const config = {
        workspace: { name: "Valid", description: 123 },
        repositories: [{ name: "repo1", path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "workspace.description")).toBe(true);
    });

    it("should reject missing repositories", () => {
      const config = {
        workspace: { name: "Valid" },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "repositories")).toBe(true);
    });

    it("should reject non-array repositories", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: "not an array",
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "repositories")).toBe(true);
    });

    it("should reject empty repositories array", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "repositories")).toBe(true);
    });

    it("should reject repository missing name", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "repositories[0].name")).toBe(true);
    });

    it("should reject repository missing path", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "repositories[0].path")).toBe(true);
    });

    it("should reject duplicate repository names", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [
          { name: "repo1", path: "./repo1" },
          { name: "repo1", path: "./repo2" }, // Duplicate name
        ],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.message.includes("Duplicate"))).toBe(true);
    });

    it("should reject invalid repository role", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1", role: "invalid" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "repositories[0].role")).toBe(true);
    });

    it("should accept valid repository roles", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [
          { name: "repo1", path: "./repo1", role: "primary" },
          { name: "repo2", path: "./repo2", role: "secondary" },
          { name: "repo3", path: "./repo3", role: "shared" },
        ],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(true);
    });

    it("should reject non-object routing", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        routing: "not an object",
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "routing")).toBe(true);
    });

    it("should reject non-object routing.patterns", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        routing: { patterns: "not an object" },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "routing.patterns")).toBe(true);
    });

    it("should reject non-string routing.default_repository", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        routing: { default_repository: 123 },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "routing.default_repository")).toBe(true);
    });

    it("should reject non-object epic", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        epic: "not an object",
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "epic")).toBe(true);
    });

    it("should reject non-boolean epic.cross_repo_tracking", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        epic: { cross_repo_tracking: "yes" },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "epic.cross_repo_tracking")).toBe(true);
    });

    it("should reject non-boolean epic.shared_milestones", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        epic: { shared_milestones: 1 },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "epic.shared_milestones")).toBe(true);
    });

    it("should accept valid knowledge section", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        knowledge: {
          workspace_root: ".nightgauge/knowledge/",
          aggregate: true,
          cross_repo_links: false,
        },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(true);
      expect(result.errors).toHaveLength(0);
    });

    it("should accept config without knowledge section (backwards compatible)", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(true);
    });

    it("should reject non-object knowledge", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        knowledge: "not an object",
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "knowledge")).toBe(true);
    });

    it("should reject non-string knowledge.workspace_root", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        knowledge: { workspace_root: 123 },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "knowledge.workspace_root")).toBe(true);
    });

    it("should reject non-boolean knowledge.aggregate", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        knowledge: { aggregate: "yes" },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "knowledge.aggregate")).toBe(true);
    });

    it("should reject non-boolean knowledge.cross_repo_links", () => {
      const config = {
        workspace: { name: "Valid" },
        repositories: [{ name: "repo1", path: "./repo1" }],
        knowledge: { cross_repo_links: 1 },
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.some((e) => e.path === "knowledge.cross_repo_links")).toBe(true);
    });

    it("should accumulate multiple validation errors", () => {
      const config = {
        workspace: { name: "", description: 123 }, // 2 errors
        repositories: [], // 1 error
      };

      const result = validateWorkspaceConfig(config);

      expect(result.valid).toBe(false);
      expect(result.errors.length).toBeGreaterThanOrEqual(3);
    });
  });

  describe("isMultiWorkspace()", () => {
    it("should return true for explicit workspace config", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(`
workspace:
  name: "Test"
repositories:
  - name: repo1
    path: ./repo1
`);

      const result = await isMultiWorkspace(workspaceRoot);

      expect(result).toBe(true);
    });

    it("should return true for auto-detected workspace", async () => {
      vi.mocked(fs.access).mockRejectedValueOnce(new Error("ENOENT: file not found"));

      const mockWorkspaceFolders = [
        { uri: { fsPath: "/test/repo1" } },
        { uri: { fsPath: "/test/repo2" } },
      ];
      vi.mocked(vscode.workspace).workspaceFolders = mockWorkspaceFolders as any;

      vi.mocked(fs.access).mockResolvedValueOnce(undefined).mockResolvedValueOnce(undefined);

      const result = await isMultiWorkspace(workspaceRoot);

      expect(result).toBe(true);
    });

    it("should return false for single repository", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT: file not found"));
      vi.mocked(vscode.workspace).workspaceFolders = [{ uri: { fsPath: workspaceRoot } }] as any;

      const result = await isMultiWorkspace(workspaceRoot);

      expect(result).toBe(false);
    });

    it("should return false on detection errors (safe default)", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("EACCES: permission denied"));
      vi.mocked(vscode.workspace).workspaceFolders = undefined;

      const result = await isMultiWorkspace(workspaceRoot);

      expect(result).toBe(false);
    });
  });
});
