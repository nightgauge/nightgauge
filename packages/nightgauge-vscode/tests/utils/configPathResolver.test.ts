/**
 * Unit tests for configPathResolver utility
 *
 * @see Issue #433 - Rename config file from nightgauge.yaml to config.yaml
 */

import * as fs from "fs/promises";
import * as path from "path";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  CONFIG_FILE_NAME,
  LEGACY_CONFIG_FILE_NAME,
  CONFIG_DIR,
  getConfigPaths,
  resolveConfigPath,
  needsMigration,
  isLegacyConfigPath,
  getRelativeConfigPath,
} from "../../src/utils/configPathResolver";

// Mock fs/promises module
vi.mock("fs/promises");

describe("configPathResolver", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.resetAllMocks();
  });

  describe("constants", () => {
    it("should export correct config file name", () => {
      expect(CONFIG_FILE_NAME).toBe("config.yaml");
    });

    it("should export correct legacy config file name", () => {
      expect(LEGACY_CONFIG_FILE_NAME).toBe("nightgauge.yaml");
    });

    it("should export correct config directory", () => {
      expect(CONFIG_DIR).toBe(".nightgauge");
    });
  });

  describe("getConfigPaths", () => {
    it("should return correct primary and legacy paths", () => {
      const paths = getConfigPaths(workspaceRoot);

      expect(paths.primary).toBe(path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME));
      expect(paths.legacy).toBe(path.join(workspaceRoot, CONFIG_DIR, LEGACY_CONFIG_FILE_NAME));
    });
  });

  describe("isLegacyConfigPath", () => {
    it("should return true for legacy config path", () => {
      expect(isLegacyConfigPath("/some/path/.nightgauge/nightgauge.yaml")).toBe(true);
    });

    it("should return false for primary config path", () => {
      expect(isLegacyConfigPath("/some/path/.nightgauge/config.yaml")).toBe(false);
    });

    it("should return false for other paths", () => {
      expect(isLegacyConfigPath("/some/path/other.yaml")).toBe(false);
    });
  });

  describe("getRelativeConfigPath", () => {
    it("should return primary relative path when isLegacy is false", () => {
      expect(getRelativeConfigPath(false)).toBe(".nightgauge/config.yaml");
    });

    it("should return legacy relative path when isLegacy is true", () => {
      expect(getRelativeConfigPath(true)).toBe(".nightgauge/nightgauge.yaml");
    });
  });

  describe("resolveConfigPath (async)", () => {
    it("should return primary path when primary config exists", async () => {
      const primaryPath = path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME);

      vi.mocked(fs.access).mockImplementation(async (filePath) => {
        if (filePath === primaryPath) {
          return Promise.resolve();
        }
        throw new Error("ENOENT");
      });

      const result = await resolveConfigPath(workspaceRoot);

      expect(result.path).toBe(primaryPath);
      expect(result.exists).toBe(true);
      expect(result.isLegacy).toBe(false);
    });

    it("should return legacy path when only legacy config exists", async () => {
      const primaryPath = path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME);
      const legacyPath = path.join(workspaceRoot, CONFIG_DIR, LEGACY_CONFIG_FILE_NAME);

      vi.mocked(fs.access).mockImplementation(async (filePath) => {
        if (filePath === legacyPath) {
          return Promise.resolve();
        }
        throw new Error("ENOENT");
      });

      const result = await resolveConfigPath(workspaceRoot);

      expect(result.path).toBe(legacyPath);
      expect(result.exists).toBe(true);
      expect(result.isLegacy).toBe(true);
    });

    it("should return primary path with exists=false when neither exists", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await resolveConfigPath(workspaceRoot);

      expect(result.path).toBe(path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME));
      expect(result.exists).toBe(false);
      expect(result.isLegacy).toBe(false);
    });

    it("should prefer primary over legacy when both exist", async () => {
      const primaryPath = path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME);

      // Both exist - mock returns successfully for all paths
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const result = await resolveConfigPath(workspaceRoot);

      expect(result.path).toBe(primaryPath);
      expect(result.exists).toBe(true);
      expect(result.isLegacy).toBe(false);
    });
  });

  describe("needsMigration", () => {
    it("should return true when legacy exists but primary does not", async () => {
      const primaryPath = path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME);
      const legacyPath = path.join(workspaceRoot, CONFIG_DIR, LEGACY_CONFIG_FILE_NAME);

      vi.mocked(fs.access).mockImplementation(async (filePath) => {
        if (filePath === legacyPath) {
          return Promise.resolve();
        }
        throw new Error("ENOENT");
      });

      const result = await needsMigration(workspaceRoot);

      expect(result).toBe(true);
    });

    it("should return false when primary exists", async () => {
      const primaryPath = path.join(workspaceRoot, CONFIG_DIR, CONFIG_FILE_NAME);

      vi.mocked(fs.access).mockImplementation(async (filePath) => {
        if (filePath === primaryPath) {
          return Promise.resolve();
        }
        throw new Error("ENOENT");
      });

      const result = await needsMigration(workspaceRoot);

      // needsMigration checks legacy first, if legacy doesn't exist, returns false
      expect(result).toBe(false);
    });

    it("should return false when both exist (primary takes precedence)", async () => {
      // Both exist
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const result = await needsMigration(workspaceRoot);

      expect(result).toBe(false);
    });

    it("should return false when neither exists", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await needsMigration(workspaceRoot);

      expect(result).toBe(false);
    });
  });
});
