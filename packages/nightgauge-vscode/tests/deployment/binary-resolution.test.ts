/**
 * BinaryResolver — Real-filesystem deployment tests
 *
 * These tests exercise BinaryResolver with real filesystem operations.
 * No mocking of existsSync — tests use actual temp directories to verify
 * that the resolution tiers work correctly against the real filesystem.
 *
 * All tests complete without requiring the actual Go binary to be built.
 *
 * @see src/services/BinaryResolver.ts — The class under test
 * @see Issue #1939 — Extension deployment tests: verify binary bundled, resolves, connects
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { existsSync, mkdirSync, writeFileSync, rmSync, chmodSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { BinaryResolver, type BinaryResolverDeps } from "../../src/services/BinaryResolver";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

let tempDirs: string[] = [];

function makeTempDir(suffix: string): string {
  const dir = join(tmpdir(), `binary-resolver-test-${suffix}-${Date.now()}`);
  mkdirSync(dir, { recursive: true });
  tempDirs.push(dir);
  return dir;
}

function makeTempFile(dir: string, name: string, executable = false): string {
  const filePath = join(dir, name);
  writeFileSync(filePath, '#!/bin/sh\necho "0.1.0"\n');
  if (executable) {
    chmodSync(filePath, 0o755);
  }
  return filePath;
}

function makeResolver(overrides: Partial<BinaryResolverDeps>): BinaryResolver {
  const defaults: BinaryResolverDeps = {
    existsSync, // Real filesystem — no mock
    getVSCodeSetting: () => "", // Tier 1 disabled by default
    getExtensionPath: () => undefined, // Tier 3 disabled by default
    platform: "darwin",
    arch: "arm64",
    env: {}, // No env vars by default
    which: async () => null, // Tier 4 returns null by default
  };
  return new BinaryResolver({ ...defaults, ...overrides });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("BinaryResolver — real filesystem", () => {
  afterEach(() => {
    // Clean up all temp directories created during the test
    for (const dir of tempDirs) {
      try {
        rmSync(dir, { recursive: true, force: true });
      } catch {
        // ignore cleanup errors
      }
    }
    tempDirs = [];
  });

  // -------------------------------------------------------------------------
  // Tier 2: Environment variable
  // -------------------------------------------------------------------------

  describe("Tier 2: NIGHTGAUGE_GO_BINARY_PATH env var", () => {
    it("resolves when env var points to an existing file", async () => {
      const dir = makeTempDir("tier2-exists");
      const binaryPath = makeTempFile(dir, "nightgauge", true);

      const resolver = makeResolver({
        env: { NIGHTGAUGE_GO_BINARY_PATH: binaryPath },
      });

      const result = await resolver.resolve();
      expect(result).toBe(binaryPath);
    });

    it("skips tier 2 when env var path does not exist", async () => {
      const resolver = makeResolver({
        env: {
          NIGHTGAUGE_GO_BINARY_PATH: "/nonexistent/path/nightgauge",
        },
      });

      const result = await resolver.resolve();
      expect(result).toBeNull();
    });

    it("skips tier 2 when env var is empty string", async () => {
      const resolver = makeResolver({
        env: { NIGHTGAUGE_GO_BINARY_PATH: "" },
      });

      const result = await resolver.resolve();
      expect(result).toBeNull();
    });

    it("skips tier 2 when NIGHTGAUGE_GO_BINARY_PATH key is absent from env", async () => {
      const resolver = makeResolver({
        env: {}, // key not present
      });

      const result = await resolver.resolve();
      expect(result).toBeNull();
    });
  });

  // -------------------------------------------------------------------------
  // Tier 3: Bundled binary
  // -------------------------------------------------------------------------

  describe("Tier 3: extension-bundled binary", () => {
    it("resolves platform-arch named binary when it exists", async () => {
      const extPath = makeTempDir("tier3-platform");
      mkdirSync(join(extPath, "dist", "bin"), { recursive: true });
      const binaryPath = makeTempFile(
        join(extPath, "dist", "bin"),
        "nightgauge-darwin-arm64",
        true
      );

      const resolver = makeResolver({
        getExtensionPath: () => extPath,
        platform: "darwin",
        arch: "arm64",
      });

      const result = await resolver.resolve();
      expect(result).toBe(binaryPath);
      expect(result).toContain("nightgauge-darwin-arm64");
    });

    it("resolves linux-amd64 named binary", async () => {
      const extPath = makeTempDir("tier3-linux");
      mkdirSync(join(extPath, "dist", "bin"), { recursive: true });
      const binaryPath = makeTempFile(join(extPath, "dist", "bin"), "nightgauge-linux-amd64", true);

      const resolver = makeResolver({
        getExtensionPath: () => extPath,
        platform: "linux",
        arch: "x64",
      });

      const result = await resolver.resolve();
      expect(result).toBe(binaryPath);
    });

    it("resolves plain nightgauge binary as fallback when platform-arch name not present", async () => {
      const extPath = makeTempDir("tier3-plain");
      mkdirSync(join(extPath, "dist", "bin"), { recursive: true });
      const binaryPath = makeTempFile(join(extPath, "dist", "bin"), "nightgauge", true);

      const resolver = makeResolver({
        getExtensionPath: () => extPath,
        platform: "darwin",
        arch: "arm64",
        // No platform-arch binary exists — only the plain one
      });

      const result = await resolver.resolve();
      expect(result).toBe(binaryPath);
      expect(result).toContain(join("dist", "bin", "nightgauge"));
    });

    it("skips tier 3 entirely when getExtensionPath returns undefined", async () => {
      const resolver = makeResolver({
        getExtensionPath: () => undefined,
      });

      const result = await resolver.resolve();
      expect(result).toBeNull();
    });

    it("returns null when extensionPath exists but dist/bin/ is empty", async () => {
      const extPath = makeTempDir("tier3-empty");
      mkdirSync(join(extPath, "dist", "bin"), { recursive: true });
      // No binary files created

      const resolver = makeResolver({
        getExtensionPath: () => extPath,
        platform: "darwin",
        arch: "arm64",
      });

      const result = await resolver.resolve();
      expect(result).toBeNull();
    });
  });

  // -------------------------------------------------------------------------
  // Priority ordering between tiers
  // -------------------------------------------------------------------------

  describe("tier priority ordering", () => {
    it("tier 2 env var takes priority over tier 3 bundled binary", async () => {
      const envDir = makeTempDir("priority-env");
      const envBinary = makeTempFile(envDir, "nightgauge-from-env", true);

      const extPath = makeTempDir("priority-bundled");
      mkdirSync(join(extPath, "dist", "bin"), { recursive: true });
      makeTempFile(join(extPath, "dist", "bin"), "nightgauge-darwin-arm64", true);

      const resolver = makeResolver({
        env: { NIGHTGAUGE_GO_BINARY_PATH: envBinary },
        getExtensionPath: () => extPath,
        platform: "darwin",
        arch: "arm64",
      });

      const result = await resolver.resolve();
      expect(result).toBe(envBinary);
      expect(result).toContain("nightgauge-from-env");
    });

    it("tier 1 VSCode setting takes priority over tier 2 env var", async () => {
      const settingDir = makeTempDir("priority-setting");
      const settingBinary = makeTempFile(settingDir, "nightgauge-from-setting", true);

      const envDir = makeTempDir("priority-env2");
      const envBinary = makeTempFile(envDir, "nightgauge-from-env", true);

      const resolver = makeResolver({
        getVSCodeSetting: () => settingBinary,
        env: { NIGHTGAUGE_GO_BINARY_PATH: envBinary },
      });

      const result = await resolver.resolve();
      expect(result).toBe(settingBinary);
    });

    it("tier 1 skipped when VSCode setting path does not exist on filesystem", async () => {
      const envDir = makeTempDir("tier1-skip");
      const envBinary = makeTempFile(envDir, "nightgauge", true);

      const resolver = makeResolver({
        getVSCodeSetting: () => "/nonexistent/path/from/setting",
        env: { NIGHTGAUGE_GO_BINARY_PATH: envBinary },
      });

      // Tier 1 path doesn't exist → falls through to tier 2
      const result = await resolver.resolve();
      expect(result).toBe(envBinary);
    });
  });

  // -------------------------------------------------------------------------
  // Tier 4: System PATH
  // -------------------------------------------------------------------------

  describe("Tier 4: system PATH via which()", () => {
    it("returns path from which() when no other tier resolves", async () => {
      const resolver = makeResolver({
        which: async () => "/usr/local/bin/nightgauge",
      });

      const result = await resolver.resolve();
      expect(result).toBe("/usr/local/bin/nightgauge");
    });

    it("returns null when all tiers fail including which()", async () => {
      const resolver = makeResolver({
        // All defaults: no setting, no env, no extPath, which returns null
      });

      const result = await resolver.resolve();
      expect(result).toBeNull();
    });
  });

  // -------------------------------------------------------------------------
  // Real BinaryResolver.fromVSCode() factory smoke check
  // -------------------------------------------------------------------------

  describe("BinaryResolver.fromVSCode() factory", () => {
    it("creates a BinaryResolver instance without throwing", () => {
      // fromVSCode() lazy-requires 'vscode' — the setup.ts mock handles this.
      // This test ensures the factory constructs without errors in a test env.
      expect(() => BinaryResolver.fromVSCode()).not.toThrow();
    });

    it("returns a BinaryResolver with a resolve() method", () => {
      const resolver = BinaryResolver.fromVSCode();
      expect(typeof resolver.resolve).toBe("function");
    });
  });
});
