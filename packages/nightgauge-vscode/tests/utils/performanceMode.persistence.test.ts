/**
 * performanceMode.persistence.test.ts (Issue #3009)
 *
 * Round-trip + precedence tests for `getPerformanceMode` and
 * `writePerformanceModeStateFile`. Mirrors the legacy
 * isSuperchargeModeActive.test.ts pattern: the file is written to the
 * primary VS Code workspace; pipeline stages may run under a different
 * root, so the read path checks primary first then falls back to the
 * passed-in workspace root.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "__PRIMARY__" } }],
  },
}));

import * as vscode from "vscode";
import {
  getPerformanceMode,
  writePerformanceModeStateFile,
} from "../../src/utils/resolvers/monitoringResolver";

describe("performance-mode persistence", () => {
  let primaryRoot: string;
  let stageRoot: string;
  const originalEnv = process.env;

  beforeEach(() => {
    primaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), "perf-primary-"));
    stageRoot = fs.mkdtempSync(path.join(os.tmpdir(), "perf-stage-"));
    vi.mocked(vscode.workspace).workspaceFolders = [
      { uri: { fsPath: primaryRoot } } as vscode.WorkspaceFolder,
    ];
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
    delete process.env.NIGHTGAUGE_SUPERCHARGE;
  });

  afterEach(() => {
    fs.rmSync(primaryRoot, { recursive: true, force: true });
    fs.rmSync(stageRoot, { recursive: true, force: true });
    process.env = originalEnv;
  });

  it("defaults to elevated when no state file or env var is present", () => {
    expect(getPerformanceMode(stageRoot)).toBe("elevated");
  });

  it("write + read round-trips for each mode", () => {
    for (const mode of ["efficiency", "elevated", "maximum"] as const) {
      writePerformanceModeStateFile(primaryRoot, mode);
      const filePath = path.join(primaryRoot, ".nightgauge", "performance-mode.yaml");
      expect(fs.existsSync(filePath)).toBe(true);
      expect(getPerformanceMode(stageRoot)).toBe(mode);
    }
  });

  it("env var beats state file", () => {
    writePerformanceModeStateFile(primaryRoot, "elevated");
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "maximum";
    expect(getPerformanceMode(stageRoot)).toBe("maximum");
  });

  it("env var ignores unknown values and falls back to file/default", () => {
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "supercharge";
    writePerformanceModeStateFile(primaryRoot, "efficiency");
    expect(getPerformanceMode(stageRoot)).toBe("efficiency");
  });

  it("primary workspace wins over stage root when both have files", () => {
    writePerformanceModeStateFile(primaryRoot, "maximum");
    writePerformanceModeStateFile(stageRoot, "efficiency");
    expect(getPerformanceMode(stageRoot)).toBe("maximum");
  });

  it("falls back to passed-in workspaceRoot when there is no primary workspace", () => {
    vi.mocked(vscode.workspace).workspaceFolders = undefined;
    writePerformanceModeStateFile(stageRoot, "maximum");
    expect(getPerformanceMode(stageRoot)).toBe("maximum");
  });

  it("ignores malformed YAML and returns the default", () => {
    fs.mkdirSync(path.join(primaryRoot, ".nightgauge"), { recursive: true });
    fs.writeFileSync(
      path.join(primaryRoot, ".nightgauge", "performance-mode.yaml"),
      "this is not valid yaml :::\n",
      "utf-8"
    );
    expect(getPerformanceMode(stageRoot)).toBe("elevated");
  });
});
