/**
 * Tests for BinaryResolver
 *
 * Verifies 4-tier binary resolution:
 * 1. VSCode setting (power user / development override)
 * 2. Environment variable (CI / testing override)
 * 3. Extension-bundled binary (production — primary path)
 * 4. System PATH (global install via brew, etc.)
 *
 * No vscode module mock needed — BinaryResolver is designed to be
 * tested without a live VSCode instance via its deps interface.
 *
 * @see BinaryResolver.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { BinaryResolver } from "../../src/services/BinaryResolver";
import type { BinaryResolverDeps } from "../../src/services/BinaryResolver";

function makeDeps(overrides: Partial<BinaryResolverDeps> = {}): BinaryResolverDeps {
  return {
    existsSync: vi.fn().mockReturnValue(false),
    getVSCodeSetting: vi.fn().mockReturnValue(""),
    getExtensionPath: vi.fn().mockReturnValue(undefined),
    platform: "darwin",
    arch: "arm64",
    env: {},
    which: vi.fn().mockResolvedValue(null),
    ...overrides,
  };
}

describe("BinaryResolver", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // ---------------------------------------------------------------------------
  // Tier 1: VSCode setting
  // ---------------------------------------------------------------------------

  describe("Tier 1: VSCode setting", () => {
    it("returns configured path when VSCode setting is set and file exists", async () => {
      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue("/custom/path/nightgauge"),
        existsSync: vi.fn().mockReturnValue(true),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe("/custom/path/nightgauge");
    });

    it("skips tier 1 when VSCode setting is empty string", async () => {
      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue(""),
        which: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe("/usr/local/bin/nightgauge");
      expect(deps.existsSync).not.toHaveBeenCalled();
    });

    it("skips tier 1 when VSCode setting is set but file does not exist", async () => {
      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue("/nonexistent/nightgauge"),
        existsSync: vi.fn().mockReturnValue(false),
        which: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      // Falls through to tier 4
      expect(result).toBe("/usr/local/bin/nightgauge");
    });
  });

  // ---------------------------------------------------------------------------
  // Tier 2: Environment variable
  // ---------------------------------------------------------------------------

  describe("Tier 2: Environment variable", () => {
    it("returns env var path when NIGHTGAUGE_GO_BINARY_PATH is set and file exists", async () => {
      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue(""),
        env: { NIGHTGAUGE_GO_BINARY_PATH: "/env/path/nightgauge" },
        existsSync: vi.fn().mockReturnValue(true),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe("/env/path/nightgauge");
    });

    it("skips tier 2 when env var is not set", async () => {
      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue(""),
        env: {},
        which: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe("/usr/local/bin/nightgauge");
    });

    it("skips tier 2 when env var is set but file does not exist", async () => {
      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue(""),
        env: { NIGHTGAUGE_GO_BINARY_PATH: "/nonexistent/nightgauge" },
        existsSync: vi.fn().mockReturnValue(false),
        which: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe("/usr/local/bin/nightgauge");
    });
  });

  // ---------------------------------------------------------------------------
  // Tier 3: Extension-bundled binary
  // ---------------------------------------------------------------------------

  describe("Tier 3: Extension-bundled binary", () => {
    it("returns bundled binary for darwin/arm64", async () => {
      const extPath = "/home/user/.vscode/extensions/nightgauge-vscode";
      const deps = makeDeps({
        getExtensionPath: vi.fn().mockReturnValue(extPath),
        platform: "darwin",
        arch: "arm64",
        existsSync: vi
          .fn()
          .mockImplementation((p: string) => p === `${extPath}/dist/bin/nightgauge-darwin-arm64`),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe(`${extPath}/dist/bin/nightgauge-darwin-arm64`);
    });

    it("returns bundled binary for linux/x64 mapping arch to amd64", async () => {
      const extPath = "/home/user/.vscode/extensions/nightgauge-vscode";
      const deps = makeDeps({
        getExtensionPath: vi.fn().mockReturnValue(extPath),
        platform: "linux",
        arch: "x64",
        existsSync: vi
          .fn()
          .mockImplementation((p: string) => p === `${extPath}/dist/bin/nightgauge-linux-amd64`),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe(`${extPath}/dist/bin/nightgauge-linux-amd64`);
    });

    it("returns bundled binary for win32/x64 with .exe suffix", async () => {
      const extPath = "C:\\Users\\user\\.vscode\\extensions\\nightgauge-vscode";
      const deps = makeDeps({
        getExtensionPath: vi.fn().mockReturnValue(extPath),
        platform: "win32",
        arch: "x64",
        existsSync: vi
          .fn()
          .mockImplementation(
            (p: string) =>
              p === `${extPath}\\dist\\bin\\nightgauge-win-amd64.exe` ||
              p.endsWith("nightgauge-win-amd64.exe")
          ),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toMatch(/nightgauge-win-amd64\.exe$/);
    });

    it("falls back to plain binary when platform-specific binary not found", async () => {
      const extPath = "/home/user/.vscode/extensions/nightgauge-vscode";
      const deps = makeDeps({
        getExtensionPath: vi.fn().mockReturnValue(extPath),
        platform: "darwin",
        arch: "arm64",
        existsSync: vi
          .fn()
          .mockImplementation((p: string) => p === `${extPath}/dist/bin/nightgauge`),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe(`${extPath}/dist/bin/nightgauge`);
    });

    it("skips tier 3 when no extensionPath is available", async () => {
      const deps = makeDeps({
        getExtensionPath: vi.fn().mockReturnValue(undefined),
        which: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe("/usr/local/bin/nightgauge");
    });
  });

  // ---------------------------------------------------------------------------
  // Tier 4: System PATH via which()
  // ---------------------------------------------------------------------------

  describe("Tier 4: System PATH", () => {
    it("falls through to which() and returns its result", async () => {
      const deps = makeDeps({
        which: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe("/usr/local/bin/nightgauge");
      expect(deps.which).toHaveBeenCalledWith("nightgauge");
    });

    it("returns null when which() returns null", async () => {
      const deps = makeDeps({
        which: vi.fn().mockResolvedValue(null),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBeNull();
    });
  });

  // ---------------------------------------------------------------------------
  // Priority ordering
  // ---------------------------------------------------------------------------

  describe("Priority ordering", () => {
    it("tier 1 takes precedence over tier 2 and tier 3", async () => {
      const extPath = "/home/user/.vscode/extensions/nightgauge-vscode";
      const setting = "/custom/path/nightgauge";
      const envPath = "/env/path/nightgauge";

      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue(setting),
        env: { NIGHTGAUGE_GO_BINARY_PATH: envPath },
        getExtensionPath: vi.fn().mockReturnValue(extPath),
        platform: "darwin",
        arch: "arm64",
        existsSync: vi.fn().mockReturnValue(true),
        which: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe(setting);
      expect(deps.which).not.toHaveBeenCalled();
    });

    it("tier 2 takes precedence over tier 3 when tier 1 is absent", async () => {
      const extPath = "/home/user/.vscode/extensions/nightgauge-vscode";
      const envPath = "/env/path/nightgauge";

      const deps = makeDeps({
        getVSCodeSetting: vi.fn().mockReturnValue(""),
        env: { NIGHTGAUGE_GO_BINARY_PATH: envPath },
        getExtensionPath: vi.fn().mockReturnValue(extPath),
        platform: "darwin",
        arch: "arm64",
        existsSync: vi.fn().mockReturnValue(true),
      });
      const resolver = new BinaryResolver(deps);

      const result = await resolver.resolve();

      expect(result).toBe(envPath);
    });
  });

  // ---------------------------------------------------------------------------
  // resolveSync() — synchronous filesystem tiers (1-3) used by skillRunner to
  // export NIGHTGAUGE_BIN into skill subprocess env without an async hop
  // (#4029).
  // ---------------------------------------------------------------------------

  describe("resolveSync (#4029)", () => {
    it("resolves the extension-bundled binary synchronously (tier 3)", () => {
      const extPath = "/home/user/.vscode/extensions/nightgauge-vscode";
      const deps = makeDeps({
        getExtensionPath: vi.fn().mockReturnValue(extPath),
        platform: "darwin",
        arch: "arm64",
        existsSync: vi
          .fn()
          .mockImplementation((p: string) => p === `${extPath}/dist/bin/nightgauge-darwin-arm64`),
      });

      expect(new BinaryResolver(deps).resolveSync()).toBe(
        `${extPath}/dist/bin/nightgauge-darwin-arm64`
      );
    });

    it("honors the VSCode setting and env-var tiers", () => {
      expect(
        new BinaryResolver(
          makeDeps({
            getVSCodeSetting: vi.fn().mockReturnValue("/custom/nightgauge"),
            existsSync: vi.fn().mockReturnValue(true),
          })
        ).resolveSync()
      ).toBe("/custom/nightgauge");

      expect(
        new BinaryResolver(
          makeDeps({
            env: { NIGHTGAUGE_GO_BINARY_PATH: "/env/nightgauge" },
            existsSync: vi.fn().mockReturnValue(true),
          })
        ).resolveSync()
      ).toBe("/env/nightgauge");
    });

    it("returns null (NOT a PATH lookup) when no filesystem tier matches", () => {
      const which = vi.fn().mockResolvedValue("/usr/local/bin/nightgauge");
      const deps = makeDeps({ which });

      // resolveSync intentionally omits tier 4 — the skill's own `command -v`
      // cascade covers the on-PATH case, so no env injection is needed there.
      expect(new BinaryResolver(deps).resolveSync()).toBeNull();
      expect(which).not.toHaveBeenCalled();
    });
  });
});
