/**
 * Unit tests for globalConfigResolver utility
 *
 * @see Issue #434 - Add Global Config Layer
 */

import * as fs from "fs/promises";
import * as path from "path";
import * as os from "os";
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  GLOBAL_CONFIG_FILE_NAME,
  NIGHTGAUGE_DIR_NAME,
  NIGHTGAUGE_LEGACY_DIR_NAME,
  getGlobalConfigDir,
  getGlobalConfigPath,
  resolveGlobalConfigPath,
  resolveGlobalConfigPathSync,
  globalConfigExists,
  globalConfigExistsSync,
  describeGlobalConfigLocation,
} from "../../src/utils/globalConfigResolver";

// Mock fs/promises module
vi.mock("fs/promises");

// Mock os module
vi.mock("os", async () => {
  const actual = await vi.importActual("os");
  return {
    ...actual,
    homedir: vi.fn(),
    platform: vi.fn(),
  };
});

describe("globalConfigResolver", () => {
  const mockHomeDir = "/Users/testuser";

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(os.homedir).mockReturnValue(mockHomeDir);
  });

  afterEach(() => {
    vi.resetAllMocks();
  });

  describe("constants", () => {
    it("should export correct config file name", () => {
      expect(GLOBAL_CONFIG_FILE_NAME).toBe("config.yaml");
    });

    it("should export correct nightgauge directory name", () => {
      expect(NIGHTGAUGE_DIR_NAME).toBe("nightgauge");
    });

    it("should export correct legacy directory name", () => {
      expect(NIGHTGAUGE_LEGACY_DIR_NAME).toBe(".nightgauge");
    });
  });

  describe("getGlobalConfigDir", () => {
    describe("with NIGHTGAUGE_CONFIG_HOME env var", () => {
      it("should return NIGHTGAUGE_CONFIG_HOME when set", () => {
        const env = { NIGHTGAUGE_CONFIG_HOME: "/custom/nightgauge" };

        const result = getGlobalConfigDir(env, "darwin");

        expect(result.dir).toBe("/custom/nightgauge");
        expect(result.source).toBe("env_nightgauge_config_home");
      });

      it("should prioritize NIGHTGAUGE_CONFIG_HOME over XDG_CONFIG_HOME", () => {
        const env = {
          NIGHTGAUGE_CONFIG_HOME: "/custom/nightgauge",
          XDG_CONFIG_HOME: "/custom/xdg",
        };

        const result = getGlobalConfigDir(env, "linux");

        expect(result.dir).toBe("/custom/nightgauge");
        expect(result.source).toBe("env_nightgauge_config_home");
      });
    });

    describe("with XDG_CONFIG_HOME env var", () => {
      it("should return XDG_CONFIG_HOME/nightgauge when set", () => {
        const env = { XDG_CONFIG_HOME: "/custom/xdg" };

        const result = getGlobalConfigDir(env, "linux");

        expect(result.dir).toBe("/custom/xdg/nightgauge");
        expect(result.source).toBe("env_xdg_config_home");
      });

      it("should work on macOS with XDG_CONFIG_HOME", () => {
        const env = { XDG_CONFIG_HOME: "/custom/xdg" };

        const result = getGlobalConfigDir(env, "darwin");

        expect(result.dir).toBe("/custom/xdg/nightgauge");
        expect(result.source).toBe("env_xdg_config_home");
      });
    });

    describe("platform-specific defaults", () => {
      it("should return ~/.nightgauge on macOS", () => {
        const result = getGlobalConfigDir({}, "darwin");

        expect(result.dir).toBe(path.join(mockHomeDir, ".nightgauge"));
        expect(result.source).toBe("platform_default");
      });

      it("should return ~/.config/nightgauge on Linux", () => {
        const result = getGlobalConfigDir({}, "linux");

        expect(result.dir).toBe(path.join(mockHomeDir, ".config", "nightgauge"));
        expect(result.source).toBe("platform_default");
      });

      it("should return APPDATA/nightgauge on Windows with APPDATA set", () => {
        const env = { APPDATA: "C:\\Users\\testuser\\AppData\\Roaming" };

        const result = getGlobalConfigDir(env, "win32");

        // path.join normalizes separators based on the current platform,
        // so on macOS/Linux it uses forward slashes even for Windows paths
        expect(result.dir).toBe(path.join("C:\\Users\\testuser\\AppData\\Roaming", "nightgauge"));
        expect(result.source).toBe("platform_default");
      });

      it("should fallback to Home/AppData/Roaming on Windows without APPDATA", () => {
        const result = getGlobalConfigDir({}, "win32");

        expect(result.dir).toBe(path.join(mockHomeDir, "AppData", "Roaming", "nightgauge"));
        expect(result.source).toBe("platform_default");
      });

      it("should fallback to ~/.nightgauge on unknown platform", () => {
        const result = getGlobalConfigDir({}, "freebsd" as NodeJS.Platform);

        expect(result.dir).toBe(path.join(mockHomeDir, ".nightgauge"));
        expect(result.source).toBe("platform_default");
      });
    });
  });

  describe("getGlobalConfigPath", () => {
    it("should return full path to config.yaml", () => {
      const result = getGlobalConfigPath({}, "darwin");

      expect(result).toBe(path.join(mockHomeDir, ".nightgauge", "config.yaml"));
    });

    it("should respect NIGHTGAUGE_CONFIG_HOME", () => {
      const env = { NIGHTGAUGE_CONFIG_HOME: "/custom/nightgauge" };

      const result = getGlobalConfigPath(env, "darwin");

      expect(result).toBe("/custom/nightgauge/config.yaml");
    });
  });

  describe("resolveGlobalConfigPath (async)", () => {
    it("should return exists=true when config file exists", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const result = await resolveGlobalConfigPath({}, "darwin");

      expect(result.exists).toBe(true);
      expect(result.path).toBe(path.join(mockHomeDir, ".nightgauge", "config.yaml"));
      expect(result.source).toBe("platform_default");
      expect(result.configDir).toBe(path.join(mockHomeDir, ".nightgauge"));
    });

    it("should return exists=false when config file does not exist", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await resolveGlobalConfigPath({}, "darwin");

      expect(result.exists).toBe(false);
      expect(result.path).toBe(path.join(mockHomeDir, ".nightgauge", "config.yaml"));
    });

    it("should use NIGHTGAUGE_CONFIG_HOME when set", async () => {
      const env = { NIGHTGAUGE_CONFIG_HOME: "/custom/nightgauge" };
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const result = await resolveGlobalConfigPath(env, "darwin");

      expect(result.path).toBe("/custom/nightgauge/config.yaml");
      expect(result.source).toBe("env_nightgauge_config_home");
      expect(result.configDir).toBe("/custom/nightgauge");
    });
  });

  describe("resolveGlobalConfigPathSync", () => {
    it("should return exists=true when config file exists", () => {
      const fsSync = require("fs");
      vi.spyOn(fsSync, "existsSync").mockReturnValue(true);

      const result = resolveGlobalConfigPathSync({}, "darwin");

      expect(result.exists).toBe(true);
      expect(result.path).toBe(path.join(mockHomeDir, ".nightgauge", "config.yaml"));
    });

    it("should return exists=false when config file does not exist", () => {
      const fsSync = require("fs");
      vi.spyOn(fsSync, "existsSync").mockReturnValue(false);

      const result = resolveGlobalConfigPathSync({}, "darwin");

      expect(result.exists).toBe(false);
    });
  });

  describe("globalConfigExists (async)", () => {
    it("should return true when config exists", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const result = await globalConfigExists({}, "darwin");

      expect(result).toBe(true);
    });

    it("should return false when config does not exist", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await globalConfigExists({}, "darwin");

      expect(result).toBe(false);
    });
  });

  describe("globalConfigExistsSync", () => {
    it("should return true when config exists", () => {
      const fsSync = require("fs");
      vi.spyOn(fsSync, "existsSync").mockReturnValue(true);

      const result = globalConfigExistsSync({}, "darwin");

      expect(result).toBe(true);
    });

    it("should return false when config does not exist", () => {
      const fsSync = require("fs");
      vi.spyOn(fsSync, "existsSync").mockReturnValue(false);

      const result = globalConfigExistsSync({}, "darwin");

      expect(result).toBe(false);
    });
  });

  describe("describeGlobalConfigLocation", () => {
    it("should describe NIGHTGAUGE_CONFIG_HOME source", () => {
      const result = describeGlobalConfigLocation(
        "env_nightgauge_config_home",
        "/custom/nightgauge/config.yaml"
      );

      expect(result).toBe("$NIGHTGAUGE_CONFIG_HOME (/custom/nightgauge/config.yaml)");
    });

    it("should describe XDG_CONFIG_HOME source", () => {
      const result = describeGlobalConfigLocation(
        "env_xdg_config_home",
        "/custom/xdg/nightgauge/config.yaml"
      );

      expect(result).toBe("$XDG_CONFIG_HOME/nightgauge (/custom/xdg/nightgauge/config.yaml)");
    });

    it("should describe platform default source", () => {
      const configPath = "/Users/testuser/.nightgauge/config.yaml";
      const result = describeGlobalConfigLocation("platform_default", configPath);

      expect(result).toBe(configPath);
    });
  });
});
