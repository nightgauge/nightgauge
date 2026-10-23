/**
 * Unit tests for local config path resolution
 *
 * Tests the local config (.nightgauge/config.local.yaml) path resolution
 * functionality added in Issue #435.
 *
 * @see Issue #435 - Add local config override
 */

import * as fs from "fs/promises";
import * as path from "path";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  CONFIG_DIR,
  LOCAL_CONFIG_FILE_NAME,
  getConfigPaths,
  resolveLocalConfigPath,
  resolveLocalConfigPathSync,
  getRelativeLocalConfigPath,
  isLocalConfigPath,
} from "../../src/utils/configPathResolver";

// Mock fs/promises module
vi.mock("fs/promises");

describe("localConfigResolver", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.resetAllMocks();
  });

  describe("constants", () => {
    it("should export correct local config file name", () => {
      expect(LOCAL_CONFIG_FILE_NAME).toBe("config.local.yaml");
    });
  });

  describe("getConfigPaths", () => {
    it("should return local path alongside primary and legacy paths", () => {
      const paths = getConfigPaths(workspaceRoot);

      expect(paths.local).toBe(path.join(workspaceRoot, CONFIG_DIR, LOCAL_CONFIG_FILE_NAME));
      // Verify other paths are still present
      expect(paths.primary).toBeDefined();
      expect(paths.legacy).toBeDefined();
    });

    it("should return correct local path for different workspace roots", () => {
      const paths1 = getConfigPaths("/project/a");
      const paths2 = getConfigPaths("/project/b");

      expect(paths1.local).toBe("/project/a/.nightgauge/config.local.yaml");
      expect(paths2.local).toBe("/project/b/.nightgauge/config.local.yaml");
    });
  });

  describe("isLocalConfigPath", () => {
    it("should return true for local config path", () => {
      expect(isLocalConfigPath("/some/path/.nightgauge/config.local.yaml")).toBe(true);
    });

    it("should return false for primary config path", () => {
      expect(isLocalConfigPath("/some/path/.nightgauge/config.yaml")).toBe(false);
    });

    it("should return false for legacy config path", () => {
      expect(isLocalConfigPath("/some/path/.nightgauge/nightgauge.yaml")).toBe(false);
    });

    it("should return false for other paths", () => {
      expect(isLocalConfigPath("/some/path/other.yaml")).toBe(false);
    });
  });

  describe("getRelativeLocalConfigPath", () => {
    it("should return correct relative path", () => {
      expect(getRelativeLocalConfigPath()).toBe(".nightgauge/config.local.yaml");
    });
  });

  describe("resolveLocalConfigPath (async)", () => {
    it("should return path with exists=true when local config exists", async () => {
      const localPath = path.join(workspaceRoot, CONFIG_DIR, LOCAL_CONFIG_FILE_NAME);

      vi.mocked(fs.access).mockResolvedValue(undefined);

      const result = await resolveLocalConfigPath(workspaceRoot);

      expect(result.path).toBe(localPath);
      expect(result.exists).toBe(true);
    });

    it("should return path with exists=false when local config does not exist", async () => {
      const localPath = path.join(workspaceRoot, CONFIG_DIR, LOCAL_CONFIG_FILE_NAME);

      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await resolveLocalConfigPath(workspaceRoot);

      expect(result.path).toBe(localPath);
      expect(result.exists).toBe(false);
    });
  });

  describe("resolveLocalConfigPathSync", () => {
    it("should return path with exists=true when local config exists", () => {
      const fsSync = require("fs");
      vi.spyOn(fsSync, "existsSync").mockReturnValue(true);

      const result = resolveLocalConfigPathSync(workspaceRoot);

      expect(result.path).toBe(path.join(workspaceRoot, CONFIG_DIR, LOCAL_CONFIG_FILE_NAME));
      expect(result.exists).toBe(true);
    });

    it("should return path with exists=false when local config does not exist", () => {
      const fsSync = require("fs");
      vi.spyOn(fsSync, "existsSync").mockReturnValue(false);

      const result = resolveLocalConfigPathSync(workspaceRoot);

      expect(result.path).toBe(path.join(workspaceRoot, CONFIG_DIR, LOCAL_CONFIG_FILE_NAME));
      expect(result.exists).toBe(false);
    });
  });
});
