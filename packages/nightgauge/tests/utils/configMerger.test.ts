import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [
      {
        uri: {
          fsPath: "/test/workspace",
        },
      },
    ],
  },
}));

vi.mock("node:fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

import * as fs from "node:fs";
import { ConfigMerger, type ConfigSource } from "../../src/utils/configMerger";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";

describe("ConfigMerger", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("merge", () => {
    it("returns env when all sources present", () => {
      const sources: ConfigSource = {
        env: "from-env",
        config: "from-config",
        default: "from-default",
      };
      expect(ConfigMerger.merge(sources)).toBe("from-env");
    });

    it("returns config when env is null", () => {
      const sources: ConfigSource = {
        env: null,
        config: "from-config",
        default: "from-default",
      };
      expect(ConfigMerger.merge(sources)).toBe("from-config");
    });

    it("returns config when env is undefined", () => {
      const sources: ConfigSource = {
        config: "from-config",
        default: "from-default",
      };
      expect(ConfigMerger.merge(sources)).toBe("from-config");
    });

    it("returns default when env and config are null", () => {
      const sources: ConfigSource = {
        env: null,
        config: null,
        default: "from-default",
      };
      expect(ConfigMerger.merge(sources)).toBe("from-default");
    });

    it("returns default when only default provided", () => {
      const sources: ConfigSource = { default: "from-default" };
      expect(ConfigMerger.merge(sources)).toBe("from-default");
    });

    it("works with non-string types", () => {
      const sources: ConfigSource<number> = {
        env: null,
        config: 42,
        default: 0,
      };
      expect(ConfigMerger.merge(sources)).toBe(42);
    });

    it("treats false as a valid value (not null)", () => {
      const sources: ConfigSource<boolean> = {
        env: false,
        config: true,
        default: true,
      };
      expect(ConfigMerger.merge(sources)).toBe(false);
    });
  });

  describe("readConfigLines", () => {
    it("returns lines when config exists", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockReturnValue("line1\nline2\nline3");

      const lines = ConfigMerger.readConfigLines("/test/workspace");

      expect(lines).toEqual(["line1", "line2", "line3"]);
    });

    it("returns null when config does not exist", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: false,
        isLegacy: false,
      });

      const lines = ConfigMerger.readConfigLines("/test/workspace");

      expect(lines).toBeNull();
    });

    it("returns null when no workspace root", () => {
      const lines = ConfigMerger.readConfigLines(undefined);

      // Uses auto-detected workspace root from vscode mock
      expect(resolveConfigPathSync).toHaveBeenCalledWith("/test/workspace");
    });

    it("degrades to an empty merge on file read error (never throws)", () => {
      vi.mocked(resolveConfigPathSync).mockReturnValue({
        path: "/test/workspace/.nightgauge/config.yaml",
        exists: true,
        isLegacy: false,
      });

      vi.mocked(fs.readFileSync).mockImplementation(() => {
        throw new Error("Read error");
      });

      // The tier-merged reader treats an unreadable project file as an empty
      // tier rather than a fatal error — other tiers (machine, local) may
      // still be readable. With every tier empty the effective text is "".
      const lines = ConfigMerger.readConfigLines("/test/workspace");

      expect(lines).toEqual([""]);
    });
  });

  describe("resolveRoot", () => {
    it("returns provided root when given", () => {
      expect(ConfigMerger.resolveRoot("/custom/path")).toBe("/custom/path");
    });

    it("auto-detects from vscode workspace when not provided", () => {
      expect(ConfigMerger.resolveRoot()).toBe("/test/workspace");
    });
  });
});
